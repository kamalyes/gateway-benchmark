/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:21:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:21:00
 * @FilePath: \gateway-benchmark\driver\main.go
 * @Description: 压测驱动 - 固定并发打满，报 RPS 与延迟分位
 *
 * 自己写而不是用 wrk / hey / vegeta：这些都要装外部二进制，
 * 而驱动本身的实现会影响结论（连接复用、读完响应体与否、计时点），
 * 写进仓库才能让别人复现出同一个数
 *
 * [EN] Hand-written rather than wrk / hey / vegeta, all of which need an external
 * binary, and whose own behaviour shapes the result (connection reuse, whether the
 * body is drained, where the clock starts). Committing it is what makes the numbers
 * reproducible
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	// Scheme 这一轮属于哪个被测方案，报告生成器按它分组
	// 手写报告时数字靠人从输出里抄，抄错与过期都看不出来，所以分组标识必须随数据落盘
	//
	// [EN] Which subject this round measured; the report generator groups by it
	// Hand-written reports copy numbers out of the output by eye, where a typo and a
	// stale figure look the same, so the grouping key is recorded with the data
	Scheme     string  `json:"scheme"`
	Shape      string  `json:"shape"`
	Name       string  `json:"name"`
	URL        string  `json:"url"`
	Conns      int     `json:"conns"`
	Duration   string  `json:"duration"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	NonOK      int64   `json:"non_ok"`
	RPS        float64 `json:"rps"`
	P50us      float64 `json:"p50_us"`
	P90us      float64 `json:"p90_us"`
	P99us      float64 `json:"p99_us"`
	P999us     float64 `json:"p999_us"`
	MaxUs      float64 `json:"max_us"`
	MeanUs     float64 `json:"mean_us"`
	BytesPerRe int64   `json:"bytes_per_response"`

	// 环境信息随数据落盘，报告里的环境一节也不必人工维护
	// [EN] Environment travels with the data so the report's environment section is not hand-kept
	GoVersion  string `json:"go_version"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	RecordedAt string `json:"recorded_at"`
}

func main() {
	url := flag.String("url", "", "目标 URL")
	name := flag.String("name", "", "这一轮的名字")
	conns := flag.Int("conns", 64, "并发连接数")
	dur := flag.Duration("duration", 10*time.Second, "测量时长")
	warmup := flag.Duration("warmup", 3*time.Second, "预热时长，预热期的样本不计入")
	body := flag.String("body", `{"id":"bench","seq":1,"text":"hello"}`, "请求体")
	out := flag.String("out", "", "结果写到这个 JSON 文件，留空则只打印")
	scheme := flag.String("scheme", "", "被测方案标识，如 go-zero / go-rpc-gateway / baseline-json")
	shape := flag.String("shape", "", "形态标识，如 A / B")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "需要 -url")
		os.Exit(2)
	}

	payload := []byte(*body)

	// 每个 worker 一个 Transport：共用一个 Transport 时它内部的连接池和锁
	// 会成为驱动自己的瓶颈，测出来的是驱动而不是被测对象
	// [EN] One Transport per worker: sharing one makes its connection pool and locks
	// the driver's own bottleneck, so the measurement reflects the driver, not the target
	clients := make([]*http.Client, *conns)
	for i := range clients {
		clients[i] = &http.Client{
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
				MaxIdleConns:        2,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
			Timeout: 10 * time.Second,
		}
	}

	var (
		measuring atomic.Bool
		stop      atomic.Bool
		requests  atomic.Int64
		errs      atomic.Int64
		nonOK     atomic.Int64
		respBytes atomic.Int64
	)

	// 每个 worker 独立的延迟切片，最后合并：共享切片要加锁，锁会污染测量
	// [EN] Each worker keeps its own latency slice and they are merged at the end:
	// a shared slice needs a lock, and the lock would contaminate the measurement
	lat := make([][]time.Duration, *conns)
	for i := range lat {
		lat[i] = make([]time.Duration, 0, 1<<16)
	}

	var wg sync.WaitGroup
	for w := 0; w < *conns; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			client := clients[w]
			for !stop.Load() {
				start := time.Now()
				resp, err := client.Post(*url, "application/json", bytes.NewReader(payload))
				if err != nil {
					if measuring.Load() {
						errs.Add(1)
					}
					continue
				}
				// 必须读完并关闭，否则连接不回池，下一次是新建连接，
				// 测出来会变成「建连开销」而不是「请求开销」
				// [EN] The body must be drained and closed or the connection is not
				// reused, turning the measurement into connection setup cost
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				elapsed := time.Since(start)

				if !measuring.Load() {
					continue
				}
				requests.Add(1)
				respBytes.Store(n)
				if resp.StatusCode != http.StatusOK {
					nonOK.Add(1)
				}
				lat[w] = append(lat[w], elapsed)
			}
		}(w)
	}

	time.Sleep(*warmup)
	measuring.Store(true)
	measureStart := time.Now()
	time.Sleep(*dur)
	measuring.Store(false)
	actual := time.Since(measureStart)
	stop.Store(true)
	wg.Wait()

	all := make([]time.Duration, 0, requests.Load())
	for _, s := range lat {
		all = append(all, s...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	r := result{
		Scheme:     *scheme,
		Shape:      *shape,
		Name:       *name,
		URL:        *url,
		Conns:      *conns,
		Duration:   actual.Round(time.Millisecond).String(),
		Requests:   requests.Load(),
		Errors:     errs.Load(),
		NonOK:      nonOK.Load(),
		BytesPerRe: respBytes.Load(),
		GoVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		RecordedAt: time.Now().Format(time.RFC3339),
	}
	if len(all) > 0 {
		r.RPS = float64(len(all)) / actual.Seconds()
		r.P50us = us(pct(all, 0.50))
		r.P90us = us(pct(all, 0.90))
		r.P99us = us(pct(all, 0.99))
		r.P999us = us(pct(all, 0.999))
		r.MaxUs = us(all[len(all)-1])
		var sum time.Duration
		for _, d := range all {
			sum += d
		}
		r.MeanUs = us(sum / time.Duration(len(all)))
	}

	fmt.Printf("%-28s RPS %9.0f  p50 %8.1fus  p90 %8.1fus  p99 %9.1fus  p99.9 %10.1fus  错误 %d  非200 %d  GOMAXPROCS=%d\n",
		r.Name, r.RPS, r.P50us, r.P90us, r.P99us, r.P999us, r.Errors, r.NonOK, runtime.GOMAXPROCS(0))

	if *out != "" {
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写 %s 失败: %v\n", *out, err)
			os.Exit(1)
		}
	}
}

// pct 取排序后切片的分位值
// [EN] pct returns the percentile value of an already-sorted slice
func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func us(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1000 }

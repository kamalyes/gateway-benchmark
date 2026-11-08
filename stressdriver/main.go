/*
 * @Description: 用 go-stress 当压测引擎的驱动 - 与自建 driver 输出同一份结果 JSON
 *
 * 为什么要第二个驱动而不是替掉第一个：
 * 压测里最难自证的一件事是「这个数字是被测对象的，不是驱动自己的」。单驱动做不到
 * 自证 —— 驱动的连接复用策略、计时点、worker 模型都会直接改变结论，而这些错误不会
 * 报错，只会让数字偏向某一侧。两个独立实现跑同一档，数字一致才说明测的是网关；
 * 不一致就定位到驱动，这比任何一个驱动的注释都有说服力
 *
 * go-stress（github.com/kamalyes/go-stress）是独立的压测引擎：自己的 worker 池、
 * 自己的客户端池、自己的延迟直方图。它与 driver/ 唯一共享的是「往同一个 URL 发同样的
 * POST」这件事本身
 *
 * 两者口径上已知的三处差异，读数时必须记住（都写进了结果 JSON 的 driver 字段）：
 *
 *  1. 预热 driver/ 用「跑着但不记样本」的方式预热，连接在预热期就已建立，
 *     测量期一条新连接都不建 go-stress 没有「丢弃样本」模式，这里用一轮独立的
 *     短压测当预热，测量轮会重新建连 —— 于是测量期前 N 个请求里含 TCP 握手
 *     N = 并发数，相对几十万次请求可以忽略，但它确实在样本里
 *  2. 分位数 go-stress 的 Report 给 p50/p90/p95/p99，没有 p99.9
 *     所以结果 JSON 里 p999_us 写 0，另外多给一个 p95_us
 *  3. 客户端拓扑 driver/ 刻意一个 worker 一个 http.Transport（共用会让 Transport
 *     内部的连接池锁成为驱动自己的瓶颈）go-stress 用它自己的 ClientPool，
 *     这正是两者该互相印证的地方：如果高并发档 go-stress 明显更低，
 *     那是驱动侧的瓶颈，不是网关变慢了
 */

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"time"

	"github.com/kamalyes/go-stress/config"
	"github.com/kamalyes/go-stress/executor"
	stresslog "github.com/kamalyes/go-stress/logger"
	"github.com/kamalyes/go-stress/statistics"
	"github.com/kamalyes/go-stress/types"
)

// result 与 driver/main.go 的输出结构逐字一致，另加 driver 与 p95_us
// 保持一致是为了让 bootstrap/report 不用改就能同时吃两个驱动的结果
//
// [EN] Byte-for-byte the same shape as driver/main.go's output plus driver and p95_us, so
// bootstrap/report consumes both drivers' results without changes
type result struct {
	Scheme string `json:"scheme"`
	Shape  string `json:"shape"`
	Name   string `json:"name"`
	// Driver 哪个驱动产生的这条结果 两个驱动的数字放在一起看时，这是唯一的区分依据
	// [EN] Which driver produced this row, the only way to tell the two apart in one table
	Driver     string  `json:"driver"`
	URL        string  `json:"url"`
	Conns      int     `json:"conns"`
	Duration   string  `json:"duration"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	NonOK      int64   `json:"non_ok"`
	RPS        float64 `json:"rps"`
	P50us      float64 `json:"p50_us"`
	P90us      float64 `json:"p90_us"`
	P95us      float64 `json:"p95_us"`
	P99us      float64 `json:"p99_us"`
	P999us     float64 `json:"p999_us"`
	MaxUs      float64 `json:"max_us"`
	MeanUs     float64 `json:"mean_us"`
	BytesPerRe int64   `json:"bytes_per_response"`
	GoVersion  string  `json:"go_version"`
	GOMAXPROCS int     `json:"gomaxprocs"`
	OS         string  `json:"os"`
	Arch       string  `json:"arch"`
	RecordedAt string  `json:"recorded_at"`
}

func main() {
	url := flag.String("url", "", "目标 URL")
	name := flag.String("name", "", "这一轮的名字")
	conns := flag.Int("conns", 64, "并发数")
	dur := flag.Duration("duration", 10*time.Second, "测量时长")
	warmup := flag.Duration("warmup", 3*time.Second, "预热时长，预热轮的样本不计入")
	body := flag.String("body", `{"id":"bench","seq":1,"text":"hello"}`, "请求体")
	out := flag.String("out", "", "结果写到这个 JSON 文件，留空则只打印")
	scheme := flag.String("scheme", "", "被测方案标识")
	shape := flag.String("shape", "", "形态标识，如 A / B")
	timeout := flag.Duration("timeout", 10*time.Second, "单请求超时")
	// cpuprofile 只覆盖测量轮，不含预热：要找的是「稳态下驱动自己把时间花在哪」
	// 驱动与被测对象在同一台机器上抢核，所以驱动侧的开销不只是它自己的事 ——
	// 它会直接压低被测对象能拿到的 CPU
	// [EN] The CPU profile covers the measured round only, not the warmup: the question is
	// where the driver spends time in steady state. The driver competes for cores with the
	// subject, so its own cost is not merely its own problem -- it directly reduces the CPU the
	// subject can get
	cpuprofile := flag.String("cpuprofile", "", "把测量轮的 CPU profile 写到这个文件")
	// 吞吐不达标时 CPU profile 往往是错的工具
	//
	// 压测驱动的吞吐 = 并发数 / 每次迭代的墙上时间，而迭代时间里「请求之外」那部分
	// 既可能是 CPU，也可能是等锁、等 channel、等调度 —— 后者在 CPU profile 里根本不出现，
	// 因为 goroutine 一阻塞就从 CPU 上下来了
	// 实测本驱动 8 并发 63k RPS、p50 83 µs，即每次迭代 127 µs，请求之外 44 µs，
	// 而 CPU 上只有 0.5 µs 量级 —— 那 44 µs 必须用 mutex / block profile 才看得见
	// [EN] A CPU profile is often the wrong tool for a throughput shortfall: throughput is
	// concurrency divided by per-iteration wall time, and the non-request part of that time may
	// be lock, channel or scheduler waiting, none of which appears in a CPU profile because a
	// blocked goroutine is off the CPU. Measured here: 63k RPS at 8 workers with p50 83 µs is
	// 127 µs per iteration, 44 µs of it outside the request, against roughly 0.5 µs of CPU
	mutexprofile := flag.String("mutexprofile", "", "把测量轮的 mutex 竞争 profile 写到这个文件")
	blockprofile := flag.String("blockprofile", "", "把测量轮的阻塞 profile 写到这个文件")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr, "需要 -url")
		os.Exit(2)
	}

	// 日志压到 ERROR：压测驱动自己打日志会掺进测量，也会淹掉编排脚本的输出
	// 实时报告服务器与逐秒表格都在 INFO，压到 ERROR 之后它们一并安静
	// [EN] The driver's own logging would fold into the measurement and bury the
	// orchestration script's output; the realtime report server and the per-second table are
	// both at INFO and go quiet at ERROR
	log := stresslog.New().WithLevel(stresslog.ERROR)

	cfgFor := func(d time.Duration) func() *config.Config {
		return func() *config.Config {
			cfg := config.DefaultConfig()
			cfg.Protocol = types.ProtocolHTTP
			cfg.URL = *url
			cfg.Method = "POST"
			cfg.Body = *body
			cfg.Headers = map[string]string{"Content-Type": "application/json"}
			cfg.Concurrency = uint64(*conns)
			cfg.Timeout = *timeout
			// 按时长压测时请求数必须清零，两者并存时 Worker 以 reqCount==0 判断走时长模式
			// [EN] The request count must be zero in duration mode; the worker decides by
			// reqCount == 0
			cfg.Duration = d
			cfg.Requests = 0
			if cfg.Advanced == nil {
				cfg.Advanced = &config.AdvancedConfig{}
			}
			// 与自建 driver 发**一模一样**的请求：它只带 Content-Type
			// go-stress 默认会注入 traceparent，而网关的 tracing 中间件会据它续链路，
			// 多出来的那段工作只在一侧发生，两边就不是同一个被测路径了
			// （v1.0.1 只注入 traceparent 这一个头，所以关掉它就对齐了）
			// [EN] The exact same request as the built-in driver, which sends only
			// Content-Type. go-stress injects a traceparent by default and the gateway's
			// tracing middleware continues that trace, work that would happen on one side
			// only. v1.0.1 injects just this one header, so disabling it aligns the two
			cfg.Advanced.DisableTraceHeader = true
			// 关掉实时报告服务器：每轮都要起一个 HTTP 服务（默认 :8088），
			// 既白占端口（同机并排跑两轮就冲突），又让驱动在测量期多带一个服务端
			// [EN] The realtime report server is turned off: every round otherwise starts an
			// HTTP server (:8088 by default), which both occupies a port -- two rounds side by
			// side on one machine collide -- and leaves the driver hosting a server while
			// measuring
			cfg.Advanced.DisableRealtime = true
			cfg.HTTP = &config.HTTPConfig{
				KeepAlive:       true,
				FollowRedirects: true,
				MaxConnsPerHost: *conns * 2,
			}
			return cfg
		}
	}

	run := func(d time.Duration) *statistics.Report {
		res := executor.RunTask(executor.RunOptions{
			ConfigFunc:  cfgFor(d),
			Concurrency: uint64(*conns),
			Duration:    d,
			Timeout:     *timeout,
			StorageMode: executor.StorageModeMemory,
			Logger:      log,
			NoReport:    true,
			NoPrint:     true,
			NoWait:      true,
		})
		if res.Error != nil {
			fmt.Fprintf(os.Stderr, "go-stress 运行失败: %v\n", res.Error)
			os.Exit(1)
		}
		return res.Report
	}

	// 预热轮的报告整份丢掉
	// [EN] The warmup round's report is discarded entirely
	if *warmup > 0 {
		run(*warmup)
	}

	// 采样率在预热之后才打开，于是 profile 只覆盖测量轮
	// [EN] The rates are enabled after the warmup so the profiles cover the measured round only
	if *mutexprofile != "" {
		runtime.SetMutexProfileFraction(1)
	}
	if *blockprofile != "" {
		// 1 表示每次阻塞事件都记：开销不小，但这是诊断用的一次性运行
		// [EN] 1 records every blocking event: costly, but this is a one-off diagnostic run
		runtime.SetBlockProfileRate(1)
	}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "创建 profile 文件失败: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "启动 CPU profile 失败: %v\n", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}

	rep := run(*dur)
	if rep == nil || rep.TotalRequests == 0 {
		fmt.Fprintln(os.Stderr, "这一轮一个请求都没完成 —— 别把它当成一个慢的结果，它是一次失败")
		os.Exit(1)
	}

	for name, path := range map[string]string{"mutex": *mutexprofile, "block": *blockprofile} {
		if path == "" {
			continue
		}
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "创建 %s profile 失败: %v\n", name, err)
			os.Exit(1)
		}
		if err := pprof.Lookup(name).WriteTo(f, 0); err != nil {
			fmt.Fprintf(os.Stderr, "写 %s profile 失败: %v\n", name, err)
			os.Exit(1)
		}
		_ = f.Close()
	}

	var nonOK int64
	for code, n := range rep.StatusCodes {
		if code != 200 {
			nonOK += int64(n)
		}
	}

	r := result{
		Scheme:     *scheme,
		Shape:      *shape,
		Name:       *name,
		Driver:     "go-stress",
		URL:        *url,
		Conns:      *conns,
		Duration:   rep.TotalTime.Round(time.Millisecond).String(),
		Requests:   int64(rep.TotalRequests),
		Errors:     int64(rep.FailedRequests),
		NonOK:      nonOK,
		RPS:        rep.QPS,
		P50us:      us(rep.P50Latency),
		P90us:      us(rep.P90Latency),
		P95us:      us(rep.P95Latency),
		P99us:      us(rep.P99Latency),
		MaxUs:      us(rep.MaxLatency),
		MeanUs:     us(rep.AvgLatency),
		GoVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		RecordedAt: time.Now().Format(time.RFC3339),
	}
	if rep.TotalRequests > 0 {
		r.BytesPerRe = int64(rep.TotalSize / float64(rep.TotalRequests))
	}

	// 失败或非 200 要喊出来：失败路径通常比成功路径便宜得多，
	// 一组全是 429 的结果看起来会非常漂亮
	// [EN] Failures must be loud: a failure path is usually far cheaper than the success path,
	// and a round of 429s produces very pretty numbers
	if r.Errors > 0 || r.NonOK > 0 {
		fmt.Fprintf(os.Stderr,
			"⚠️  %s: %d 次失败、%d 次非 200 —— 这一轮量到的不全是成功路径\n",
			r.Name, r.Errors, r.NonOK)
		// 把错误字符串逐条打出来：localhost 目标上的失败几乎一定是驱动侧的问题
		// （握手、连接池、超时），只报一个计数就查不下去了
		// [EN] The error strings are printed one by one: a failure against a localhost target is
		// almost certainly driver-side (handshake, connection pool, timeout), and a bare count
		// leaves nothing to investigate
		keys := make([]string, 0, len(rep.Errors))
		for k := range rep.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "    %6d × %s\n", rep.Errors[k], k)
		}
		for code, n := range rep.StatusCodes {
			if code != 200 {
				fmt.Fprintf(os.Stderr, "    %6d × HTTP %d\n", n, code)
			}
		}
	}

	fmt.Printf("%-28s driver=go-stress conns=%d rps=%.0f p50=%.0fus p99=%.0fus errors=%d non200=%d\n",
		r.Name, r.Conns, r.RPS, r.P50us, r.P99us, r.Errors, r.NonOK)

	if *out != "" {
		blob, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化结果失败: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, append(blob, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", *out, err)
			os.Exit(1)
		}
	}
}

// us 纳秒转微秒
func us(d time.Duration) float64 { return float64(d) / 1000.0 }

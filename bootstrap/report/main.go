/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-03 21:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-03 22:10:00
 * @FilePath: \gateway-benchmark\bootstrap\report\main.go
 * @Description: 报告生成器 - 读 results/*.json 出报告，数字一个都不手打
 *
 * 存在的理由：手写报告时数字靠人从基准输出里抄进 markdown 表格，
 * 抄错与过期在文档里长得一模一样，读者无从分辨，作者自己过几天也忘了哪些是旧的
 * 改动一轮代码就要全文重抄一遍，于是必然漂
 *
 * 这里把分工定死：方法论与警示是人写的散文，放在 template.md；
 * 所有数字由本程序从 results/*.json 算出并填进去，模板里只有占位符
 *
 * [EN] The report generator exists because hand-written numbers drift
 * Copying figures from benchmark output into markdown tables makes a typo and a stale
 * value look identical in the document, and every code change would require retyping
 * the whole table
 * The split is enforced here: methodology and caveats are prose in template.md, while
 * every number is computed by this program from results/*.json and substituted in
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// run 与 driver 落盘的 JSON 一一对应
type run struct {
	Scheme     string  `json:"scheme"`
	Shape      string  `json:"shape"`
	Name       string  `json:"name"`
	Conns      int     `json:"conns"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	NonOK      int64   `json:"non_ok"`
	RPS        float64 `json:"rps"`
	P50us      float64 `json:"p50_us"`
	P90us      float64 `json:"p90_us"`
	P99us      float64 `json:"p99_us"`
	P999us     float64 `json:"p999_us"`
	MeanUs     float64 `json:"mean_us"`
	BytesPerRe int64   `json:"bytes_per_response"`
	GoVersion  string  `json:"go_version"`
	GOMAXPROCS int     `json:"gomaxprocs"`
	OS         string  `json:"os"`
	Arch       string  `json:"arch"`
}

// group 同一个 (形态, 方案, 并发) 的多轮
type group struct {
	shape, scheme string
	conns         int
	runs          []run
}

func (g *group) key() string { return fmt.Sprintf("%s|%s|%d", g.shape, g.scheme, g.conns) }

// median 取中位数 单轮数字不可信，报告里一律报中位数并同时给出轮数与极差
// [EN] The median across rounds; a single round is not trustworthy, so the report always
// states the median together with the round count and the spread
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func spread(vs []float64) float64 {
	if len(vs) < 2 {
		return 0
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	lo, hi := s[0], s[len(s)-1]
	if lo == 0 {
		return 0
	}
	return (hi - lo) / lo * 100
}

func pick(rs []run, f func(run) float64) []float64 {
	out := make([]float64, 0, len(rs))
	for _, r := range rs {
		out = append(out, f(r))
	}
	return out
}

func main() {
	dir := flag.String("results", "results", "结果 JSON 目录")
	tmpl := flag.String("template", "template.md", "报告模板")
	out := flag.String("out", "REPORT.md", "输出文件")
	flag.Parse()

	files, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "在 %s 下没找到结果 JSON，先跑 driver\n", *dir)
		os.Exit(1)
	}

	groups := map[string]*group{}
	var order []string
	var env run
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读 %s: %v\n", f, err)
			os.Exit(1)
		}
		var r run
		if err := json.Unmarshal(b, &r); err != nil {
			fmt.Fprintf(os.Stderr, "解析 %s: %v\n", f, err)
			os.Exit(1)
		}
		if r.Scheme == "" {
			fmt.Fprintf(os.Stderr, "%s 没有 scheme 字段，无法分组 —— 跑 driver 时要带 -scheme\n", f)
			os.Exit(1)
		}
		if env.GoVersion == "" {
			env = r
		}
		g := &group{shape: r.Shape, scheme: r.Scheme, conns: r.Conns}
		if ex, ok := groups[g.key()]; ok {
			ex.runs = append(ex.runs, r)
		} else {
			g.runs = append(g.runs, r)
			groups[g.key()] = g
			order = append(order, g.key())
		}
	}
	sort.Strings(order)

	// 模板支持的占位符：{{表格:形态}}、{{要点:形态}} 与 {{环境}}
	//
	// 不提供「{{对比:形态:方案A:方案B}}」这类两两对比的占位符：方案不止两个，
	// 挑哪两个来对比本身就是一次取舍，写进生成器等于把这个取舍藏进代码
	// 要对比就看 {{表格:形态}}，它把该形态下所有方案并排列出来
	//
	// [EN] The template supports exactly two placeholders, {{table:shape}} and {{env}}
	// No pairwise-comparison placeholder is offered: there are more than two subjects, and
	// choosing which two to compare is itself an editorial choice that would be buried in
	// the generator; the per-shape table lists every subject side by side instead
	content, err := os.ReadFile(*tmpl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读模板 %s: %v\n", *tmpl, err)
		os.Exit(1)
	}
	doc := string(content)

	shapes := map[string]bool{}
	for _, k := range order {
		shapes[groups[k].shape] = true
	}
	for shape := range shapes {
		doc = strings.ReplaceAll(doc, fmt.Sprintf("{{表格:%s}}", shape), renderTable(groups, order, shape))
		doc = strings.ReplaceAll(doc, fmt.Sprintf("{{要点:%s}}", shape), renderHighlights(groups, order, shape))
	}
	doc = strings.ReplaceAll(doc, "{{环境}}", renderEnv(env, groups, order))

	if strings.Contains(doc, "{{") {
		i := strings.Index(doc, "{{")
		j := strings.Index(doc[i:], "}}")
		tok := doc[i : i+j+2]
		fmt.Fprintf(os.Stderr, "模板里有没被填充的占位符 %s —— 对应的结果数据不存在，拒绝生成半成品报告\n", tok)
		os.Exit(1)
	}

	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写 %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s，取材 %d 个结果文件、%d 组\n", *out, len(files), len(order))
}

// 基线方案名 要点里的两个比值都以它们为分母
// [EN] The baseline scheme names used as denominators in the highlights
const (
	floorScheme   = "net-http-json"
	transcodeBase = "bare-grpc-gateway"
)

// renderHighlights 把一个形态的要点算出来
//
// 为什么幅度由生成器算而不是手写进模板
//
// 手写的数字下次重跑就过期，而「文档必须对齐实现」要防的正是这个
// 更要紧的是：交给生成器算之后，吃亏的那几个比值会**自动出现在报告里**
// 手写的时候，漏掉一个不利的比值不需要任何恶意，只要忘了就行
//
// 两个分母都是基线而不是别的方案：
//
//	net-http-json      Go HTTP 服务的地板，所有方案都不可能比它快
//	bare-grpc-gateway  裸 ServeMux，与付转码的方案走同一条转码路径但不挂中间件
//	                   所以「相对它的比值」就是各方案自己中间件链的代价
//
// [EN] renderHighlights computes a shape's highlights
// The magnitudes are computed rather than written into the template by hand, because
// hand-written numbers go stale on the next run, and more importantly because computing them
// makes the unflattering ratios appear in the report automatically: when they are written by
// hand, omitting an unfavourable one needs no malice, only forgetfulness
// Both denominators are baselines rather than other subjects: net-http-json is the floor of a
// Go HTTP service that nothing can beat, and bare-grpc-gateway is a bare ServeMux taking the
// same transcoding path as the subjects that pay for it but with no middleware, so the ratio
// against it is the cost of each subject's own middleware chain
func renderHighlights(groups map[string]*group, order []string, shape string) string {
	// 按并发档收集本形态的方案，以及同并发档下两条基线的中位值
	// [EN] Collects this shape's subjects per concurrency level, plus the two baselines
	type cell struct {
		scheme string
		rps    float64
	}
	byConns := map[int][]cell{}
	floor := map[int]float64{}
	base := map[int]float64{}
	// 两条基线无条件收集，不走 else 分支
	//
	// 早先用的是 switch：shape 等于 baseline 时，两条基线自己的组先命中了
	// 「本形态」那一支，于是 floor 与 base 永远是空的，baseline 那张要点表里两列
	// 全是「没有这条基线的数据」 —— 基线对自己的比值本来就该是 1.00x 与可比的
	//
	// [EN] Both baselines are collected unconditionally rather than in else branches
	// A switch was used at first, so when shape is baseline the baselines' own groups matched
	// the this-shape branch and floor and base stayed empty, leaving both columns of the
	// baseline highlights reading as having no data, when a baseline's ratio against itself
	// is simply 1.00x and perfectly comparable
	for _, k := range order {
		g := groups[k]
		m := median(rpsOf(g))
		if g.shape == shape {
			byConns[g.conns] = append(byConns[g.conns], cell{g.scheme, m})
		}
		switch g.scheme {
		case floorScheme:
			floor[g.conns] = m
		case transcodeBase:
			base[g.conns] = m
		}
	}
	if len(byConns) == 0 {
		return "（本形态没有结果数据）\n"
	}

	conns := make([]int, 0, len(byConns))
	for c := range byConns {
		conns = append(conns, c)
	}
	sort.Ints(conns)

	var b strings.Builder
	b.WriteString("| 并发 | 按 RPS 中位排序 | 相对 net/http 地板 | 相对裸 grpc-gateway mux |\n")
	b.WriteString("|---:|---|---|---|\n")
	for _, c := range conns {
		cells := byConns[c]
		sort.Slice(cells, func(i, j int) bool { return cells[i].rps > cells[j].rps })

		rank := make([]string, 0, len(cells))
		vsFloor := make([]string, 0, len(cells))
		vsBase := make([]string, 0, len(cells))
		for _, cl := range cells {
			rank = append(rank, fmt.Sprintf("%s %.1fk", cl.scheme, cl.rps/1000))
			if f := floor[c]; f > 0 {
				vsFloor = append(vsFloor, fmt.Sprintf("%s %.2fx", cl.scheme, cl.rps/f))
			}
			if bs := base[c]; bs > 0 {
				vsBase = append(vsBase, fmt.Sprintf("%s %.2fx", cl.scheme, cl.rps/bs))
			}
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", c,
			strings.Join(rank, " > "),
			joinOrDash(vsFloor), joinOrDash(vsBase))
	}
	return b.String()
}

// joinOrDash 没有可比的基线时给一个破折号，而不是留空让人以为是 1.00x
// [EN] A dash when there is no baseline to compare against, rather than a blank that reads
// as 1.00x
func joinOrDash(parts []string) string {
	if len(parts) == 0 {
		return "—（本轮没有这条基线的数据）"
	}
	return strings.Join(parts, "，")
}

// rpsOf 取一组里各轮的 RPS
// [EN] rpsOf returns the per-round RPS of one group
func rpsOf(g *group) []float64 {
	out := make([]float64, 0, len(g.runs))
	for _, r := range g.runs {
		out = append(out, r.RPS)
	}
	return out
}

func renderTable(groups map[string]*group, order []string, shape string) string {
	var b strings.Builder
	b.WriteString("| 并发 | 方案 | 轮数 | RPS(中位) | RPS极差 | p50 | p90 | p99 | p99.9 | 响应体 | 错误 |\n")
	b.WriteString("|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	type row struct {
		conns  int
		scheme string
		g      *group
	}
	var rows []row
	for _, k := range order {
		g := groups[k]
		if g.shape != shape {
			continue
		}
		rows = append(rows, row{g.conns, g.scheme, g})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].conns != rows[j].conns {
			return rows[i].conns < rows[j].conns
		}
		return rows[i].scheme < rows[j].scheme
	})
	for _, r := range rows {
		g := r.g
		rps := pick(g.runs, func(x run) float64 { return x.RPS })
		var errs, nonok int64
		for _, x := range g.runs {
			errs += x.Errors
			nonok += x.NonOK
		}
		errCell := "0"
		if errs > 0 || nonok > 0 {
			errCell = fmt.Sprintf("**%d/%d**", errs, nonok)
		}
		b.WriteString(fmt.Sprintf("| %d | %s | %d | %.0f | %.1f%% | %s | %s | %s | %s | %d B | %s |\n",
			g.conns, g.scheme, len(g.runs), median(rps), spread(rps),
			us(median(pick(g.runs, func(x run) float64 { return x.P50us }))),
			us(median(pick(g.runs, func(x run) float64 { return x.P90us }))),
			us(median(pick(g.runs, func(x run) float64 { return x.P99us }))),
			us(median(pick(g.runs, func(x run) float64 { return x.P999us }))),
			g.runs[0].BytesPerRe, errCell))
	}
	return b.String()
}

func renderEnv(env run, groups map[string]*group, order []string) string {
	total := 0
	for _, k := range order {
		total += len(groups[k].runs)
	}
	return fmt.Sprintf("%s / %s / %s / GOMAXPROCS=%d，共 %d 轮、%d 组",
		env.GoVersion, env.OS, env.Arch, env.GOMAXPROCS, total, len(order))
}

// us 微秒按量级选单位，避免报告里出现 46085.5us 这种读不出量级的数
// [EN] Microseconds are scaled by magnitude so the report avoids figures like 46085.5us
func us(v float64) string {
	switch {
	case v >= 1000:
		return fmt.Sprintf("%.2fms", v/1000)
	default:
		return fmt.Sprintf("%.0fµs", v)
	}
}

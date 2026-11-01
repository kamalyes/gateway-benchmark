/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:24:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:24:00
 * @FilePath: \gateway-benchmark\gw-zero\main.go
 * @Description: go-zero 侧的被测网关 - 形态 A 直出，形态 B 经 zrpc 调后端
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"

	"github.com/kamalyes/gateway-benchmark/core"
	benchpb "github.com/kamalyes/gateway-benchmark/proto"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/zrpc"
)

type echoReq struct {
	ID   string `json:"id"`
	Seq  int32  `json:"seq"`
	Text string `json:"text"`
}

func main() {
	port := flag.Int("port", 8201, "HTTP 端口")
	backend := flag.String("backend", "", "形态 B 的后端 gRPC 地址，留空则走形态 A 直出")
	flag.Parse()

	// 日志关掉：go-rpc-gateway 侧也关，否则一边在写 stdout 一边不写，比的是 I/O
	// [EN] Logging off on both sides, otherwise one pays for stdout I/O and the other does not
	logx.Disable()

	var cfg rest.RestConf
	conf.FillDefault(&cfg)
	cfg.ServiceConf = service.ServiceConf{Log: logx.LogConf{Mode: "console", Level: "severe"}}
	cfg.Host = "127.0.0.1"
	cfg.Port = *port
	// 关掉 go-zero 自带的自适应熔断与统计上报：这两项 go-rpc-gateway 默认不开，
	// 留着就是让 go-zero 多做一份对方没做的工作
	// [EN] go-zero's adaptive breaker and stat reporting are off: go-rpc-gateway does not
	// enable them by default, so leaving them on charges go-zero for work the other never does
	cfg.Timeout = 0

	// go-zero 的 MiddlewaresConf 全部字段都是 default=true，conf.FillDefault 会把它们
	// 全部打开 —— 实测 11 个中间件（Trace / Log / Prometheus / MaxConns / Breaker /
	// Shedding / Timeout / Recover / Metrics / MaxBytes / Gunzip）都在链上
	// 对照之下 go-rpc-gateway 默认只开 5 个，所以这个对比里 go-zero 是被让的一方，
	// 不是反过来 —— 报告里要如实说明这一点
	//
	// [EN] Every field of go-zero's MiddlewaresConf defaults to true and conf.FillDefault
	// turns them all on, measured as eleven handlers on the chain, against five for
	// go-rpc-gateway; go-zero is the handicapped side in this comparison, not the other
	// way round, and the report says so
	server := rest.MustNewServer(cfg, rest.WithNotFoundHandler(http.NotFoundHandler()))
	defer server.Stop()

	var handler http.HandlerFunc
	if *backend == "" {
		handler = directHandler()
	} else {
		handler = rpcHandler(*backend)
	}

	server.AddRoute(rest.Route{
		Method:  http.MethodPost,
		Path:    "/bench.BenchService/Echo",
		Handler: handler,
	})

	fmt.Printf("gw-zero listening on 127.0.0.1:%d (backend=%q)\n", *port, *backend)
	server.Start()
}

// directHandler 形态 A：rest 直接出业务结果，没有跨进程跳
// [EN] Shape A: rest answers from the handler with no cross-process hop
func directHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req echoReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, core.Echo(req.ID, req.Seq, req.Text))
	}
}

// rpcHandler 形态 B：经 zrpc 跨网络调同一个 gRPC 后端
// [EN] Shape B: calls the same gRPC backend over the network through zrpc
func rpcHandler(target string) http.HandlerFunc {
	client := zrpc.MustNewClient(zrpc.RpcClientConf{
		Endpoints: []string{target},
		NonBlock:  false,
		// 关掉 zrpc 默认的熔断与超时拦截器，与 go-rpc-gateway 侧的裸 grpc 客户端对齐
		// [EN] zrpc's default breaker and timeout interceptors are off, matching the bare
		// grpc client used on the go-rpc-gateway side
		Timeout: 0,
	})
	stub := benchpb.NewBenchServiceClient(client.Conn())

	return func(w http.ResponseWriter, r *http.Request) {
		var req echoReq
		if err := httpx.Parse(r, &req); err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		resp, err := stub.Echo(context.Background(), &benchpb.EchoRequest{
			Id: req.ID, Seq: req.Seq, Text: req.Text,
		})
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		httpx.OkJsonCtx(r.Context(), w, resp)
	}
}

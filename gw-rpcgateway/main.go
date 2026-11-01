/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:28:00
 * @FilePath: \gateway-benchmark\gw-rpcgateway\main.go
 * @Description: go-rpc-gateway 侧的被测网关 - 形态 A 进程内转码，形态 B 代理到后端
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/kamalyes/gateway-benchmark/core"
	benchpb "github.com/kamalyes/gateway-benchmark/proto"
	goconfig "github.com/kamalyes/go-config"
	"github.com/kamalyes/go-logger"
	// 包 gateway 就在模块根，不是 go-rpc-gateway/gateway 子目录
	// docs/QUICKSTART.md 里所有示例写的都是后者，按文档抄会报 does not contain package
	// [EN] Package gateway sits at the module root, not under a gateway/ subdirectory
	// Every example in docs/QUICKSTART.md uses the latter and does not compile
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	gateway "github.com/kamalyes/go-rpc-gateway"
	gwglobal "github.com/kamalyes/go-rpc-gateway/global"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type svc struct {
	benchpb.UnimplementedBenchServiceServer
}

func (s *svc) Echo(_ context.Context, req *benchpb.EchoRequest) (*benchpb.EchoResponse, error) {
	return core.Echo(req.GetId(), req.GetSeq(), req.GetText()), nil
}

func main() {
	backend := flag.String("backend", "", "形态 B 的后端 gRPC 地址，留空则走形态 A 进程内转码")
	flag.Parse()

	gw, err := gateway.NewGateway().
		WithSearchPath("gw-rpcgateway/resources").
		WithEnvironment(goconfig.GetEnvironment()).
		WithPrefix("gateway-bench").
		Silent().
		Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build gateway: %v\n", err)
		os.Exit(1)
	}

	// 把全局日志级别抬到 ERROR，让访问日志那行 INFO 被级别闸门挡掉
	//
	// 配置里写 middleware.logging.enabled: false 不生效 —— 同一段里的 prefix 能读到，
	// 只有这个布尔值回来还是 true，也就是 YAML 里显式的 false 覆盖不了默认的 true
	// 这是 go-config 侧的缺陷，已单独记录，这里先用级别闸门达到同样效果
	//
	// 对照组 go-zero 调的是 logx.Disable()，同样是级别闸门而不是删掉中间件，
	// 两边因此付的是同一种开销
	//
	// [EN] The global log level is raised to ERROR so the access-log INFO line is
	// dropped by the level gate
	// Setting middleware.logging.enabled: false in YAML has no effect: prefix in the
	// same section is read, but this boolean still comes back true, so an explicit
	// false cannot override a true default -- a go-config defect, recorded separately
	// The go-zero side calls logx.Disable(), also a level gate rather than removing
	// the middleware, so both sides pay the same kind of cost
	if gwglobal.LOGGER != nil {
		gwglobal.LOGGER.SetLevel(logger.ERROR)
	}

	impl := &svc{}

	if *backend == "" {
		// 形态 A：gRPC 服务与 HTTP 转码同进程，RegisterGatewayHandler 直接把
		// handler 挂在 mux 上，请求不出进程
		// [EN] Shape A: the gRPC service and HTTP transcoding share one process, and
		// RegisterGatewayHandler mounts the handler straight onto the mux
		gw.RegisterService(func(s *grpc.Server) {
			benchpb.RegisterBenchServiceServer(s, impl)
		})
		if err := gw.RegisterGatewayHandler(func(ctx context.Context, mux *runtime.ServeMux) error {
			return benchpb.RegisterBenchServiceHandlerServer(ctx, mux, impl)
		}); err != nil {
			fmt.Fprintf(os.Stderr, "register gateway handler: %v\n", err)
			os.Exit(1)
		}
	} else {
		// 形态 B：转码后经 gRPC 客户端跨网络调后端，与 go-zero 的 api 到 rpc 对齐
		// [EN] Shape B: after transcoding, a gRPC client crosses the network to the
		// backend, matching go-zero's api-to-rpc layout
		if err := gw.RegisterProxyHandler(
			benchpb.RegisterBenchServiceHandlerFromEndpoint,
			*backend,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		); err != nil {
			fmt.Fprintf(os.Stderr, "register proxy handler: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("gw-rpcgateway ready (backend=%q)\n", *backend)
	if err := gw.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}

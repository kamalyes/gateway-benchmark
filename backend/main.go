/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:19:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:19:00
 * @FilePath: \gateway-benchmark\backend\main.go
 * @Description: 形态 B 用的独立 gRPC 后端 - 两边网关都跨网络调它
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"flag"
	"log"
	"net"

	"github.com/kamalyes/gateway-benchmark/core"
	benchpb "github.com/kamalyes/gateway-benchmark/proto"
	"google.golang.org/grpc"
)

type server struct {
	benchpb.UnimplementedBenchServiceServer
}

func (s *server) Echo(_ context.Context, req *benchpb.EchoRequest) (*benchpb.EchoResponse, error) {
	return core.Echo(req.GetId(), req.GetSeq(), req.GetText()), nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9100", "gRPC 监听地址")
	flag.Parse()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}

	// 不挂任何拦截器：形态 B 要测的是网关到后端这一跳，后端本身越薄越好
	// [EN] No interceptors: shape B measures the gateway-to-backend hop, so the
	// backend is kept as thin as possible
	s := grpc.NewServer()
	benchpb.RegisterBenchServiceServer(s, &server{})

	log.Printf("bench backend listening on %s", *addr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

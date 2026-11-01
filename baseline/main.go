/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:40:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:40:00
 * @FilePath: \gateway-benchmark\baseline\main.go
 * @Description: 两条基线 - 用来把「转码成本」和「中间件链成本」拆开
 *
 * 只比两个框架只能得到一个比值，说不清这个差距里哪部分是架构固有的、
 * 哪部分是各自默认中间件带来的。两条零中间件基线给出参照：
 *   json   net/http + encoding/json，Go HTTP 服务的地板
 *   gwmux  grpc-gateway 的 ServeMux 裸跑，隔离出 protojson 转码的代价
 *
 * [EN] Two zero-middleware baselines that separate transcoding cost from
 * middleware-chain cost: comparing only the two frameworks yields a ratio but
 * cannot say how much of the gap is inherent to the architecture
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/kamalyes/gateway-benchmark/core"
	benchpb "github.com/kamalyes/gateway-benchmark/proto"
)

type svc struct {
	benchpb.UnimplementedBenchServiceServer
}

func (s *svc) Echo(_ context.Context, req *benchpb.EchoRequest) (*benchpb.EchoResponse, error) {
	return core.Echo(req.GetId(), req.GetSeq(), req.GetText()), nil
}

type echoReq struct {
	ID   string `json:"id"`
	Seq  int32  `json:"seq"`
	Text string `json:"text"`
}

func main() {
	mode := flag.String("mode", "json", "json 或 gwmux")
	addr := flag.String("addr", "127.0.0.1:8401", "监听地址")
	flag.Parse()

	var h http.Handler
	switch *mode {
	case "json":
		// 地板：标准库 + encoding/json，没有任何中间件
		// [EN] The floor: standard library plus encoding/json, no middleware at all
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req echoReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(core.Echo(req.ID, req.Seq, req.Text))
		})
	case "gwmux":
		// 裸 grpc-gateway：与 go-rpc-gateway 形态 A 走的是同一条转码路径，
		// 但不挂任何中间件，两者之差就是 go-rpc-gateway 中间件链的代价
		// [EN] Bare grpc-gateway: the same transcoding path as go-rpc-gateway's shape A
		// with no middleware, so the difference is the cost of its middleware chain
		mux := runtime.NewServeMux()
		if err := benchpb.RegisterBenchServiceHandlerServer(context.Background(), mux, &svc{}); err != nil {
			panic(err)
		}
		h = mux
	default:
		panic("mode 只能是 json 或 gwmux")
	}

	fmt.Printf("baseline %s listening on %s\n", *mode, *addr)
	if err := http.ListenAndServe(*addr, h); err != nil {
		panic(err)
	}
}

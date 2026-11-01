/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-11-01 03:18:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-11-01 03:18:00
 * @FilePath: \gateway-benchmark\core\handler.go
 * @Description: 两边网关共用的业务逻辑 - 跑分对比的是网关开销，不是业务实现
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package core

import (
	"strconv"

	benchpb "github.com/kamalyes/gateway-benchmark/proto"
)

// ItemCount 响应里的 item 条数
// 固定值而不是随请求变化：两边必须做同样多的工作，响应体大小也要一致
//
// [EN] Number of items in the response
// Fixed rather than request-driven so both sides do the same amount of work
// and produce the same response size
const ItemCount = 8

// Echo 两边网关最终都调到这里
// 刻意不碰数据库、缓存、时间、随机数：这些都会把噪声引进测量
// 做的事情是「拼一个有结构的响应」，代表一次最轻的真实业务调用
//
// [EN] Both gateways end up here
// Deliberately touches no database, cache, clock or randomness, all of which
// would inject noise into the measurement
// What it does is assemble a structured response, standing in for the lightest
// realistic business call
func Echo(id string, seq int32, text string) *benchpb.EchoResponse {
	items := make([]*benchpb.Item, 0, ItemCount)
	for i := 0; i < ItemCount; i++ {
		items = append(items, &benchpb.Item{
			Key:   int64(i),
			Value: "item-" + strconv.Itoa(i),
		})
	}
	return &benchpb.EchoResponse{
		Id:    id,
		Seq:   seq,
		Text:  text,
		Items: items,
	}
}

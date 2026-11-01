#!/usr/bin/env bash
# 压测编排：一次只起一个被测对象
#
# 为什么必须串行：早先一版把四个服务（两个网关加两条基线）同时起着，
# 轮流压 其中任一个 结果是同一档 64 并发下 go-rpc-gateway 79612 对 83054，
# 差 4.3%，而且让「完整网关快过零中间件裸 mux」这种不可能的结论看起来成立
# 空闲进程不是零成本：各自有后台 ticker、GC、以及抢同一批核
#
# 协议：起一个 -> 预热 -> 跑 N 轮 -> 停掉 -> 下一个
#
# [EN] One subject at a time
# An earlier version kept all four servers up and took turns driving them, which cost
# 4.3% at 64 connections and made an impossible conclusion look true, namely a full
# gateway outrunning a zero-middleware bare mux
# Idle processes are not free: each has background tickers, GC, and competes for cores
set -euo pipefail

cd "$(dirname "$0")/.."

ROUNDS="${ROUNDS:-3}"
DURATION="${DURATION:-8s}"
WARMUP="${WARMUP:-3s}"
CONNS="${CONNS:-8 32 64}"
BACKEND_ADDR=127.0.0.1:9100
LOGDIR="${LOGDIR:-/tmp/gateway-benchmark-logs}"

mkdir -p results bin "$LOGDIR"
rm -f results/*.json

for t in backend gw-zero gw-rpcgateway baseline driver; do
  go build -o "bin/$t" "./$t"
done
go build -o bin/report ./bootstrap/report

# stop_all 清掉全部被测进程，每一组开始前后都调一次
stop_all() {
  pkill -f "$PWD/bin/gw-zero" 2>/dev/null || true
  pkill -f "$PWD/bin/gw-rpcgateway" 2>/dev/null || true
  pkill -f "$PWD/bin/backend" 2>/dev/null || true
  pkill -f "$PWD/bin/baseline" 2>/dev/null || true
  sleep 1
}
trap stop_all EXIT

# wait_ready 等端口能答 200，而不是靠 sleep 猜
wait_ready() {
  local port="$1"
  # 循环变量用不上，只是计次；shellcheck 的 SC2034 在这种场合是误报，用 _ 明示
  # [EN] The loop variable is only a counter, so _ makes that explicit for shellcheck
  for _ in $(seq 1 60); do
    if [ "$(curl -s -m 2 -o /dev/null -w '%{http_code}' -X POST \
          -H 'Content-Type: application/json' \
          -d '{"id":"x","seq":1,"text":"hi"}' \
          "http://127.0.0.1:$port/bench.BenchService/Echo" 2>/dev/null)" = "200" ]; then
      return 0
    fi
    sleep 0.5
  done
  echo "端口 $port 在 30 秒内没有就绪" >&2
  return 1
}

# measure <shape> <scheme> <port> <tag>
measure() {
  local shape="$1" scheme="$2" port="$3" tag="$4" c r
  for c in $CONNS; do
    for r in $(seq 1 "$ROUNDS"); do
      ./bin/driver -shape "$shape" -scheme "$scheme" \
        -name "$shape/$scheme/c$c/r$r" \
        -url "http://127.0.0.1:$port/bench.BenchService/Echo" \
        -conns "$c" -duration "$DURATION" -warmup "$WARMUP" \
        -out "results/$shape-$tag-c$c-r$r.json"
    done
  done
}

echo "=== 形态 B：带一跳 RPC ==="
for subject in zero rpcgw; do
  stop_all
  ./bin/backend -addr "$BACKEND_ADDR" > "$LOGDIR/backend.log" 2>&1 &
  sleep 1
  if [ "$subject" = zero ]; then
    ./bin/gw-zero -port 8201 -backend "$BACKEND_ADDR" > "$LOGDIR/zero-b.log" 2>&1 &
    wait_ready 8201 && measure B go-zero 8201 zero
  else
    ./bin/gw-rpcgateway -backend "$BACKEND_ADDR" > "$LOGDIR/rpcgw-b.log" 2>&1 &
    wait_ready 8301 && measure B go-rpc-gateway 8301 rpcgw
  fi
done

echo "=== 形态 A：单进程（归因用） ==="
stop_all
./bin/gw-zero -port 8201 > "$LOGDIR/zero-a.log" 2>&1 &
wait_ready 8201 && measure A go-zero 8201 zero
stop_all
./bin/gw-rpcgateway > "$LOGDIR/rpcgw-a.log" 2>&1 &
wait_ready 8301 && measure A go-rpc-gateway 8301 rpcgw

echo "=== 两条零中间件基线 ==="
stop_all
./bin/baseline -mode json -addr 127.0.0.1:8401 > "$LOGDIR/base-json.log" 2>&1 &
wait_ready 8401 && measure baseline net-http-json 8401 json
stop_all
./bin/baseline -mode gwmux -addr 127.0.0.1:8402 > "$LOGDIR/base-gwmux.log" 2>&1 &
wait_ready 8402 && measure baseline bare-grpc-gateway 8402 gwmux

stop_all
echo "=== 生成报告 ==="
./bin/report

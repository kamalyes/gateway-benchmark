# gateway-benchmark

go-rpc-gateway 与 go-zero 的网关层压测台

**测量结果与结论看 [REPORT.md](REPORT.md)**,本文只讲这个台子怎么构成的

第一次看这份数据,先读 REPORT.md 的第 2 节「表里的每个量是什么」:并发、RPS、
p50/p90/p99、极差、响应体各是什么,单位是什么,以及为什么报中位数而不是平均数
—— 不先弄清这些,表里的数字很容易被读反

## 为什么要自己搭

直接拿两个框架的 hello world 对打会得到一个数字,但说不清那个数字的含义 这个台子刻意做了三件事:

1. **业务逻辑完全共用** 两边最终都调 `core/handler.go` 的同一个 `Echo` 函数,不碰数据库、缓存、时钟、随机数 比的是网关层,不是谁的业务代码写得快
2. **分两种形态** go-rpc-gateway 原生是单进程转码,go-zero 原生是 api 加 rpc 两进程 只测一种会把「一次网络跳」算到一边账上
3. **带两条零中间件基线** 只有两个框架互比得不到分解 加上 `net/http + encoding/json` 的地板和裸 grpc-gateway mux,才能把「转码代价」和「中间件链代价」拆开

## 目录

| 路径 | 作用 |
|---|---|
| `proto/bench.proto` | 两边共用的契约 用 `generate_unbound_methods=true` 生成 HTTP 绑定,因此不需要 googleapis 的 `google.api.http` 注解 |
| `core/handler.go` | 两边共用的业务逻辑 |
| `backend/` | 形态 B 的独立 gRPC 后端,不挂任何拦截器 |
| `gw-zero/` | go-zero 侧被测网关 `-backend` 留空走形态 A,填地址走形态 B |
| `gw-rpcgateway/` | go-rpc-gateway 侧被测网关 同一个开关 |
| `baseline/` | 两条零中间件基线 `-mode json` 与 `-mode gwmux` |
| `driver/` | 压测驱动 |
| `results/` | 每轮的原始 JSON |

## 为什么压测驱动是自己写的

`wrk` / `hey` / `vegeta` 都要装外部二进制,而且驱动自身的行为会直接改变结论:

- **连接复用** 响应体不读完就关,连接不回池,下一次是新建连接,测出来变成建连开销 驱动里 `io.Copy(io.Discard, resp.Body)` 是必须的
- **驱动自己的瓶颈** 所有 worker 共用一个 `http.Transport` 时,它内部的连接池和锁会先饱和,测出来的是驱动而不是被测对象 所以每 worker 一个 Transport
- **计时点** 从发请求前到读完响应体,而不是到收到响应头
- **预热** 前几秒的样本不计入,否则连接建立和 JIT 式的首次路径解析会污染分位数

写进仓库才能让别人复现出同一个数

## 生成 proto 产物

```bash
protoc -I. \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative,generate_unbound_methods=true \
  proto/bench.proto
```

需要 `protoc-gen-go`、`protoc-gen-go-grpc`、`protoc-gen-grpc-gateway` 三个插件在 `PATH` 里

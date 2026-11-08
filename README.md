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
| `stressdriver/` | 第二个压测驱动，直接调 [go-stress](https://github.com/kamalyes/go-stress) 库（`go.mod` 里 `replace` 到 `../go-stress` 工作树，理由见下）输出同一份结果 JSON，用来与 `driver/` 互相印证 |
| `results/` | 每轮的原始 JSON |

## 为什么压测驱动是自己写的

`wrk` / `hey` / `vegeta` 都要装外部二进制,而且驱动自身的行为会直接改变结论:

- **连接复用** 响应体不读完就关,连接不回池,下一次是新建连接,测出来变成建连开销 驱动里 `io.Copy(io.Discard, resp.Body)` 是必须的
- **驱动自己的瓶颈** 所有 worker 共用一个 `http.Transport` 时,它内部的连接池和锁会先饱和,测出来的是驱动而不是被测对象 所以每 worker 一个 Transport
- **计时点** 从发请求前到读完响应体,而不是到收到响应头
- **预热** 前几秒的样本不计入,否则连接建立和 JIT 式的首次路径解析会污染分位数

写进仓库才能让别人复现出同一个数

## 为什么有两个驱动

上面那些理由说明的是「驱动必须可控」,不是「一个驱动就够了」。压测里最难自证的一件事是:

> 这个数字是被测对象的,还是驱动自己的?

单驱动回答不了。连接复用策略、worker 模型、计时点都会直接改变结论,而这些错误不会报错,
只会让数字偏向某一侧 —— 一个把连接数打满的驱动会让所有被测对象看起来一样慢,
一个不读完响应体的驱动会让所有被测对象看起来一样快。

所以这里放了两个独立实现:`driver/` 是本仓自己写的,`stressdriver/` 用 go-stress 当引擎
(它有自己的 worker 池、客户端池、延迟直方图,与本仓唯一共享的是「往同一个 URL 发同样的 POST」
这件事本身)。两者跑同一档,数字一致才说明测的是网关;不一致就定位到驱动,
这比任何一个驱动的注释都有说服力。

```bash
# 自建驱动(默认)
make bench

# go-stress 引擎,结果与报告各自落一份 —— 不能与上面共用 results/
DRIVER=stressdriver RESULTS_DIR=results-gostress REPORT_OUT=REPORT-gostress.md ./scripts/bench.sh
```

`RESULTS_DIR` 必须跟着换:报告按 `(形态, 方案, 并发)` 分组取中位数,
两个驱动的结果混在同一个目录里会被悄悄平均成一行 —— 那比没测更糟。

口径上已知的三处差异,读数时必须记住:

| | `driver/` | `stressdriver/` (go-stress v1.0.1) |
|---|---|---|
| 预热 | 跑着但不记样本,测量期一条新连接都不建 | 独立的一轮短压测,测量轮重新建连,前 `并发数` 个请求含 TCP 握手 |
| 分位数 | p50/p90/p99/p99.9 | p50/p90/p95/p99,没有 p99.9(结果 JSON 里 `p999_us` 写 0,另给 `p95_us`) |
| 客户端 | 每 worker 一个 `http.Transport` | go-stress 自己的 `ClientPool` |

第三行正是两者该互相印证的地方:如果高并发档 go-stress 明显更低,那是驱动侧的瓶颈,
不是网关变慢了。

### 第一次交叉验证就抓到了一个:go-stress v1.0.1 在这台机器上是驱动受限的

两个驱动压**同一个零中间件 net/http 基线**(`baseline -mode json`,8 并发,6s):

| 驱动 | RPS | p50 | p99 | 错误 |
|---|---:|---:|---:|---:|
| `driver/` | **95436** | 76 µs | 237 µs | 0 |
| `stressdriver/` (go-stress v1.0.1) | **11580** | 78 µs | 955 µs | 1 次 `context deadline exceeded` |

**p50 两边一致,只有吞吐差 8 倍** —— 它测的延迟是对的,但它从来没把被测对象压起来。
go-stress 自己的逐秒输出指出了原因:

```
1s  QPS 18546  │ 内存已存储 10000 条记录
2s  QPS 16951  │              20000
4s  QPS 13330  │              40000
6s  QPS 11583  │              60000      ← 吞吐单调衰减,与存储条数同步
```

每个请求都往内存存一条明细(`storage=memory`,文档写的是「高速、无限制」),
条数涨上去吞吐就掉下来,而延迟一直是平的 —— 衰减发生在请求之外。
8 并发下 p50 只有 78 µs 却只跑出 11580 RPS,等于每个 worker 每次迭代 690 µs 花在请求之外。

**结论:在这台机器上,go-stress 的数字不能用来比较网关版本** —— 被测对象能跑 95k 而驱动只发得出
11k,量到的是驱动。它量出来的 `local vs base` 差异(±1~4%)全是驱动受限下的噪声。

这不是说这个驱动没用:

- 它作为交叉验证**已经起作用了**,第一次跑就把一个会让人读错结论的问题顶了出来 ——
  如果只有 go-stress 一个驱动,这里会得出「网关只能跑 1 万 RPS」
- 低吞吐目标(每秒几千)、或者 go-stress 把明细存储改成有上限的环形缓冲之后,它仍然是有效的第二只眼
- 延迟口径是可信的:两边 p50 一致

顺带说清一件事:高并发档 go-stress 的 p50 反而**更低**(64 并发 208 µs 对自建驱动的 431 µs),
读起来像「go-stress 测出网关更快」,其实是它没把服务器压满,量到的是空载延迟。
分位数低而吞吐低,是驱动受限的典型形状。

## 生成 proto 产物

```bash
protoc -I. \
  --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=. --grpc-gateway_opt=paths=source_relative,generate_unbound_methods=true \
  proto/bench.proto
```

需要 `protoc-gen-go`、`protoc-gen-go-grpc`、`protoc-gen-grpc-gateway` 三个插件在 `PATH` 里

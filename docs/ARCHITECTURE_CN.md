# 架构

六层，每层一个包或一对文件，每层只干一件事。英文版：[ARCHITECTURE.md](ARCHITECTURE.md)。

```
                    ┌─────────────────────────────┐
   你的代码  ───►    │  6. 客户端                   │  类型化方法、
                    │     读循环协程               │  请求匹配
                    └──────────────┬──────────────┘
                                   │ Transport
                    ┌──────────────▼──────────────┐
                    │  2. 传输                    │  stdio / HTTP
                    └──────────────┬──────────────┘
                                   │ []byte
                    ┌──────────────▼──────────────┐
                    │  1. JSON-RPC 2.0            │  帧、id、错误码
                    └──────────────┬──────────────┘
                                   │
                    ┌──────────────▼──────────────┐
                    │  3. 握手                    │  initialize、能力
                    │  4. 工具                    │  list、call、schema
                    │  5. 资源与提示词             │  list、read、get
                    └─────────────────────────────┘
                                  服务端
```

依赖方向是单向的：上层用下层，反之不行。`Transport` 接口是客户端和服务端
唯一共享的东西，这就是集成测试能让两边跑在一根内存管道上的原因。

## 第 1 层：JSON-RPC 2.0

`go/protocol/`、`cpp/src/jsonrpc.cpp`。

管帧、id、错误码。唯一不显然的决定：Go 里 `id` 是 `json.RawMessage`，
C++ 里存字面文本，所以它能逐字节 round-trip。这一层其余部分都是机械劳动。

## 第 2 层：传输

`go/transport/`、`cpp/src/transport_stdio.cpp`、`cpp/src/transport_http.cpp`。

两个方法：读一条消息，写一条消息。stdio 是换行分隔的 JSON，HTTP 是 POST 后读。
有意思的是这里**没有**的东西：没有缓冲决策、没有重试、没有超出传输本身所需的连接管理。

用 `bufio.Reader.ReadBytes` 而不是 `bufio.Scanner`，因为后者有 64 KiB 上限。

## 第 3 层：握手

`go/server/server.go`、`handleInitialize`。

`initialize` 置一个标志，其余方法都检查它。检查放在 `dispatch` 顶部一处，
而不是每个 handler 一处，这样新加的方法不可能忘记检查。

## 第 4 层：工具

`go/server/handlers.go`、`handleToolCall`。

注册表把名字映射到 handler。有意思的代码是错误分叉：调用畸形走协议错误，
工具跑了但失败走 `isError` 结果。schema 校验也在这层。

## 第 5 层：资源与提示词

`go/server/handlers.go`、`handleResourceRead`、`handlePromptGet`。

直白的查表。唯一的细微之处是错误码：未知 URI 是 `-32002`，不是 `-32602`。

## 第 6 层：客户端

`go/client/`、`cpp/src/client.cpp`。

一张按 id 索引的 pending 请求表，加一个填它的读循环协程。每个方法都是
`call()` 的薄封装。客户端不重试、不池化、不断线重连：那些是生产关注点，
而这是教学实现。

## 为什么写两遍

不是为了覆盖率。重点是同一个协议在两套不同约束下各实现一遍，
能看出哪些是本质、哪些是偶然：

- Go 从 `net/http` 免费拿到 HTTP；C++ 得自己开 socket、手写响应解析，
  这让协议的 HTTP 那一半变得可见。
- Go 有 goroutine；C++ 得自己管 `std::thread` 和条件变量，
  这让读循环的设计变得可见。
- Go 有 `encoding/json`；C++ 得写解析器，这让数字保真问题变得可见。

C++ 版不是加了注释的移植，它是第二遍讲解。

## 测试策略

| 层级        | 位置                                                   | 证明什么              |
| --------- | ---------------------------------------------------- | ----------------- |
| 单元        | `go/protocol`、`go/server/schema_test.go`、`cpp/tests` | 单层孤立正确            |
| 集成        | `go/server/integration_test.go`、`cpp/tests`          | 各层组合正确            |
| 跨语言       | `cpp/examples/http_client_main.cpp`                  | C++ 客户端能连上 Go 服务端 |
| sanitizer | CI（ASan、UBSan、TSan）                                  | 无内存错误、无数据竞争       |
| 基准        | `go/cmd/mcpbench`                                    | 成本真实且被测量          |

其中两行带着附加说明。TSan 不是每台机器都能跑：在 WSL2 上它会在进 main
之前以 `FATAL: ThreadSanitizer: unexpected memory mapping` 退出，所以那一栏
是 CI 的声明，你在那儿本地复现不出来。ASan 和 UBSan 本地能跑，干净。
跨语言那一行是补的——因为 C++ 客户端曾经对着 Go 服务端死锁过一次
（见 PITFALLS），而这种 bug 是任何进程内测试都抓不到的。

集成测试是最值得写的那个。它让真实的客户端和真实的服务端跑在真实的传输上，
没有任何 mock——这是唯一能从客户端那一侧断言错误分类、而不是想当然的办法。

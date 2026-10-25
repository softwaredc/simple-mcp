# simple-mcp

零依赖的 MCP（Model Context Protocol）实现，Go 和 C++ 各写一遍，每一层都有注释。

[English](README.md) | 中文

[![CI](https://github.com/softwaredc/simple-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/softwaredc/simple-mcp/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.21+-00ADD8.svg)](go/go.mod)
[![C++](https://img.shields.io/badge/C++-17-00599C.svg)](cpp/CMakeLists.txt)
[![Dependencies](https://img.shields.io/badge/dependencies-none-brightgreen.svg)](go/go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

---

MCP 是让语言模型调用工具、读取资源、使用提示词模板的协议。大多数人通过 SDK 用它，
从没读过线上的报文。这个仓库就是那个报文格式的完整实现——两种语言，小到一个下午能读完。

**它不是生产级 SDK。** 它是你用来理解手上那个 SDK 的东西，也是你不能引依赖时需要抄的东西。

## 数字

以下数据在 [docs/BENCHMARKS_CN.md](docs/BENCHMARKS_CN.md) 所述的参考机上实测，
可用 `make bench` 复现。

|                             |                             |
| --------------------------- | ---------------------------:|
| Go 实现                       | 2,692 行                     |
| Go 测试                       | 911 行，32 个用例，`-race` 干净     |
| C++ 实现                      | 2,334 行（源码 1,721 + 头文件 613） |
| C++ 测试                      | 70 项断言，ASan/UBSan 干净        |
| stdio 上一次 `tools/call` 往返延迟 | **p50 10.7 µs**             |
| 其中 JSON 编码 + 解码             | 1.5 µs                      |
| stdio 吞吐                    | 约 67,000 msg/s              |
| `tools/list` 响应大小           | 352 字节                      |

## 编译

```bash
# Go
cd go && go build ./... && go test ./...

# C++
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release
cmake --build cpp/build -j
cd cpp/build && ctest --output-on-failure
```

零依赖。Go 只用标准库，C++ 只用标准库和 POSIX socket。

## 跑演示

```bash
cd go
go build -o bin/mcpserver ./cmd/mcpserver
go build -o bin/mcpclient ./cmd/mcpclient
./bin/mcpclient -server ./bin/mcpserver
```

客户端把服务端拉成子进程，通过它的 stdin/stdout 说 JSON-RPC——Claude Desktop 和
大多数 CLI agent 就是这么做的。输出：

```
connected to simple-mcp-demo 1.0.0 (protocol 2025-06-18)
instructions: Demo server. Use echo to repeat text, add to sum two integers, and get_weather for a fixed mock forecast.

3 tools:
  echo           Repeat the given text back verbatim. Use to verify connectivity.
  add            Add two integers and return the sum. Use for arithmetic, not for string concatenation.
  get_weather    Return a mock forecast for a city. The data is fixed and synthetic.

calling tools:
  echo           hello from the client
  add            42
  get_weather    Beijing: 22 C (mock, clear)

error taxonomy:
  wrong argument type -> protocol error: rpc error -32602: invalid arguments: city: expected string, got number
```

C++ 版同样有：

```bash
cmake --build cpp/build -j
./cpp/build/mcp_client ./cpp/build/mcp_server
```

## 接到你自己的客户端

服务端说 stdio，所以任何能拉起子进程的客户端都能用：

```json
{
  "mcpServers": {
    "simple-mcp": {
      "command": "/path/to/bin/mcpserver"
    }
  }
}
```

或者走 HTTP：`./bin/mcpserver -http :8080`，然后连 `http://localhost:8080/mcp`。

## 六层架构

| 层               | 职责                                                             | Go                      | C++                                                |
| --------------- | -------------------------------------------------------------- | ----------------------- | -------------------------------------------------- |
| 1. JSON-RPC 2.0 | 请求/响应/通知帧、错误码                                                  | `go/protocol/`          | `cpp/src/jsonrpc.cpp`                              |
| 2. 传输           | stdio 换行分隔 JSON；HTTP 的 POST + SSE                              | `go/transport/`         | `cpp/src/transport_stdio.cpp`、`transport_http.cpp` |
| 3. 握手           | `initialize`、能力协商、`notifications/initialized`                  | `go/server/server.go`   | `cpp/src/server.cpp`                               |
| 4. 工具           | `tools/list`、`tools/call`、JSON Schema 校验                       | `go/server/handlers.go` | `cpp/src/server.cpp`                               |
| 5. 资源与提示词       | `resources/list`、`resources/read`、`prompts/list`、`prompts/get` | `go/server/handlers.go` | `cpp/src/server.cpp`                               |
| 6. 客户端 SDK      | 请求响应匹配、读循环协程、类型化方法                                             | `go/client/`            | `cpp/src/client.cpp`                               |

阅读顺序：先 `go/protocol/jsonrpc.go`，再 `go/server/server.go`，最后
`go/client/client.go`。C++ 版逐个文件对应。

## 最值得看懂的一件事

MCP 有两种失败，而且不能互相替代：

```
调用本身有问题     ->  JSON-RPC 错误        （-32602、-32601、-32002 …）
工具跑了但失败了   ->  isError 为 true 的结果（content 里说明原因）
```

客户端收到 JSON-RPC 错误，会认为这次调用没有发生；收到 `isError`，它知道工具执行过了。
如果一个「找不到这个城市」的工具返回协议错误，一个守规矩的 agent 会重试三次、放弃，
然后告诉用户「工具坏了」——而工具其实完全正常地工作完了。

这个判断在 `go/server/handlers.go` 里做出，`go/server/integration_test.go`
从客户端两侧断言它的两个方向。C++ 测试做同样的事。

## 这些代码是为了避开这些坑

每一条都有测试守着，改坏了会红。

1. **`bufio.Scanner` 把单条消息限制在 64 KiB。** 返回源码文件或 base64 图片的工具
   轻易超过，而且报错信息里压根不提 MCP。`go/transport/transport_test.go` 既证明
   300 KiB 能完整通过，也证明默认的 `Scanner` 会拒绝它。
2. **stdout 归协议所有。** 一行日志就能让所有解析这个流的客户端崩。诊断信息走 stderr。
3. **通知不该有响应。** 回复 `notifications/initialized` 会让客户端在等下一个
   真正响应时读到一个响应。
4. **id 必须逐字节 round-trip。** 把 `id` 解进 `interface{}`，`1` 会变成 float64 的 `1`，
   按 id 匹配响应的客户端就会永久挂住。
5. **客户端必须有一个读循环。** 「先写后读」在通知插到响应前面那天之前都能工作。
6. **资源不存在是 `-32002`，不是 `-32602`。** 一个表示「请求格式错」，一个表示
   「东西不存在」，恢复动作完全不同。
7. **JSON Schema 校验是服务端的活。** MCP 没规定谁校验工具参数，你不做就没人做。

完整写法见 [docs/PITFALLS_CN.md](docs/PITFALLS_CN.md)。

## 文档

- [架构](docs/ARCHITECTURE.md) · [架构（中文）](docs/ARCHITECTURE_CN.md)
- [协议笔记](docs/PROTOCOL.md) · [协议笔记（中文）](docs/PROTOCOL_CN.md)
- [踩坑记录](docs/PITFALLS.md) · [踩坑记录（中文）](docs/PITFALLS_CN.md)
- [基准测试](docs/BENCHMARKS.md) · [基准测试（中文）](docs/BENCHMARKS_CN.md)

## 局限性

提前说清楚，因为一个藏着边界的教学实现会教错东西。

- **Linux 和 macOS。** C++ 的 HTTP 传输用 POSIX socket。
- **不是生产 SDK。** 没有重试、连接池、分页、取消、进度通知、采样。
- **Streamable HTTP 是简化版。** 只实现单响应形态，服务端挂住 POST 持续推流没做。
  详见 [docs/PROTOCOL.md](docs/PROTOCOL.md)。
- **C++ 版没有 HTTP 服务端。** 手写一个会多出几百行，而复述 Go 版已经讲过的课。
- **`get_weather` 返回固定 mock 数据。** 调真实 API 的演示需要 key、会断网、
  还会把限流变成教学问题。
- **协议版本默认 2025-06-18**，握手时接受更早的版本，但不实现已被移除的字段。

## 目录

```
go/
  protocol/    JSON-RPC 2.0：帧、id、错误码
  transport/   stdio 与 Streamable HTTP
  mcptypes/    线上类型，含 Content 这个 tagged union
  server/      注册表、握手、分发、schema 校验
  client/      类型化方法、读循环协程、pending 请求表
  cmd/         mcpserver、mcpclient、mcpbench
cpp/
  include/mcp/ 头文件，一层一个
  src/         实现，一层一个
  tests/       70 项断言
  examples/    server_main、client_main、http_client_main
docs/          架构、协议、踩坑、基准测试
```

`http_client_main` 只做一件事情，而且是任何单元测试都做不到的事：把 C++ 客户端
指向正在运行的 Go 服务端，用真实 socket 对话。[PITFALLS](docs/PITFALLS_CN.md)
里那条「从另一个线程关 socket 不会唤醒读线程」就是这么被发现的——当时两个实现的
测试全绿，而客户端挂在退出那一步。跑起来：

```bash
cd go && go build -o mcpserver ./cmd/mcpserver && ./mcpserver -http 127.0.0.1:8080 &
cmake -S cpp -B cpp/build -DCMAKE_BUILD_TYPE=Release && cmake --build cpp/build -j
./cpp/build/mcp_http_client 127.0.0.1 8080
```

## 贡献

这个仓库最有价值的贡献是**纠错**。如果某条注释是错的，或者某个协议细节和规范不一致，
请提 issue 并指出规范章节。见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可

Apache-2.0，见 [LICENSE](LICENSE)。

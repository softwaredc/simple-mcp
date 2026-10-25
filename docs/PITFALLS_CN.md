# 踩坑记录

七个会让 MCP 实现出错的地方，每条都有症状、原因和一个会因为回归而变红的测试。
本文提到的每个测试都真实存在。英文版：[PITFALLS.md](PITFALLS.md)。

---

## 1. `bufio.Scanner` 把你的消息悄悄限制在 64 KiB

**症状。** 一个返回源码文件、JSON 转储或 base64 图片的工具，测试时好好的，
上线就报：

```
bufio.Scanner: token too long
```

这句话里压根没提 MCP，于是大家会跑去错误的地方排查几个小时。

**原因。** `bufio.Scanner` 默认单 token 上限 64 KiB。stdio 传输是按行分隔的，
超过这个长度的「消息」在 scanner 眼里就不是消息。

**修法。** 用 `bufio.Reader.ReadBytes('\n')`，它会按消息实际大小增长；想要上限就自己加。
本实现在 16 MiB 处设限——远高于任何真实 MCP 消息，同时把单连接内存圈住了。

```go
// go/transport/stdio.go
line, err := t.r.ReadBytes('\n')
```

**测试。** `TestLargeMessageSurvivesStdio` 把 300 KiB 推过传输层，断言逐字节一致。
`TestScannerWouldHaveFailedHere` 顺手证明默认 `Scanner` 会拒绝同样的输入——
让这个坑留在文档里，而不是变成口口相传的传说。

C++ 侧天然有这个性质：`std::getline` 没有长度上限。值得知道的是，
这正是 C++ 传输层不需要特殊处理的原因。

---

## 2. stdout 归协议所有

**症状。** 自己手跑服务端没问题，客户端一拉起就失败，第一条消息就报解析错误。

**原因。** stdio 模式下服务端的 stdout **就是**协议通道。`fmt.Println("listening")`
打一行到 stdout，客户端第一次读到的就是
`listening\n{"jsonrpc":"2.0",...}`，这不是 JSON。

**修法。** 诊断信息一律走 stderr。

```go
log.SetPrefix("mcpserver: ")   // 默认就写 stderr
```

**测试。** 没有子进程不好直接测，所以 `cmd/mcpserver` 显式设置了 logger 输出，
演示客户端也把子进程的 stderr 接到自己的 stderr。在这个仓库里给服务端加日志，
review 时该问的问题是：「这行进哪个流」。

---

## 3. 通知不该有响应

**症状。** 客户端握手之后挂住，或者在等某个响应时读到了另一个响应，从此永远错位一位。

**原因。** 没有 `id` 的 JSON-RPC 请求是通知，通知不能回。`notifications/initialized`
就是通知。回复它，等于往一个「客户端按 id 匹配响应」的流里塞了一条没人要的消息。

**修法。** 写回之前先检查 `IsNotification()`；对本来就没话可讲的方法，分发路径直接返回 nil。

```go
if req.IsNotification() || out == nil {
    return nil, nil
}
```

**测试。** `TestNotificationGetsNoResponse` 发 `notifications/initialized`，
断言服务端产出零字节。

---

## 4. `id` 必须逐字节 round-trip

**症状。** 客户端永远等一个早就到了的响应。或者小 id 正常、大 id 崩。

**原因。** 把 `id` 解进 `interface{}` 拿到的是 `float64`。重新编码时，
`1` 可能变成 `1e+00`，超过 2^53 的整数直接丢精度。客户端存的是
`"9007199254740993"`，却在找 `"9007199254740992"`，永远找不到。

**修法。** id 保持原始字节：Go 用 `json.RawMessage`，C++ 存字面文本。原样吐回去。

```go
type Request struct {
    ID json.RawMessage `json:"id,omitempty"`
}
```

**测试。** Go protocol 包的 `TestIDRoundTrip` 喂五种 id 形态，
包括一个超出 float64 精确范围的，断言每个都在响应里原样出现。
C++ 对应 `test_number_literal_preserved`。

---

## 5. 客户端必须有一个读循环协程

**症状。** 对你自己写的服务端好好的，对真实服务端挂住。或者在服务端
某天主动发了点什么之后挂住。

**原因。** 「先写请求再读响应」假设服务端从不先开口。但它会：进度通知、
日志消息、列表变更事件。第一次有一条插到响应前面，客户端就在等响应时
读到了通知，从此永久错位。

**修法。** 一个读循环协程，读所有东西，按「带不带 id」分流。
响应交给等待者；通知分发或丢弃。

```go
// go/client/client.go
func (c *Client) readLoop() {
    for {
        raw, err := c.t.ReadMessage()
        ...
        if len(resp.ID) == 0 {
            continue // 通知
        }
        ch, ok := c.pending[string(resp.ID)]
        if ok { ch <- resp }
    }
}
```

**测试。** 集成测试走的就是真实路径：客户端和服务端隔着一根管道，
读循环协程从第一个请求之前就开始跑。`Connect()` 里有注释解释为什么必须先启动它——
用同步管道时，服务端的响应会阻塞到有人读它为止。

---

## 6. 资源不存在是 `-32002`，不是 `-32602`

**症状。** 客户端对一个不存在的 URI 重试 `resources/read`，三次之后放弃，
报告服务端坏了。

**原因。** `-32602` 表示「你的参数格式不对」，`-32002` 表示「你问的资源不存在」。
这是两种情况，正确的后续动作完全不同。混在一起，客户端就会去重试一个
永远不可能成功的东西。

**修法。** MCP 特有的条件用 MCP 特有的错误码。

```go
return nil, protocol.NewError(protocol.ErrResourceNotFound,
    "no resource at "+params.URI, nil)
```

**测试。** `TestResourcesAndPrompts` 断言的是错误码而不是文案——
文案可以改，行为不能。

---

## 7. 工具失败和协议失败是两件事

**症状。** agent 重试一个工具三次，放弃，然后告诉用户「工具坏了」。
工具没坏，它只是没找到那个城市。

**原因。** handler 返回了 error，分发层把每个 error 都变成了 JSON-RPC 错误。
但「工具跑了但失败」应该返回一个 `isError` 为 true 的**结果**，
让客户端知道这次调用执行过了、不该重试。

**修法。** 两条路，明确选择：

```go
content, herr := entry.handler(args)
if herr != nil {
    // handler 可以通过返回 *RpcError 主动升级成协议错误
    var rpc *protocol.RPCError
    if errors.As(herr, &rpc) { return nil, rpc }

    // 其余一律是工具级失败
    return result(req.ID, mcptypes.CallToolResult{
        Content: []mcptypes.Content{
            mcptypes.Text(fmt.Sprintf("tool %q failed: %v", params.Name, herr)),
        },
        IsError: true,
    })
}
```

**测试。** `TestToolLevelFailureIsNotProtocolError` 调用一个必然失败的工具，
断言客户端拿到的是带 `isError` 的结果，不是错误。
`TestSchemaViolationIsProtocolError` 是镜像：参数类型不对必须是协议错误。

---

## 附赠三条容易漏的

**从另一个线程关 socket，不会唤醒正在读的那个。** HTTP keep-alive 意味着所有请求
共用一条连接，而 `Client` 同时持有这条连接和一个停在 `recv` 上的读线程。
`close()` 执行时，把 fd 关掉并不会解除已经阻塞在这个 fd 上的 `recv`——那个线程
继续睡在一个已经不存在的描述符上，析构函数里的 `join()` 就永远不返回。
进程在把所有正确答案都打印完之后，挂在退出那一步。
正确的关法是：拿锁、置标志位、唤醒条件变量，让读线程自己看到标志位再退出。

这条值得写下来的原因是——**bug 存在期间，所有单元测试都是绿的**。两个实现都全通过，
进程内没有任何东西能够触达它，因为这个故障需要真实 socket 两侧的线程。
它是把 C++ 客户端指向正在运行的 Go 服务端时才暴露出来的，也就是
`cpp/examples/http_client_main.cpp`——这个文件之所以留在仓库里就是为了这个。
如果你的项目有两份实现，最值钱的测试之一恰恰是那个从不假装它们天然一致的东西。

**通知插到前面也一样要被路由。** 第 5 条的读循环处理了这件事，但 C++ 版的同一个 bug
更隐蔽：对**响应**调用 `parse_request` 会抛异常，因为响应没有 `method` 字段，
而接住异常的 `continue` 会把响应直接丢掉。看起来和客户端挂住一模一样。
所以 C++ 的读循环把响应当普通对象解析，永远不送进请求解析器。

**空的 `arguments` 和没有 `arguments` 不是一回事。** `tools/call` 不带 `arguments`
字段是合法的；在 nil map 上做 `args["x"]`，Go 会 panic，C++ 会读到垃圾。
两个实现都在把参数交给 handler 之前先默认成 `{}`。

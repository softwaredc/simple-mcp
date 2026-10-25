# 协议笔记

这个实现覆盖什么、漏掉什么，以及规范里哪些细节值得在动手前先知道。
英文版：[PROTOCOL.md](PROTOCOL.md)。

## 消息信封

MCP 跑在 JSON-RPC 2.0 上。三种消息形态：

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{...}}   // 请求
{"jsonrpc":"2.0","id":1,"result":{...}}                          // 响应
{"jsonrpc":"2.0","method":"notifications/initialized"}           // 通知
```

没有 `id` 的请求是通知，不回复。响应里 `result` 和 `error` 有且只有一个。
`id` 可以是字符串或数字，必须原样回显——见
[踩坑第 4 条](PITFALLS_CN.md#4-id-必须逐字节-round-trip)。

## 已实现的方法

| 方法 | 方向 | 说明 |
|---|---|---|
| `initialize` | 客户端 → 服务端 | 能力协商 |
| `notifications/initialized` | 客户端 → 服务端 | 无响应 |
| `ping` | 双向 | 探活，空结果 |
| `tools/list` | 客户端 → 服务端 | 返回全部，无分页 |
| `tools/call` | 客户端 → 服务端 | 经过 schema 校验 |
| `resources/list` | 客户端 → 服务端 | 返回全部 |
| `resources/templates/list` | 客户端 → 服务端 | 这里恒为空 |
| `resources/read` | 客户端 → 服务端 | 未知 URI 返回 `-32002` |
| `prompts/list` | 客户端 → 服务端 | 返回全部 |
| `prompts/get` | 客户端 → 服务端 | 检查必填参数 |

## 刻意没实现的方法

- **`tools/list` 分页。** 类型里有 `nextCursor`，但从不设置。工具多到需要分页的
  服务端，已经超出教学版的体格了。
- **服务端到客户端的通知。** 没有 `notifications/progress`、`notifications/cancelled`、
  `notifications/message`（日志）、`.../list_changed`。读循环**路由**通知的逻辑是对的，
  所以加它们只是注册 handler，不是改结构。
- **`logging/setLevel`。** 需要 logging 能力和一个日志出口。
- **`completion/complete`。** 参数自动补全，是锦上添花，不是主路径。
- **采样（sampling）。** 服务端反过来要求客户端跑一次模型补全。这是唯一一块
  真正有意思的缺失，缺失的原因是需要模型才能演示。
- **`resources/subscribe` / `unsubscribe`。** 订阅更新。

## initialize 握手

```
客户端                                服务端
  |  initialize                        |
  |   protocolVersion: "2025-06-18"    |
  |   capabilities: {...}              |
  |   clientInfo: {name, version}      |
  | ---------------------------------> |
  |                                    |  result:
  |                                    |   protocolVersion（服务端选的）
  |                                    |   capabilities
  |                                    |   serverInfo
  |                                    |   instructions?
  | <--------------------------------- |
  |  notifications/initialized         |
  | ---------------------------------> |   （无响应）
```

三个关键细节：

1. **服务端用它支持的版本回应**，可能和客户端要的不一样。本实现认得客户端版本时
   优先用它，否则回退到自己最新的。
2. **能力是可选指针。** 字段缺失表示「不支持」；空对象表示「支持，但没有列表变更通知」。
   `{"tools":{}}` 和没有 `tools` 键是两种不同的声明。
3. **`instructions` 是服务端返回、给模型看的自由文本。** 它是整个协议里最便宜的
   质量杠杆：会把工具用法讲清楚的服务端，被正确调用的频率就是高。

## 工具参数与 JSON Schema

`tools/list` 每个工具带一个 `inputSchema`，必须是 JSON Schema 对象。
`tools/call` 的 `arguments` 是任意 JSON 对象。规范没说谁校验，所以服务端校验，
本实现理解真实工具用到的那个子集：

```json
{
  "type": "object",
  "properties": {
    "city":  {"type": "string"},
    "days":  {"type": "integer"},
    "unit":  {"type": "string", "enum": ["celsius", "fahrenheit"]},
    "tags":  {"type": "array", "items": {"type": "string"}}
  },
  "required": ["city"]
}
```

`type`、`properties`、`required`、`items`、`enum`。超出这些的可以原样透传，
由 handler 自己校验。

注意 `"integer"` 接受整值的数：JSON 只有一种数字类型，「整数」是对取值的约束，
不是另一种类型。

## 内容块

工具结果是一个按 `type` 区分的 content 块数组：

| type | 字段 | 用途 |
|---|---|---|
| `text` | `text` | 几乎所有东西 |
| `image` | `data`（base64）、`mimeType` | 截图、图表 |
| `audio` | `data`（base64）、`mimeType` | 语音 |
| `resource` | `resource: {uri, mimeType?, text? \| blob?}` | 内嵌文件读取 |

提示词消息携带同样的块。tag 写错会产出 `{"type":"text","data":"..."}`，
客户端会拒绝。

## 错误分类

| 码 | 含义 | 何时用 |
|---|---|---|
| `-32700` | 解析错误 | 字节不是 JSON |
| `-32600` | 无效请求 | 不是合法的 Request 对象 |
| `-32601` | 方法不存在 | 未知方法 |
| `-32602` | 参数无效 | 参数畸形、未知工具、schema 违规 |
| `-32603` | 内部错误 | 服务端自己崩了 |
| `-32002` | 资源不存在 | MCP 特有：URI 不存在 |

工具内部出的其他问题**不是**错误码，是 `isError` 为 true 的结果。
见[踩坑第 7 条](PITFALLS_CN.md#7-工具失败和协议失败是两件事)。

## 传输

### stdio

服务端的 stdin/stdout 上每行一个 JSON 对象。这是 Claude Desktop 和大多数
CLI agent 的用法，因为拉起子进程是 agent 宿主能做出的最简单的事。

两条规则：每次写都以恰好一个 `\n` 结尾并 flush；stdout 上除了协议什么都没有。

### Streamable HTTP

客户端向一个端点 POST 消息，服务端回 `application/json`（单个响应）或
`text/event-stream`（SSE 流）。客户端也可以 GET 一个 SSE 流来收服务端到客户端的通知。

**本实现覆盖单响应形态和一次性流。** 它不实现「服务端挂住 POST 持续推送
无限序列」，也不实现长期 GET 流承载真实事件——GET handler 打开流就关闭，
因为演示服务端从不主动发通知。

这是一个真实的简化，写在这里而不是埋起来，因为要决定是否基于它继续建东西的人
需要知道边界在哪。Go 版 HTTP 的客户端和服务端都有；C++ 版只有客户端。

## 协议版本

默认 `2025-06-18`。握手时接受 `2025-03-26` 和 `2024-11-05`。
后续版本移除的字段没有实现，所以 2024-11-05 的客户端拿到的是最新行为
而不是它要求的行为——握手会明确告知，更严格的服务端会直接拒绝。

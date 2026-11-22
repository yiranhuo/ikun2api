# IKUN2api 1.6.5

本版本针对「接上游 Anthropic 保真度」做三处修复，让 API Key 透传链路在入口校验、
未知字段传递和服务端压缩三个维度上更接近官方行为。

## 新增

- 账号级开关 `accounts.extra.anthropic_raw_body_passthrough`（布尔，默认 false）。
  打开后，Anthropic API Key 透传分支跳过 `sanitizeClaudePrivacyBody` 与
  `sanitizeClaudePrivacyHeaders`，客户端 body 与请求头 1:1 送往上游。
  默认关闭，保留既有的隐私裁剪行为（剥离客户端自动上下文、计费头、tool provenance 等）。

- 网关侧 Anthropic 服务端压缩仿真（`claude_compact_emu.go`）。
  客户端在 `context_management.edits[]` 里带 `type` 以 `compact` 开头的编辑时：
  - 用 `len(messages JSON)/4` 估算输入 token，未超过 `trigger.value` 时完全不动作；
  - 超过阈值时把除最近 2 条以外的历史折叠成一段连续性摘要，保留可读的开头/中段/结尾取样；
  - 从上行 body 移除 `context_management`，避免上游缺少 beta token 时 400；
  - 非流式响应把 `{"type":"compaction","content":...}` 插到 `content` 首位，并在
    `usage.iterations` 补一条 `{"type":"compaction",...}` 记账（随后是 `{"type":"message",...}`）。
  该实现不依赖客户端是否携带 `compact-*` / `context-management-*` 的 beta token。

## 修复

- API Key 鉴权入口的 401 响应体改为 Anthropic 官方包络：
  `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`。
  影响三处拒绝路径：请求头超长、key 超长、key 查不到。原先返回的是
  `{"code":"INVALID_API_KEY","message":"Invalid API key"}` 扁平结构。
  ops 错误日志解析（`parseOpsErrorResponse`）第一分支即支持该包络，错误分类不受影响。

## 兼容性

- `anthropic_raw_body_passthrough` 缺失时行为与 1.6.4 完全一致。
- 压缩仿真只在客户端显式发送 `context_management.edits[].type=compact*` 且历史超过阈值时生效，
  普通对话请求不产生任何 body 变化。
- 流式响应不改写 `content` 数组（注入 compaction 块需要同步重排 `content_block_*` 的 index，
  对不支持该块类型的客户端风险高于收益），压缩回执仅在非流式响应中给出。

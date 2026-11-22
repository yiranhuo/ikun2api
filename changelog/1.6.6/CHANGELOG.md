# IKUN2api 1.6.6

本版本把「上游保真度」从账号级单开关扩展为一组可配置的网关侧修复，
并补上三处「直连官方能过、经过本网关被改写」的差异：严格入口、工具请求的
输出预算、thinking 签名失败的自动重试。所有修复都可以在
「系统设置 → 网关服务 → 网关保真度修复」里统一开关，并支持按分组、按账号排除。

## 新增

- 网关保真度修复配置 `gateway_fidelity_settings`（JSON 存储，进程内 30s 缓存，
  面板保存后立即失效）。字段：

  | 字段 | 默认 | 含义 |
  | --- | --- | --- |
  | `enabled` | `true` | 总开关。关闭后所有修复都不生效 |
  | `entrance_strict` | `true` | 严格入口 |
  | `tool_max_tokens_floor` | `true` | 工具请求抬 `max_tokens` |
  | `tool_max_tokens_floor_value` | `4096` | 抬底目标值（256–128000） |
  | `thinking_binding_gated` | `true` | thinking 签名重试的 beta 门控 |
  | `excluded_groups` | `[]` | 分组级排除名单 |
  | `excluded_accounts` | `[]` | 账号级排除名单 |

- 管理端点 `GET/PUT /api/v1/admin/settings/gateway-fidelity`。
  走独立端点而不是塞进整文档 `PUT /api/v1/admin/settings`，避免旧前端只提交
  部分字段时把其它设置重置为零值。

- 前端「系统设置 → 网关服务」新增「网关保真度修复」卡片：总开关、三个子开关、
  抬底数值输入、分组/账号排除列表，中英文案齐备。

## 修复

- **严格入口**：请求体带 `context_management` 而 `anthropic-beta` 里既没有
  `context-management-*` 也没有 `compact-*` 时，返回官方 400
  `{"type":"error","error":{"type":"invalid_request_error","message":"context_management requires the context-management-2025-06-27 beta header"}}`。
  此前网关会静默接受，与直连官方行为不一致。
  `compact-*` 必须放行——服务端压缩只带 `compact-2026-01-12` 一个 token，
  否则压缩请求会被自己的严格入口拒掉。

- **工具请求 max_tokens 抬底**：`tools` 非空且 `max_tokens` 低于
  `tool_max_tokens_floor_value` 时抬到该值。原因：思考预算与输出预算共用
  `max_tokens`，思考块吃满后工具参数回填为空、`stop_reason=max_tokens`，
  表现为「大工具参数丢失」。抬高只是上限，不产生额外计费；缺 `max_tokens`
  的请求不补（保持「拒绝缺少 max_tokens」的入口校验）。

- **thinking 签名重试的 beta 门控**：上游对 thinking 签名有绑定校验，
  只有客户端显式携带 `thinking-binding-controls-*` beta 时才代表它能处理签名
  失效。此前网关无条件做「剥离历史 thinking 后重试」，把上游本该返回的 400
  洗成 200；现在未携带该 beta 的请求不做任何 thinking 历史改写，也不做签名
  重试，上游的校验结果原样返回。

## 兼容性

- 配置缺失或读取失败时按默认值（全部开启）处理，行为与 1.6.5 相同。
- 排除名单命中（分组或账号）时四项修复全部跳过，等价于 1.6.5 之前的透传行为。
- 抬底只作用于 `tools` 非空的请求；纯文本请求的 `max_tokens` 原样转发。
- 1.6.5 的 `accounts.extra.anthropic_raw_body_passthrough`、压缩仿真、
  401 官方包络均保持不变。

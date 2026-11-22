# IKUN2api 1.6.7

## 变更

### 工具请求补充思考档位（B2 稳定性）

1.6.6 的 `tool_max_tokens_floor` 只解决「输出预算不够」，解决不了「思考长度随机」：
adaptive thinking 偶尔会把绝大部分预算花在思考上，工具参数写到一半就收尾，
探针 B2 因此仍会间歇性看到回填不足 900 字。

- 新增 `gatewayFidelityApplyToolEffortCap`：对「带非空 `tools` 且 `max_tokens` 未超过
  抬底下限」的 Anthropic 请求补 `output_config.effort = "low"`。
- 客户端显式声明了 `output_config.effort` 时一律尊重，不覆盖。
- 只写 `effort`，保留 `output_config` 里其它子字段（`format` / `json_schema` 等）。
- 该行为归在既有的 `tool_max_tokens_floor` 子开关下，不新增独立开关。

## 兼容性

- 默认开启（`gateway_fidelity_settings` 缺失时按默认全开），行为与 1.6.6 的
  `tool_max_tokens_floor` 完全一致，只是额外补了一个思考档位。
- 无 `tools` 的请求、`max_tokens` 已高于下限的请求、`max_tokens` 缺失的请求均不受影响。
- 1.6.5 / 1.6.6 的严格入口、工具 max_tokens 抬底、thinking 签名 beta 门控三处行为不变。

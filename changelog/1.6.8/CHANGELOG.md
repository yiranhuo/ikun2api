# IKUN2api 1.6.8

## 新增

- **正文预算保底**（`body_budget_floor`）：任意 Anthropic 请求（不限工具请求）只要 `max_tokens` 低于下限，自动抬到下限（默认 512）。官方 adaptive thinking 在 `max_tokens` 极小时偶发把整份预算花在思考上，导致 `stop_reason=max_tokens` 且正文一个字都没有；抬高只是抬高上限，不产生额外计费，也不改变 `input_tokens`。
- 系统设置 → 网关服务 → 网关保真度修复：新增「正文预算保底」开关与「正文保底值」（64 - 128000），与既有总开关、单项开关、分组级 / 账号级排除名单一致生效。

## 修复

- 探测脚本 A3（`max_tokens=32` 的极短请求）偶发空正文：经网关后稳定返回正文，使四条链路（直连 new-api / 直连 sub2api / 经网关 new-api 账号 / 经网关 sub2api 账号）结果一致。

## 兼容性

- 新字段使用缺字段回填默认（`body_budget_floor=true`、`body_budget_floor_value=512`），老版本落库的 `gateway_fidelity_settings` JSON 无需迁移；管理员显式写入的 `false` 会被保留。
- 关闭总开关或把账号 / 分组 ID 加入排除名单后，行为与 1.6.7 完全一致。

# IKUN2api 1.6.10

## 上游同步

- 合入上游 `1812095643/ikun2api` 至 `9279699`（合入 Sub2API 0.2.25 并补齐失效重登额度），共 9 个提交。
- 流式请求等待响应头超过 90 秒时安全切换其他账号；新增 `requesttiming` 与 `stream_header_timeout` 支持。
- 新增账号定时调度（`accounttimer`）与连接诊断、内容审计账号维度接口。
- OpenAI OAuth 失效重登支持认证异常阈值、统计窗口与重登间隔，旧任务不覆盖新授权。
- 内容审计新增上游账号免审白名单，覆盖请求审计与事后风控记录；修复正常账号配置与运维请求被误拦截。
- 补齐 Gemini 4 Neon、Hydrogen、Nano Banana 2.1 与 Antigravity 思考档位、模型元数据及价格匹配。
- `gorilla/websocket` 升级到 `v1.5.4-0.20240701034025-d67f41855da4`。

## 冲突处理

- `backend/cmd/server/VERSION`、`frontend/package.json`：取本仓库序列并推进到 1.6.10，避开与上游 1.6.2 的同名 tag 撞车。
- `changelog/1.6.1`、`changelog/1.6.2`：两边内容都是新增文件，按「本仓库 / 上游」两节并存保留。
- `README.md`：取上游新版结构化文档，仓库链接换回本仓库，并补回维护者发布流程入口。
- `backend/internal/service/gateway_anthropic_passthrough.go`：两边改动同时保留 —— 本仓库的 `rawBodyPassthrough` 开关继续生效，脱敏调用改用上游的账号感知版本 `sanitizeClaudePrivacyHeadersForAccount(req.Header, account)`。

## 发布链路

- `sync-upstream.yml` 冲突 issue 正文里 `--local-version` 的占位符改为真实版本号。
- 新增版本号撞车检测：拉取上游时读取 `upstream/<ref>:backend/cmd/server/VERSION`，撞车时在 issue 正文显式提示需先把本地版本推进到上游之上。

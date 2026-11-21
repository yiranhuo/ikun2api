# IKUN2api 1.6.1

- 仓库发布源切换为 `yiranhuo/ikun2api`：更新检查、Release 产物、容器镜像与签名公钥全部指向本仓库，不再回读上游 `1812095643/ikun2api`。
- 独立 Ed25519 发布签名密钥接入 CI。`scripts/release/sign.py` 会用私钥推导公钥并与 `backend/internal/service/release_signature.go` 中的 `ReleaseSigningPublicKey` 比对，不一致直接中断发布，避免签出无法通过更新校验的包。
- 发布流水线改为完全由 `${{ github.repository }}` 派生镜像名与 manifest 仓库字段。原来写死上游 owner 的 `REGISTRY_IMAGE`、`release-manifest.json` 现在跟随仓库走，派生仓库无需改 CI 即可出包。
- 更新下载校验仍只接受 `https://github.com/<repo>/releases/download/v...` 前缀，仓库名随常量一起切换，跨仓库 URL 依然被拒。
- 安装脚本 `deploy/install.sh` 支持 `IKUN_GITHUB_REPO` 覆盖，默认走本仓库；服务单元、容器编排、Dockerfile 标签与前端版本徽章中的仓库链接同步更新。
- 保留上游署名：`NOTICE.md` 中 `1812095643/ikun2api` 的修改声明保留，并追加本仓库的后续修改声明。

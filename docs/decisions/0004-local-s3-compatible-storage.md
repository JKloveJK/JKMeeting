# 0004：P0 本地对象存储从 MinIO 切换为 SeaweedFS

- 日期：2026-09-29
- 状态：已接受
- 关联：`docs/plans/2026-09-29-p0-minimal-loop-design.md`、`docker-compose.yml`

## 背景

P0 原计划使用 MinIO 提供本地 S3 兼容对象存储。2026-09-29 本机实测发现：

- `minio/minio` Docker Hub 仓库不可拉取；
- `quay.io/minio/minio:latest` 匿名拉取返回 `401 Unauthorized`。

因此 MinIO 已无法作为可复现的本地开发依赖。用户已确认替换为 SeaweedFS。

## 决定

1. 使用实测可拉取的 `chrislusf/seaweedfs:3.68` 作为 P0 单容器 S3 兼容对象存储；`seaweedfs/seaweedfs:3.68` 在本机 Docker Hub 返回“repository does not exist”。
2. 保持本地 S3 API 端口为 `9000`，以避免后续客户端与 Go 服务端的 endpoint 语义变化。
3. 将 SeaweedFS filer 管理端口映射到本机 `9001`，master 状态端口映射到 `9333`。
4. 从本机私有环境运行 `make prepare-local-config`，生成被 `.gitignore` 排除的 `server/seaweedfs/s3.json`；不把 S3 access key、secret key 或管理员凭据提交到仓库。当前先仅在回环地址开放本地端口，真实设备联调前再确认凭据与局域网安全边界。
5. API 仍必须使用标准 S3 预签名 PUT / GET URL；后续业务代码不得依赖 SeaweedFS 的非 S3 私有 API。

## 后果

- P0 的对象存储实现从 MinIO 改为 SeaweedFS，但客户端上传模型、Go SDK 选择和 HTTP 接口保持不变。
- MinIO Console 不再可用；开发期可通过 SeaweedFS filer 管理端口观察对象。
- 生产环境对象存储不在 P0 范围，本决定仅约束本地开发闭环。

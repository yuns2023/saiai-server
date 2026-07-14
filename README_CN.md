# SAIAI Server

[English](README.md)

SAIAI Server 是一个可自托管的 AI API 网关，包含管理 Web UI。它负责验证客户端
API Key、转发支持的供应商协议、执行账号调度与用量控制，并提供 SAIAI V2 客户端
使用的 schema-2 bootstrap 协议。

本项目最初派生自 Sub2API，现在是独立的社区项目；它不是 Sub2API 或任何上游 AI
供应商的官方网站、官方服务或官方发行版。来源和版权归属请参阅
[NOTICE](NOTICE) 与[源码来源记录](docs/SOURCE_PROVENANCE.md)。

## 仓库边界

| 仓库 | 可见性 | 职责 |
| --- | --- | --- |
| `yuns2023/saiai-server` | 公开 | Gateway 后端、管理前端、数据库迁移、测试、发布镜像和 V2 bootstrap API |
| [`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client) | 公开 | `saiai` CLI、Tauri 桌面应用、各平台安装器和客户端发布资产 |
| `saiai-ops` | 私有 | 部署专用配置、密钥、基础设施清单、备份和运维记录 |

客户端源码和发布二进制属于 `saiai-client`。部署凭据和私有基础设施信息不得提交到
本仓库。
为了从 Gateway 当前公开域名生成简短安装命令，服务端会在
`scripts/saiai-cli/` 和 `frontend/public/saiai-cli/` 保留经测试的、可按请求来源渲染的
wrapper 镜像；客户端源码和二进制发布的权威仓库仍是 `saiai-client`。

## 主要能力

- 支持 Anthropic、OpenAI 及相关账号类型的兼容网关路由
- API Key、分组、账号、额度、并发、速率限制和用量控制
- 粘性会话与账号调度
- 用于管理用户、Key、分组、账号和可观测性的 Vue 管理界面
- PostgreSQL 持久化与 Redis 协调
- 经过审查的 SAIAI V2 bootstrap 实现，不产生模型调用费用

支持某种协议或账号类型并不代表获得上游服务的使用授权。运营者和用户需要自行遵守
供应商条款、账号权限、数据处理要求及适用法律。
本仓库不分发第三方 OAuth client_secret。需要这类凭据的兼容 OAuth 流程默认禁用；
只有运营者提供有权使用的凭据并接受供应商条款后才会启用。
Sora 去水印解析不内置任何第三方端点。请求必须显式提供自定义
`watermark_parse_url`；否则 SAIAI 不会把生成的 post 发布给解析服务，并按请求配置的
回退策略处理。

## SAIAI V2

V2 是全新的干净客户端模式。Claude 与 Codex 是可选、彼此独立配置的产品；只配置
其中一个时，不得要求输入另一个产品的 Key。两个产品共用一个 Gateway URL，同时保留
独立凭据和隔离的客户端目录。

Gateway 协议入口为：

```http
GET /api/v1/client/bootstrap
Authorization: Bearer <SAIAI API key>
```

Schema 2 会区分原生 Claude 能力与 OpenAI 分组可选的 `/v1/messages` 协议适配。
尤其是，`openai_messages_dispatch=true` 绝不能让 `capabilities.claude` 变为
`true`。Bootstrap 只在本地完成认证，不选择上游账号，也不发送模型请求。

完整响应结构、能力字段、安全属性和成对发布规则见
[V2 Gateway 协议](docs/V2_GATEWAY_CONTRACT.md)。

先安装实际要使用的官方 Claude Code 或 Codex CLI；SAIAI 不会安装这两个上游
客户端。然后安装 SAIAI，再只启动需要配置的产品。SAIAI 安装过程不会要求输入
API Key。首次启动产品时，如果尚未保存共享 Gateway URL，会先询问 URL，然后只
要求输入当前产品自己的 SAIAI Key，绝不会要求另一个产品的 Key。

Windows PowerShell：

```powershell
irm https://api.saiai.top/saiai-cli/setup.ps1 | iex
Invoke-Saiai install
saiai codex # 或：saiai claude
```

PATH 排障、按产品 revoke、完全重置以及 Preview 更新/签名边界见客户端仓库的
[完整 Windows 指南](https://github.com/yuns2023/saiai-client/blob/main/docs/WINDOWS.md)。

Linux 或 macOS：

```bash
curl -fsSL https://api.saiai.top/saiai-cli/setup.sh | bash -s -- install
"$HOME/.local/bin/saiai" codex # 或："$HOME/.local/bin/saiai" claude
```

Unix 安装器会打印实际绝对路径；如果通过 `SAIAI_INSTALL_DIR` 修改了默认目录，
请使用安装器打印的路径。

自托管运营者应将 `https://api.saiai.top` 替换成自己的 HTTPS Gateway 来源。可以用
`saiai doctor` 输出适合排障的诊断摘要。V2 不迁移旧模式状态；需要完全重置 V2 时，
执行 `saiai revoke --all` 后再按产品重新初始化。

## 开发

前置环境：

- `backend/go.mod` 声明的 Go 版本
- Node.js 24，以及通过 Corepack 使用 `frontend/package.json` 固定版本的 pnpm
- 集成测试和本地运行所需的 PostgreSQL 与 Redis

常用检查：

```bash
cd backend
go test -tags=unit ./...

cd ../frontend
corepack enable
pnpm install --frozen-lockfile
pnpm run lint:check
pnpm run typecheck
pnpm run test:run
```

开发时优先运行有针对性的测试，再交给公开 CI 完成完整矩阵。开发流程见
[CONTRIBUTING.md](CONTRIBUTING.md)，部署模板见 [deploy/](deploy/)。公开到不受信任
网络之前，请逐项审阅模板、生成独立强密钥、固定发布制品并配置 HTTPS。

维护者发布配对的 Gateway 与客户端时，还应遵循与具体生产拓扑无关的
[发布运维手册](docs/RELEASE_OPERATIONS.md)。

## 安全

不要在 Issue 中公开疑似漏洞或凭据。私密报告方式与部署注意事项见
[SECURITY.md](SECURITY.md)。

## 许可证

除非文件或第三方声明另有说明，本仓库发行的 SAIAI Server 组合作品以 GNU 宽通用
公共许可证第 3 版或任何更高版本（`LGPL-3.0-or-later`）提供。纳入本项目且最初按
MIT 条款取得的部分和其他已识别第三方材料记录在 [NOTICE](NOTICE) 中；这些声明
继续有效。

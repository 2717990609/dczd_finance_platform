# 财务平台

一期提供产品目录、来源映射、期次、规则组件、人员身份映射和来源订单查询。后端使用 Go、go-zero 和 PostgreSQL，前端使用 Vue 3、Vite 和 Ant Design Vue。

| 目录 | 内容 |
| --- | --- |
| `server/` | Go 模块、路由、数据库结构与后端测试 |
| `web/` | Vue 页面、表格与配置表单 |

系数、金额和规则适用范围保存在规则版本中。订单修订各保存一次并可回查源订单号及原始业务字段。源端数据纠正后可按原订单 ID 从财务主动重拉。一期验收以来源目录和配置链为主，自动整批同步、影子核算和正式结算安排在后续阶段。

## 本地运行

需要 Go 1.25、Node.js 22、pnpm 10 和专用 PostgreSQL 开发库。首次配置时将 `.env.example` 复制为 `.env.local`，填写本地开发库连接。库名须以 `_dev` 结尾，并与 `FINANCE_EXPECTED_DATABASE` 一致。服务启动时初始化一期表。此配置文件仅保存在本机。

```sh
test -e .env.local || cp .env.example .env.local
chmod 600 .env.local
```

填写后，在仓库根目录启动。

```sh
set -a
. ./.env.local
set +a
go -C server run .
```

在另一个终端启动前端。

```sh
pnpm --dir web install
pnpm --dir web dev
```

前端地址为 `http://127.0.0.1:5176/`，开发代理转发至后端 `127.0.0.1:8096`。`/healthz` 检查 HTTP 服务，`/readyz` 检查开发库连接。

如需重拉某个来源的订单，在本机 `.env.local` 配置该来源的只读 API 根地址及令牌，重启后端。订单列表显示配置状态及最后新增记录的时间。

```sh
curl --fail http://127.0.0.1:8096/healthz
curl --fail http://127.0.0.1:8096/readyz
go -C server test ./...
pnpm --dir web build
```

# 财务平台

一期提供产品目录、来源映射、期次、规则组件、人员身份映射和来源订单查询。后端使用 Go、go-zero 和 PostgreSQL，前端使用 Vue 3、Vite 和 Ant Design Vue。

| 目录 | 内容 |
| --- | --- |
| `server/` | Go 模块、路由、数据库结构与后端测试 |
| `web/` | Vue 页面、表格与配置表单 |

规则版本保存分项、费率、适用范围及来源角色。订单修订各保存一次，试算明细引用订单、人员归属和规则版本。

本地工作台可配置自然月和 25–24 等固定账期，试算销售及推荐提成，查看唯一最新草稿和待核项。固定账期按上海时间划分，人员归属按实际付款时点读取。同一核算范围、规则和财务结果重复试算时沿用当前草稿。活动签到、阶梯、历史调整和正式结算仍在实现中。

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

开发验证可由技术人员使用现有测试来源接口选取样本。已配置单笔接口的环境可按原订单 ID 查看来源更新。当前阶段的自动数据拉取安排在后续实施。

```sh
curl --fail http://127.0.0.1:8096/healthz
curl --fail http://127.0.0.1:8096/readyz
go -C server test ./...
pnpm --dir web build
```

指定 `FINANCE_TEST_DATABASE_URL` 可运行 PostgreSQL 集成测试。请使用名称以 `_dev` 结尾的独立测试数据库和有权创建临时测试 schema 的账号。测试会在对应 schema 内创建和验证数据，随后清理该 schema。

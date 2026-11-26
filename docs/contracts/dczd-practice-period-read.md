# 大春之道实战班期次读取契约

财务在「核算期次」选择产品并发起读取。大春之道通过订单读取服务返回来源班次。首批覆盖已有财务映射的健康观念实战班和家庭教育实战班，对应来源策略号 1 和 4，均为线上问卷班次。固定月账期继续使用财务的账期设置。

## 来源接口

使用大春之道订单服务的地址和 Bearer 凭证。请求为 `GET /internal/v1/finance/source-orders/practice-periods`，协议头为 `X-DCZD-Finance-Contract: dczd.finance-practice-period-read/1.0`。

| 参数 | 语义 |
| --- | --- |
| `strategy_id` | 来源产品策略号，当前支持 1 或 4 |
| `after_id` | 上页扫描到的活动 ID，首页为 0 |
| `through_id` | 首页返回的活动主键上界，后续所有页沿用 |
| `limit` | 单页扫描数，范围 1 至 200 |

响应包含 `contract_version`、`strategy_id`、`through_id`、`next_after_id`、`has_more`、`skipped` 和 `periods`。每个班次有 `periodKey`、`name`、`ordinal`、`startsOn` 和 `endsOn`。期次键为 `PRACTICE:2:<strategy_id>:<activity_id>`，日期使用来源活动的北京时间日期。业务期数取活动名称中的明确编号，例如「158期」或「第20届」，与活动主键各自独立。缺少日期、期数不明确或起止倒置的班次计入 `skipped`。游标按扫描过的活动推进，空结果页也可能有下一页。

`startsOn` 和 `endsOn` 以 `YYYY-MM-DD` 传输，MySQL 历史零日期按缺失日期处理。本地 `source_mappings.source_key` 保持文本，只在调用本接口时将已支持的策略号转为整数。

## 财务写入

`POST /api/v1/periods/pull` 接收本地 `productId`。财务从现有订单产品映射解析来源策略号，核验每页范围后再写入。响应返回新建数量、已登记数量、来源跳过数量和冲突期次键。同一来源期次重复拉取会复用已登记记录。发现已有期次的编号、绑定或日期不一致时，列入冲突，由现有期次维护入口核对。新登记期次进入既有重新试算队列。

期次读取不携带付款或签到内容。新班次继续使用现有的订单与退款同步，以及按来源期次读取签到名单。

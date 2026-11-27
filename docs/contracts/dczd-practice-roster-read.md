# 大春之道实战班报名与签到读取契约

## 随订单同步报名与签到

大春之道通过订单就绪接口的 `practice_registrations_contract` 字段声明报名读取协议。财务按开放班次的来源期次键调用 `GET /internal/v1/finance/source-orders/practice-registrations`。凭证沿用订单读取配置，契约头为 `dczd.finance-practice-registrations-read/1.0`，请求参数为 `period_key`。

响应包含实际开班时点、已签到报名集合 `enrollments` 及其哈希，还有包含停用、退款、延期和未签到状态的 `registrations`。推荐人及其公司取自每条报名记录。有效签到的付款关联有问题时，`valid_payment_links=false`，原始报名仍可同步，该班次暂不可试算。读取全程使用同一源端数据视图。

财务按期次保存最新报名状态，并在签到范围、开班时间或签到报名归属变化时保存新版本。开放草稿读取最新版本。财务确认结算时复验最新来源事实，正式账继续绑定原版本。新同步班次使用财务配置的退款截止时间，历史已归档班次仍可使用原来的截止口径。

## 按报表月份同步付款与退款

财务在任务首次启动时取北京时间的月份，自动选择有效报表月份大于等于该月的未结算班次。手动设置的报表月份优先，未设置时取期次结束日期所在月。结算核对入口可按指定期次补拉。期次选择以财务期次和来源产品映射为准。

财务通过 `GET /internal/v1/finance/source-orders/readiness?stream=PRACTICE_ORDER_FACTS` 查询实战班专用能力。商城就绪响应同时提供 `practice_facts_contract`、`practice_refunds_contract` 和 `practice_orders_through` 后，财务从已有完整历史覆盖接续，启用以下读取。凭证和契约头沿用报名接口的传递方式。

| 路径 | 契约 | 范围 |
| --- | --- | --- |
| `POST /internal/v1/finance/source-orders/practice-facts` | `dczd.finance-practice-facts/1.0` | 请求为最多 200 条 `kind`、来源记录 `id` 和可选 `known_version`，按报名关联查询付款和签到。响应返回版本有变化的 `facts` 和来源缺失的 `missing`。 |
| `GET /internal/v1/finance/source-orders/practice-refunds` | `dczd.finance-practice-refunds/1.0` | 按退款表的 `refund_success_time,id,order_code` 分页。首次响应给出 `through`，续页沿用同一个上界。原单的期次和付款时间均可早于当前报表月。 |

退款分页每页在同一财务事务内保存订单、退款事实和游标。缺少原付款时只按订单 ID 补查。发生来源错误时从最后落库的游标续拉。退款成功时间只用于同步进度，事实中的原有退款时间继续供结算使用。历史无成功时间的记录沿用初始历史同步。付款及签到按期次记录覆盖，退款单独记录覆盖进度。

## 历史确认名单接口

旧验收路径继续使用历史名单契约。新的工作台通过上面的订单同步流程更新报名和签到。

财务按来源期次主动读取确认名单。来源负责比较最近一次确认名单与当前业务报名、签到和开班时点，并返回是否仍然一致。财务用这份结果核对当前草稿。正式确认提交时再次向来源核验，确认后保存本次核算的订单、名单和规则版本。

名单的 `strategy_id` 与实战班付款的 `source_product_code` 对应。选名单、保存关联、拉取和开放期试算均核对产品映射。健康观念实战班和家庭教育实战班分别使用各自的来源策略，不能把一边的名单挂到另一边的产品。匹配从现有来源映射读取，新增产品可使用同样的配置。

### 请求

| 项目 | 约定 |
| --- | --- |
| 方法和路径 | `GET /internal/v1/finance/source-orders/practice-rosters` |
| 来源及凭证 | 大春之道订单服务配置中的 `FINANCE_DCZD_URL` 和 `FINANCE_DCZD_TOKEN`，使用 `Authorization: Bearer <token>` |
| 协议头 | `X-DCZD-Finance-Contract: dczd.finance-practice-roster-read/1.0` |
| `period_key` | 实际开班期次稳定键，格式 `PRACTICE:<activity_type>:<strategy_id>:<activity_id>` |
| `include_versions` | 省略时只返回核验字段。值为 `1` 时附带首次和最新确认名单 |

源端使用服务凭证鉴权，读取和核验在同一数据视图中完成，返回 `Cache-Control: no-store`。生产通过 HTTPS 对接。响应使用 JSON，正文上限为 8 MiB。

### 响应

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `contract_version` | 字符串 | 固定为 `dczd.finance-practice-roster-read/1.0` |
| `period_key` | 字符串 | 与请求期次一致 |
| `current` | 布尔值 | 已确认名单仍与当前开班和签到事实一致时为 `true`。暂无确认版或业务已发生变化时为 `false` |
| `has_payment_conflict` | 布尔值 | 名单中同一应计的订金或尾款也出现在其他仍有效的已确认班次名单中时为 `true`。按付款主键检查来源当前状态，覆盖尚未被财务拉取的班次 |
| `roster_version` | 正整数 | 最新已确认版本 |
| `roster_hash` | 字符串 | 最新确认版本存档原文中的 `sha256:` 哈希 |
| `period_start_at` | RFC 3339 字符串 | 最新确认版本存档中的实际开班时点。问卷型健康观念实战班使用业务活动的接应时间 |
| `confirmed_at` | RFC 3339 字符串 | 最新确认版本存档原文中的确认时点 |
| `first_confirmed_at` | RFC 3339 字符串 | 同一期次第 1 版存档原文中的确认时点，供班次试算区分名单确认前后的退款 |
| `first_roster`、`latest_roster` | 对象 | 请求 `include_versions=1` 且 `current=true` 时返回。内容为存档的 `dczd.practice-roster/1.0` 对象。只有 1 版时两个字段返回同一对象 |

`current=true` 时，确认版本的元数据和付款冲突状态必须齐全。`current=false` 时可省略这些字段。时间字段使用存档 JSON 中的时点和精度，以保留最初确认时刻。名单继续使用 `dczd.practice-roster/1.0` 既有内容哈希计算方式。接口支持只读服务鉴权和核验，无需触发业务确认或推送。

期次键格式正确但找不到对应期次、尚无确认名单，或签到与开班时点已变更时，返回 HTTP 200、请求的 `period_key` 和 `current=false`。鉴权失败返回 HTTP 401 或 403。订单就绪状态与名单读取分别验收。名单路径返回 HTTP 404、405 或 501 时，工作台提示核对名单接口的部署或路由，并暂停该班次关账。返回 401 或 403 时提示核对服务凭证和授权。

### 商城接入

在大春之道商城后端的 `DczdOrderFactReadController` 增加上述 GET 路由，沿用订单读取的服务鉴权和契约头校验。名单读取与校验由 `PracticeRosterService` 在同一个只读事务中完成：

1. 用期次键解析出的策略、活动类型和活动 ID 定位 `dc_finance_practice_roster`，通过已有的期次及版本唯一索引读取第 1 版和最新确认版。返回存档内容中的开班、确认时点和哈希。没有确认版本时返回 `current=false`。
2. 复用 `matchesCurrent` 核对实际开班时点、有效签到及付款关联。名单内容已变更时返回 `current=false`。
3. 按最新有效确认名单中的付款主键核对其他班次。已延期、退款退出或已被新确认版移除的报名，按当前有效名单判断。只要同一付款仍应计入两个有效确认名单，就返回 `has_payment_conflict=true`，包括财务还没拉取的班次。

2026-11-23 核对商城远端 `master`（`2d6ca939d`）。现有报名管理的 `practiceRosterStatus` 返回确认和发送状态。确认版存档、现状比对和重发逻辑已存在，订单读取控制器尚缺上面的名单 GET 路由。同日使用配置为 TEST 的订单接入凭证只读请求，订单 `readiness` 返回 HTTP 200 和 `READY`，健康观念第 156 期（`PRACTICE:2:1:100`）和第 157 期（`PRACTICE:2:1:101`）的名单 GET 均返回 HTTP 404。

2026-11-24 重新调用 TEST，订单就绪状态仍为 `READY`，第 156、157 期名单读取均返回 HTTP 404。财务侧用第 156 期归档和隔离来源完成确认流程，实际来源的名单读取及联调验收仍待完成。

接通后，用同一来源入口分别验证：

- 已确认且未变更的班次返回 `current=true`，两种读取模式的版本、哈希和时间一致。打开 `include_versions=1` 时，实际存档通过财务端内容校验。
- 未确认或签到发生变化的班次返回 `current=false`。有重复付款时返回冲突标记。财务已有旧草稿时，提交结算也必须重新核对来源状态。
- 订单就绪、期次已映射、名单可拉取之后，在隔离数据库用实际名单和关联订单完成试算、结算核对和确认，比对报名范围、计提明细和受益人。财务侧隔离用例 `TestPracticeRosterPullAndSettlementUseCurrentSourceVersion` 覆盖接口缺失、鉴权失败、名单版本变化、付款冲突和重试。

## 财务行为

- 在「分润试算」选择有来源期次的班次，点击「拉取签到名单」。财务核对期次、版本、开班和确认时点，校验名单内容哈希，将两个必要版本在同一事务写入。相同内容重复拉取会复用对应版本。班次草稿在手动试算或定时处理时读取最新确认版。
- 拉取完成后，工作台立即显示最新名单版本。首次拉取后如果本期没有适用规则，授权配置人员可直接配置本期规则。工作台按来源期次标识使用确认名单，并按实际开班时间匹配适用规则。
- 在「结算核对」检查当前名单、订单同步覆盖、待核项和团队池分配。授权财务确认时重新计算并复验源端名单。核对标识绑定实际使用的版本及首次确认时刻，即使财务金额相同，刷新名单后也须重新核对。
- 同一个确认请求可安全重试，返回已有的正式结算。正式账绑定确认时核验通过的来源版本。来源在核验返回之后发生的变化走后续财务更正。

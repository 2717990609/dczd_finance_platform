<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import CatalogRuleMatches from './CatalogRuleMatches.vue'
import DraftReport from './DraftReport.vue'
import DraftPoolAllocations from './DraftPoolAllocations.vue'
import DraftSettlementReview from './DraftSettlementReview.vue'
import { benefitStatus, salesRoles, salesStatus } from './catalog-labels'

const props = defineProps({ periods: Array, componentOptions: Array, memberOptions: Array, reportDimensions: Array, sourceStreams: Array, api: Function, formatMoney: Function, formatTime: Function })
const selected = ref()
const view = ref('detail')
const draft = ref()
const loading = ref(false)
const calculating = ref(false)
const error = ref('')
const refreshStatus = ref()
let sequence = 0
let statusSequence = 0
let statusTimer
let unmounted = false

const options = computed(() => props.periods.map(period => ({
  value: period.id,
  label: `${period.productName} · ${period.name}`,
})))
const selectedPeriod = computed(() => props.periods.find(period => period.id === selected.value))
const hasMatching = computed(() => !!selectedPeriod.value?.ruleMatchingAvailable)
const matchingOnly = computed(() => hasMatching.value && !selectedPeriod.value?.catalogDraftAvailable)
const newerDraft = computed(() => refreshStatus.value?.version > (draft.value?.version || 0))
const componentLabel = code => {
  const type = code?.startsWith('period:') ? code.split(':')[1] : code
  if (type==='company_referral') return '公司推荐分润'
  return props.componentOptions.find(option=>option.value===type)?.label || type
}
const memberLabel = type => props.memberOptions.find(option=>option.value===(type===3 ? 'new' : 'retrain'))?.label
const reason = {
  ...salesStatus,
  ...benefitStatus,
  INVALID_PAYMENT: '付款金额或付款编号待核',
  UNCLASSIFIED: '订单场景分类待核',
  MAPPING_MISSING: '订单没有匹配的场景映射',
  MAPPING_CONFLICT: '订单同时命中多条场景映射',
  RULE_MISSING: '付款时点没有适用规则',
  RULE_CONFLICT: '同一付款命中多份规则',
  VIP_SALES_RULE_UNSUPPORTED: '销售分项待启用',
  UNCONFIGURED_COMPONENT: '此分项尚未配置可用的来源角色或计算方式',
  MISSING_PAYMENT_ID: '缺少原付款 ID',
  DUPLICATE_PAYMENT: '同一付款出现多条记录，需核对商品行及金额分摊',
  PAYMENT_ALREADY_SETTLED: '此付款已在其他账期结算，请核对原账与来源变更',
  MISSING_PAID_AMOUNT: '缺少有效的实付金额',
  MISSING_EFFECTIVE_RULE: '付款时点没有有效规则',
  AMBIGUOUS_PARTICIPANT: '同一来源角色有多名受益人',
  MISSING_PARTICIPANT: '订单缺少该来源角色',
  AMBIGUOUS_IDENTITY: '付款时点命中多份人员归属',
  MISSING_IDENTITY: '付款时点查不到该来源人员的归属',
  REFUND_PERFORMANCE_PAYMENT_MISSING: '没有可对应的原付款业绩，请核对原订单',
  PRACTICE_DUPLICATE_PERFORMANCE_PAYMENT: '同一原付款出现多次，请核对报名与付款记录',
  REFUND_MISSING_AMOUNT_OR_ID: '退款缺少有效退款编号或金额',
  REFUND_DUPLICATE_ID: '同一原退款编号有多条来源事实',
  REFUND_MISSING_PAYMENT_REFERENCE: '退款未明确关联原付款',
  REFUND_ORIGINAL_NOT_SETTLED: '找不到可对应的原付款提成',
  REFUND_PAYMENT_AMBIGUOUS: '退款匹配到多笔原付款',
  REFUND_AMOUNT_EXCEEDS_PAYMENT: '累计退款或扣回金额超过原付款或原计提',
  REFUND_POLICY_MISSING: '适用规则缺少退款处理口径',
  REFUND_BOOKED_CHANGED: '已入账退款金额或关联有修订，需核对后办理更正',
  CORRECTION_SCOPE_CHANGED: '原付款标识、产品或发生时间有变动，需核对归属',
  CORRECTION_MISSING_FACT_OR_RULE: '缺少结算时使用的推荐规则或有效的最新付款金额',
  CORRECTION_REFUND_CONFLICT: '最新付款与已登记退款无法按结算时规则对平',
  CORRECTION_RECOVERY_PENDING: '原受益人的扣回处理尚未确认',
  PRACTICE_DUPLICATE_ENROLLMENT: '同一报名和付款仍在其他期次，请在来源名单核对延期',
  PRACTICE_REFUND_PENDING: '对应付款已有退款，请重新试算',
  PRACTICE_REFUND_AFTER_ROSTER: '名单确认后发生退款，待按退款归属账期处理',
  PRACTICE_REFUND_PAYMENT_TIME: '退款时间早于原付款，请在子系统核对',
  PRACTICE_REFUND_ROSTER_CHANGED: '付款已全退，但报名仍在名单中，请核对并更新确认名单',
  PRACTICE_MEMBER_RULE_MISSING: '规则缺少此报名类型的分润分项',
  PRACTICE_DUPLICATE_PAYMENT: '同一付款出现多次，请在来源核对报名和商品行',
  PRACTICE_MISSING_PAYMENT: '缺少对应付款，请在来源核对订单',
  PRACTICE_INVALID_PAYMENT: '付款金额或发生时间不符合签到计提条件',
  PRACTICE_ATTRIBUTION_CONFLICT: '订金与尾款的受益人归属不一致',
  PRACTICE_TIER_COMPANY_MISSING: '付款记录缺少销售公司',
  PRACTICE_TIER_COMPANY_CONFLICT: '订金和尾款的销售公司不一致',
  PRACTICE_COMPANY_RECIPIENT_MISSING: '付款时的收款公司映射缺失',
  PRACTICE_COMPANY_RECIPIENT_CONFLICT: '付款时的收款公司映射有冲突',
  PRACTICE_COMPANY_SCOPE_MISSING: '销售公司在本报名类型下没有适用标准',
  PRACTICE_COMPANY_AWARD_EXCEEDS_PAYMENT: '外部公司固定提成超过报名净实收',
  PRACTICE_RECOMMENDATION_RELATION_MISSING: '销售公司的推荐关系待核',
  PRACTICE_RECOMMENDATION_COMPANY_MISSING: '付款时的推荐公司映射缺失或公司已停用',
  PRACTICE_RECOMMENDATION_CONFLICT: '推荐公司归属冲突，请核对订金、尾款和公司关系',
  PRACTICE_RECOMMENDATION_ROUTE_MISSING: '推荐公司缺少适用的直收配置',
  PRACTICE_TIER_COMPANY_NOT_CONFIGURED: '该销售公司未配置阶梯',
  PRACTICE_TIER_OPENING_MISSING: '所选冻结期初中缺少该人员的期初人数',
  PRACTICE_TIER_HISTORY_MISSING: '累计起点或历史期次缺少已确认名单',
  PRACTICE_TIER_HISTORY_INCOMPLETE: '历史新报的付款或人员归属待核',
  PRACTICE_TIER_GROUP_INCOMPLETE: '本期新报的付款或人员归属待核，暂无法选档',
  PRACTICE_TIER_NOT_MATCHED: '本笔没有匹配的累计人数档或实付档',
  PRACTICE_TIER_EXCEEDS_PAYMENT: '命中的档位计提超过实付金额',
  PRACTICE_PERIOD_APPOINTMENT_MISSING: '找不到分项指定的组织任职记录',
  PRACTICE_PERIOD_BENEFIT_INCOMPLETE: '适用报名或付款待核，暂未汇总此分项',
  PRACTICE_PERIOD_BENEFIT_TIER_MISSING: '净实收金额没有匹配的班次档位',
}
const lineColumns = [
  { title: '原订单', key: 'order', width: 190 },
  { title: '受益对象', key: 'beneficiary', width: 135 },
  { title: '分项', key: 'component', width: 115 },
  { title: '规则版本', key: 'rule', width: 100 },
  { title: '实付基数', key: 'base', width: 115 },
  { title: '应计提成', key: 'amount', width: 115 },
]
const refundColumns = [
  { title: '原订单', key: 'order', width: 190 },
  { title: '原受益对象', key: 'beneficiary', width: 140 },
  { title: '原结算账期', dataIndex: 'originalPeriod', width: 135 },
  { title: '退款归属期', dataIndex: 'refundPeriod', width: 135 },
  { title: '实际扣减期', dataIndex: 'postingPeriod', width: 135 },
  { title: '退款扣减', key: 'amount', width: 115 },
]
const correctionColumns = [
  { title:'原订单', key:'order', width:180 },
  { title:'原受益人', dataIndex:'originalPersonName', width:130 },
  { title:'调整人员', dataIndex:'personName', width:130 },
  { title:'原结算期', dataIndex:'originalPeriod', width:130 },
  { title:'调整金额', key:'amount', width:140 },
]
const issueColumns = [
  { title: '原订单或报名', key: 'source', width: 160 },
  { title: '分项', key: 'component', width: 110 },
  { title: '待核原因', key: 'reason', width: 240 },
]

async function load(replace = false) {
  const request = ++sequence
  if (replace) draft.value = undefined
  error.value = ''
  if (!selected.value) {
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await props.api('/drafts?periodId=' + selected.value)
    if (request === sequence) draft.value = result
  } catch (err) {
    if (request === sequence && err.status !== 404) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}
async function checkRefresh() {
  clearTimeout(statusTimer)
  const request = ++statusSequence
  const period = selected.value
  if (!period || matchingOnly.value || draft.value?.settlement || document.visibilityState === 'hidden') return
  try {
    const result = await props.api('/drafts/status?periodId=' + period)
    if (request !== statusSequence || unmounted) return
    refreshStatus.value = result
    if (!loading.value && !calculating.value && (result.settled || (newerDraft.value && view.value !== 'pools'))) await load()
  } catch {
    // The current readable draft stays on screen if the status query fails.
  } finally {
    if (request === statusSequence && !unmounted && !draft.value?.settlement && document.visibilityState !== 'hidden') {
      statusTimer = setTimeout(checkRefresh, 15000)
    }
  }
}

function visibilityChanged() {
  if (document.visibilityState === 'hidden') clearTimeout(statusTimer)
  else checkRefresh()
}
onMounted(() => document.addEventListener('visibilitychange', visibilityChanged))
onBeforeUnmount(() => {
  unmounted = true
  clearTimeout(statusTimer)
  document.removeEventListener('visibilitychange', visibilityChanged)
})
watch(selected, async () => {
  clearTimeout(statusTimer)
  const request = ++statusSequence
  refreshStatus.value = undefined
  view.value = 'detail'
  await load(true)
  if (request === statusSequence) checkRefresh()
})
watch(options, (values) => {
  if (selected.value && !values.some(option => option.value === selected.value)) selected.value = undefined
})

async function calculate() {
  if (!selected.value || calculating.value) return
  const periodId = selected.value
  calculating.value = true
  error.value = ''
  try {
    await props.api('/drafts', { method: 'POST', body: JSON.stringify({ periodId }) })
    if (selected.value === periodId) {
      await load()
      await checkRefresh()
    }
  } catch (err) {
    error.value = err.message
  } finally {
    calculating.value = false
  }
}

async function loadMore(section, cursorName) {
  if (loading.value || calculating.value) return
  const request = sequence
  const current = draft.value
  const cursor = current[section].at(-1).id
  const params = new URLSearchParams({ periodId: selected.value, version: current.version, [cursorName]: cursor })
  loading.value = true
  error.value = ''
  try {
    const next = await props.api('/drafts?' + params)
    if (request === sequence) current[section].push(...next[section])
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}
</script>

<template>
  <section class="heading">
    <div><h1>分润试算</h1><p>选择核算期，查看应计金额、订单版本和待核项。</p></div>
  </section>
  <section class="records draft-controls">
    <div class="draft-toolbar">
      <label for="draft-period">产品和核算期</label>
      <a-select id="draft-period" v-model:value="selected" :options="options" placeholder="选择产品和核算期" :disabled="calculating" />
      <a-button v-if="!matchingOnly" type="primary" :disabled="!selected || loading || !!draft?.settlement" :loading="calculating" @click="calculate">重新试算</a-button>
      <a-button v-if="!matchingOnly" :disabled="!selected || loading || calculating" @click="load()">刷新结果</a-button>
    </div>
    <p v-if="selectedPeriod" class="draft-context">
      {{ selectedPeriod.periodMode === 'class' ? '期次' : '账期' }} {{ selectedPeriod.startsOn }} 至 {{ selectedPeriod.endsOn }}
      <span v-if="draft?.practiceRosterVersion"> · 签到名单 v{{ draft.practiceRosterVersion }} · 到课 {{ draft.attendanceCount }} 人</span>
      <span v-if="draft?.settlement"> · 已结算 · {{ formatTime(draft.settlement.confirmedAt) }}</span>
      <span v-else-if="draft"> · 草稿 v{{ draft.version }} · {{ formatTime(draft.calculatedAt) }}</span>
      <span v-if="!draft?.settlement && refreshStatus?.pending && !refreshStatus.message"> · 有变更待试算</span>
      <span v-if="view==='pools' && newerDraft"> · 草稿已更新，请刷新结果后核对分配</span>
    </p>
  </section>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon />
  <a-alert v-if="!draft?.settlement && refreshStatus?.message" class="feedback" type="warning" :message="refreshStatus.message" show-icon/>
  <div v-if="draft" class="summary-grid" :class="{ 'summary-with-corrections':draft.correctionCount, 'summary-collection':!draft.refundsReady }">
    <article><div><small>{{ draft.practiceRosterVersion ? '本班计提（含名单确认前退款）' : '正向计提' }}</small><strong>{{ formatMoney(draft.accruedMinor) }}</strong></div></article>
    <article v-if="!draft.practiceRosterVersion"><div><small>退款扣减</small><strong>{{ draft.refundsReady ? formatMoney(draft.refundMinor) : '待重新试算' }}</strong></div></article>
    <article v-if="draft.correctionCount"><div><small>推荐人更正</small><strong>{{ formatMoney(draft.correctionMinor) }}</strong></div></article>
    <article v-if="draft.refundsReady"><div><small>本期合计</small><strong>{{ formatMoney(draft.totalMinor) }}</strong></div></article>
  </div>
  <a-tabs v-if="draft" v-model:active-key="view">
    <a-tab-pane key="detail" tab="明细"/>
    <a-tab-pane v-if="reportDimensions.length" key="report" tab="业绩与分润"/>
    <a-tab-pane v-if="draft.catalog && draft.refundsReady" key="pools" tab="团队池分配"/>
    <a-tab-pane v-if="draft.catalog && draft.refundsReady" key="review" tab="结算核对"/>
    <a-tab-pane v-if="hasMatching" key="matching" tab="订单规则匹配"/>
  </a-tabs>
  <DraftReport v-if="draft && view==='report' && reportDimensions.length" :draft="draft" :dimensions="reportDimensions" :api="api" :format-money="formatMoney"/>
  <DraftPoolAllocations v-if="draft?.catalog && view==='pools'" :draft="draft" :api="api" :format-money="formatMoney"/>
  <DraftSettlementReview v-if="draft?.catalog && view==='review'" :draft="draft" :period="selectedPeriod" :api="api" :format-money="formatMoney"
    :format-time="formatTime" :source-streams="sourceStreams" @show-pools="view='pools'" @show-issues="view='detail'" @confirmed="load()"/>
  <template v-if="draft && view==='detail'">
  <section class="records">
    <div class="records-head"><h2>{{ draft.settlement ? '结算明细' : '计提明细' }}</h2></div>
    <div class="table-container">
      <a-table :columns="lineColumns" :data-source="draft.lines" :pagination="false" row-key="id" :scroll="{ x: 755 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">{{ record.orderNo || record.orderId }}<small class="order-version">来源版本 {{ record.sourceVersion }}<template v-if="record.enrollmentId"> · {{ memberLabel(record.membershipType) }} {{ record.enrollmentId }}</template><template v-if="record.salesCompany"> · {{ record.salesCompany }}</template></small></template>
          <template v-else-if="column.key === 'beneficiary'">
            {{ record.personName }}<small v-if="record.beneficiaryKind==='pool'" class="order-version">团队池 · 待分配</small>
            <small v-else-if="record.beneficiaryKind==='company'" class="order-version">{{ record.beneficiaryUnitKind==='DEPARTMENT' ? '部门分润' : '公司分润' }}</small>
          </template>
          <template v-else-if="column.key === 'component'">{{ salesRoles[record.componentRole] || record.componentLabel || componentLabel(record.component) }}</template>
          <template v-else-if="column.key === 'rule'">v{{ record.ruleVersion }}<small v-if="draft.catalog && record.ruleLabel" class="order-version">{{ record.ruleLabel }}</small></template>
          <template v-else-if="column.key === 'base'">
            {{ formatMoney(record.baseMinor) }}<small v-if="record.classRefundMinor>0" class="order-version">已扣退款 {{ formatMoney(record.classRefundMinor) }}</small>
          </template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
        <template #emptyText><a-empty description="本期没有可计提明细" /></template>
      </a-table>
    </div>
    <div v-if="draft.lineCount > draft.lines.length" class="paging"><a-button :loading="loading" @click="loadMore('lines', 'afterLineId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft?.refundCount" class="records draft-issues">
    <div class="records-head"><h2>退款扣减明细</h2></div>
    <div class="table-container">
      <a-table :columns="refundColumns" :data-source="draft.refunds" :pagination="false" row-key="id" :scroll="{ x: 840 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">
            {{ record.originalOrderNo || record.originalOrderId }}
            <small class="order-version">计提时规则 v{{ record.ruleVersion }} · 退款 {{ record.refundId }}</small>
          </template>
          <template v-else-if="column.key === 'beneficiary'">
            {{ record.personName }}<small v-if="record.beneficiaryKind==='pool'" class="order-version">团队池</small>
            <small v-else-if="record.beneficiaryKind==='company'" class="order-version">公司分润</small>
          </template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
      </a-table>
    </div>
    <div v-if="draft.refundCount > draft.refunds.length" class="paging"><a-button :loading="loading" @click="loadMore('refunds', 'afterRefundId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft?.correctionCount" class="records draft-issues">
    <div class="records-head"><h2>推荐人更正明细</h2></div>
    <div class="table-container">
      <a-table :columns="correctionColumns" :data-source="draft.corrections" :pagination="false" row-key="id" :scroll="{x:710}">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='order'">
            {{ record.orderNo || record.orderId }}
            <small class="order-version">结算时规则 v{{ record.ruleVersion }} · 订单版本 {{ record.sourceVersion }}</small>
          </template>
          <template v-else-if="column.key==='amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
      </a-table>
    </div>
    <div v-if="draft.correctionCount > draft.corrections.length" class="paging"><a-button :loading="loading" @click="loadMore('corrections','afterCorrectionId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft?.issueCount" class="records draft-issues">
    <div class="records-head"><h2>待核项</h2></div>
    <div class="table-container">
      <a-table :columns="issueColumns" :data-source="draft.issues" :pagination="false" row-key="id" :scroll="{ x: 510 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'source'">{{ record.orderId || '—' }}<small v-if="record.enrollmentId" class="order-version">报名 {{ record.enrollmentId }}</small></template>
          <template v-else-if="column.key === 'component'">{{ record.componentLabel || componentLabel(record.component) || '—' }}</template>
          <template v-else-if="column.key === 'reason'">{{ reason[record.code] || record.code }}</template>
        </template>
      </a-table>
    </div>
    <div v-if="draft.issueCount > draft.issues.length" class="paging"><a-button :loading="loading" @click="loadMore('issues', 'afterIssueId')">加载下一页</a-button></div>
  </section>
  </template>
  <CatalogRuleMatches v-if="hasMatching && (!draft || view==='matching')" :period-id="selected" :draft-available="selectedPeriod.catalogDraftAvailable"
    :api="api" :format-money="formatMoney" :format-time="formatTime"/>
  <section v-if="!draft && !error && !hasMatching" class="records empty">
    <a-empty :description="loading ? '正在读取试算' : selected ? '本期暂无草稿' : '请选择核算期'" />
  </section>
</template>

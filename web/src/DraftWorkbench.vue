<script setup>
import { computed, ref, watch } from 'vue'

const props = defineProps({ periods: Array, api: Function, formatMoney: Function, formatTime: Function })
const selected = ref()
const draft = ref()
const loading = ref(false)
const calculating = ref(false)
const error = ref('')
let sequence = 0

const options = computed(() => props.periods.map(period => ({
  value: period.id,
  label: `${period.productName} · ${period.name}`,
})))
const selectedPeriod = computed(() => props.periods.find(period => period.id === selected.value))
const components = { sales: '销售提成', referral: '推荐提成', manager: '负责人提成', delivery: '交付提成' }
const reason = {
  UNCONFIGURED_COMPONENT: '此分项尚未配置可用的来源角色或计算方式',
  MISSING_PAYMENT_ID: '缺少原付款 ID',
  DUPLICATE_PAYMENT: '同一付款出现多条记录，需核对商品行及金额分摊',
  MISSING_PAID_AMOUNT: '缺少有效的实付金额',
  MISSING_EFFECTIVE_RULE: '付款时点没有有效规则',
  AMBIGUOUS_PARTICIPANT: '同一来源角色有多名受益人',
  MISSING_PARTICIPANT: '订单缺少该来源角色',
  AMBIGUOUS_IDENTITY: '付款时点命中多份人员归属',
  MISSING_IDENTITY: '付款时点查不到该来源人员的归属',
  REFUND_MISSING_AMOUNT_OR_ID: '退款缺少有效退款编号或金额',
  REFUND_DUPLICATE_ID: '同一原退款编号有多条来源事实',
  REFUND_MISSING_PAYMENT_REFERENCE: '退款未明确关联原付款',
  REFUND_ORIGINAL_NOT_SETTLED: '找不到可对应的原付款提成',
  REFUND_PAYMENT_AMBIGUOUS: '退款匹配到多笔原付款',
  REFUND_AMOUNT_EXCEEDS_PAYMENT: '累计退款或扣回金额超过原付款或原计提',
  REFUND_POLICY_MISSING: '原结算规则缺少退款处理口径',
  REFUND_BOOKED_CHANGED: '已入账退款金额或关联有修订，需核对后办理更正',
}
const lineColumns = [
  { title: '原订单', key: 'order', width: 190 },
  { title: '受益人', dataIndex: 'personName', width: 120 },
  { title: '分项', key: 'component', width: 115 },
  { title: '规则版本', key: 'rule', width: 100 },
  { title: '实付基数', key: 'base', width: 115 },
  { title: '应计提成', key: 'amount', width: 115 },
]
const refundColumns = [
  { title: '原订单', key: 'order', width: 190 },
  { title: '原受益人', dataIndex: 'personName', width: 130 },
  { title: '原结算账期', dataIndex: 'originalPeriod', width: 135 },
  { title: '退款归属期', dataIndex: 'refundPeriod', width: 135 },
  { title: '实际扣减期', dataIndex: 'postingPeriod', width: 135 },
  { title: '退款扣减', key: 'amount', width: 115 },
]
const issueColumns = [
  { title: '原订单 ID', dataIndex: 'orderId', width: 160 },
  { title: '分项', key: 'component', width: 110 },
  { title: '待核原因', key: 'reason', width: 240 },
]

async function load(replace = false) {
  const request = ++sequence
  if (replace) draft.value = undefined
  error.value = ''
  if (!selected.value) return
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
watch(selected, () => load(true))

async function calculate() {
  if (!selected.value || calculating.value) return
  const periodId = selected.value
  calculating.value = true
  error.value = ''
  try {
    await props.api('/drafts', { method: 'POST', body: JSON.stringify({ periodId }) })
    if (selected.value === periodId) await load()
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
    <div><h1>分润试算</h1><p>选择固定账期，查看应计金额、订单版本和待核项。</p></div>
  </section>
  <section class="records draft-controls">
    <div class="draft-toolbar">
      <label for="draft-period">产品和账期</label>
      <a-select id="draft-period" v-model:value="selected" :options="options" placeholder="选择产品和账期" :disabled="calculating" />
      <a-button type="primary" :disabled="!selected || loading || !!draft?.settlement" :loading="calculating" @click="calculate">重新试算</a-button>
      <a-button :disabled="!selected || loading || calculating" @click="load()">刷新结果</a-button>
    </div>
    <p v-if="selectedPeriod" class="draft-context">
      账期 {{ selectedPeriod.startsOn }} 至 {{ selectedPeriod.endsOn }}
      <span v-if="draft?.settlement"> · 已结算 · {{ formatTime(draft.settlement.confirmedAt) }}</span>
      <span v-else-if="draft"> · 草稿 v{{ draft.version }} · {{ formatTime(draft.calculatedAt) }}</span>
    </p>
  </section>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon />
  <div v-if="draft" class="summary-grid">
    <article><div><small>正向计提</small><strong>{{ formatMoney(draft.accruedMinor) }}</strong></div></article>
    <article><div><small>退款扣减</small><strong>{{ formatMoney(draft.refundMinor) }}</strong></div></article>
    <article><div><small>本期合计</small><strong>{{ formatMoney(draft.totalMinor) }}</strong></div></article>
  </div>
  <section v-if="draft" class="records">
    <div class="records-head"><h2>{{ draft.settlement ? '结算明细' : '计提明细' }}</h2></div>
    <div class="table-container">
      <a-table :columns="lineColumns" :data-source="draft.lines" :pagination="false" row-key="id" :scroll="{ x: 755 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">{{ record.orderNo || record.orderId }}<small class="order-version">来源版本 {{ record.sourceVersion }}</small></template>
          <template v-else-if="column.key === 'component'">{{ components[record.component] }}</template>
          <template v-else-if="column.key === 'rule'">v{{ record.ruleVersion }}</template>
          <template v-else-if="column.key === 'base'">{{ formatMoney(record.baseMinor) }}</template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
        <template #emptyText><a-empty description="当前账期没有可计提明细" /></template>
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
            <small class="order-version">原规则 v{{ record.ruleVersion }} · 退款 {{ record.refundId }}</small>
          </template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
      </a-table>
    </div>
    <div v-if="draft.refundCount > draft.refunds.length" class="paging"><a-button :loading="loading" @click="loadMore('refunds', 'afterRefundId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft?.issueCount" class="records draft-issues">
    <div class="records-head"><h2>待核项</h2></div>
    <div class="table-container">
      <a-table :columns="issueColumns" :data-source="draft.issues" :pagination="false" row-key="id" :scroll="{ x: 510 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'component'">{{ components[record.component] || '—' }}</template>
          <template v-else-if="column.key === 'reason'">{{ reason[record.code] || record.code }}</template>
        </template>
      </a-table>
    </div>
    <div v-if="draft.issueCount > draft.issues.length" class="paging"><a-button :loading="loading" @click="loadMore('issues', 'afterIssueId')">加载下一页</a-button></div>
  </section>
  <section v-if="!draft && !error" class="records empty">
    <a-empty :description="loading ? '正在读取试算' : selected ? '此账期暂无草稿' : '请选择固定账期'" />
  </section>
</template>

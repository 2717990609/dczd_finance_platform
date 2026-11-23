<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'
import CatalogRuleMatches from './CatalogRuleMatches.vue'
import DraftReport from './DraftReport.vue'
import DraftPoolAllocations from './DraftPoolAllocations.vue'
import DraftSettlementReview from './DraftSettlementReview.vue'
import DraftAggregateOrders from './DraftAggregateOrders.vue'
import { salesRoles } from './catalog-labels'
import { draftIssueLabels } from './draft-issue-labels'

const props = defineProps({ periods: Array, componentOptions: Array, memberOptions: Array, reportDimensions: Array, sourceStreams: Array,
  recoveryOptions: Array, settlementProducts: Array, api: Function, formatMoney: Function, formatTime: Function, unitId: String,
  requestedPeriod:Number, requestedView:String, ruleSetupLink:String, issueRuleLink:String, relatedOrderLink:Function, relatedPeriodLink:Function,
  periodsLoading:Boolean, month:String, rosterEditReady:Boolean })
const emit = defineEmits(['show-order', 'show-issue-order', 'show-applied-rule', 'show-roster'])
const selected = defineModel('periodId', {type:Number})
const view = defineModel('view', {type:String})
const detailQuery = defineModel('detailQuery', {type:Object})
const draft = ref()
const aggregateGroup = ref()
const loading = ref(false)
const calculating = ref(false)
const pullingRoster = ref(false)
const notice = ref('')
const recovery = ref()
const recoveryOutcome = ref()
const recoveryError = ref('')
const savingRecovery = ref(false)
const error = ref('')
const refreshStatus = ref()
const orderInput = ref(detailQuery.value.orderQuery || '')
const beneficiaryInput = ref(detailQuery.value.beneficiaryQuery || '')
const hasDetailFilter = computed(() => Boolean(detailQuery.value.orderQuery || detailQuery.value.beneficiaryQuery))
let sequence = 0
let statusSequence = 0
let statusTimer
let unmounted = false

const options = computed(() => props.periods.map(period => ({
  value: period.id,
  label: `${period.productName} · ${period.name}`,
})))
const selectedPeriod = computed(() => props.periods.find(period => period.id === selected.value))
const linkedPeriod = computed(() => props.periods.find(period => period.id === props.requestedPeriod)?.id)
const canPullRoster = computed(() => selectedPeriod.value?.periodMode === 'class' && selectedPeriod.value.sourcePeriodKey && !draft.value?.settlement)
const needsRosterEdit = computed(() => selectedPeriod.value?.practiceRosterEditable &&
  (!selectedPeriod.value.sourcePeriodKey || selectedPeriod.value.classStartOutsidePeriod))
const canReviewRecovery = computed(() => !draft.value?.settlement && props.recoveryOptions?.length && props.settlementProducts?.includes(selectedPeriod.value?.productId))
const hasMatching = computed(() => !!selectedPeriod.value?.ruleMatchingAvailable && !draft.value?.settlement)
const hasSettlementReview = computed(() => draft.value?.settlementReviewAvailable ?? (draft.value?.catalog && draft.value?.refundsReady))
const matchingOnly = computed(() => hasMatching.value && !selectedPeriod.value?.catalogDraftAvailable)
const newerDraft = computed(() => refreshStatus.value?.version > (draft.value?.version || 0))
const componentLabel = code => {
  const type = code?.startsWith('period:') ? code.split(':')[1] : code
  if (type==='company_referral') return '公司推荐分润'
  if (type==='retrain_service') return '复训服务分润'
  if (type==='retrain_pool') return '复训销售归集'
  return props.componentOptions.find(option=>option.value===type)?.label || type
}
const memberLabel = type => props.memberOptions.find(option=>option.value===(type===3 ? 'new' : 'retrain'))?.label
function showAppliedRule(section, record) {
  emit('show-applied-rule', {periodId:selected.value, section, rowId:record.id})
}
const lineColumns = computed(() => [
  { title: draft.value?.aggregatePackageId ? '计提范围' : '原订单', key: 'order', width: 190 },
  { title: '受益对象', key: 'beneficiary', width: 135 },
  { title: '分项', key: 'component', width: 115 },
  { title: '规则版本', key: 'rule', width: 100 },
  { title: draft.value?.aggregatePackageId ? '可计提净额' : '实付基数', key: 'base', width: 115, align:'right' },
  { title: '应计提成', key: 'amount', width: 115, align:'right' },
])
watch(()=>[draft.value?.periodId,draft.value?.version],()=>{aggregateGroup.value=undefined})
const refundColumns = computed(() => [
  { title: '原订单', key: 'order', width: 190 },
  { title: '原受益对象', key: 'beneficiary', width: 140 },
  { title: '原结算账期', dataIndex: 'originalPeriod', width: 135 },
  { title: '退款归属期', dataIndex: 'refundPeriod', width: 135 },
  { title: '实际归账期', dataIndex: 'postingPeriod', width: 135 },
  { title: draft.value?.practiceRefundMonth ? '调整金额' : '退款扣减', key: 'amount', width: 115 },
])
const correctionColumns = [
  { title:'原订单', key:'order', width:180 },
  { title:'原受益对象', dataIndex:'originalPersonName', width:130 },
  { title:'调整对象', dataIndex:'personName', width:130 },
  { title:'原结算期', dataIndex:'originalPeriod', width:130 },
  { title:'调整金额', key:'amount', width:140 },
]
const issueColumns = [
  { title: '原订单或报名', key: 'source', width: 160 },
  { title: '分项', key: 'component', width: 110 },
  { title: '处理说明', key: 'reason', width: 240 },
]
const ruleIssues = new Set(['RULE_MISSING','RULE_CONFLICT','MISSING_EFFECTIVE_RULE','UNCONFIGURED_COMPONENT',
  'PRACTICE_MEMBER_RULE_MISSING','PRACTICE_TIER_COMPANY_NOT_CONFIGURED','PRACTICE_PERIOD_BENEFIT_TIER_MISSING'])

function appendDetailFilters(params) {
  for (const [name,value] of Object.entries(detailQuery.value)) {
    if (value) params.set(name,value)
  }
}
async function searchDetails() {
  detailQuery.value = {orderQuery:orderInput.value.trim(),beneficiaryQuery:beneficiaryInput.value.trim()}
  await nextTick()
  load()
}
function clearDetailInputs() {
  orderInput.value = ''
  beneficiaryInput.value = ''
  detailQuery.value = {}
}
async function resetDetails() {
  clearDetailInputs()
  await nextTick()
  load()
}
function hasMoreDetails(section,total) {
  return draft.value.detailHasMore?.[section] ?? total>draft.value[section].length
}

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
    const params = new URLSearchParams({periodId:selected.value})
    if (props.unitId) params.set('unitId',props.unitId)
    appendDetailFilters(params)
    const result = await props.api('/drafts?' + params)
    if (request === sequence) {
      draft.value = result
      if (view.value==='matching' && !hasMatching.value || view.value==='review' && !hasSettlementReview.value ||
          view.value==='pools' && !result.poolAllocationAvailable) view.value = 'detail'
    }
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
    const metadataChanged = result.calculatedAt && result.calculatedAt !== draft.value?.calculatedAt
    if (!loading.value && !calculating.value && (result.settled || ((newerDraft.value || metadataChanged) && view.value !== 'pools'))) await load()
  } catch {
    // The current readable draft stays on screen if the status query fails.
  } finally {
    if (request === statusSequence && !unmounted && !draft.value?.settlement && document.visibilityState !== 'hidden') {
      statusTimer = setTimeout(checkRefresh, 15000)
    }
  }
}
defineExpose({ checkRefresh })

function visibilityChanged() {
  if (document.visibilityState === 'hidden') clearTimeout(statusTimer)
  else checkRefresh()
}
onMounted(() => document.addEventListener('visibilitychange', visibilityChanged))
onBeforeUnmount(() => {
  unmounted = true
  sequence++
  clearTimeout(statusTimer)
  document.removeEventListener('visibilitychange', visibilityChanged)
})
watch(selected, async (period, previous) => {
  clearTimeout(statusTimer)
  const request = ++statusSequence
  refreshStatus.value = undefined
  notice.value = ''
  recovery.value = undefined
  if (previous !== undefined) {
    clearDetailInputs()
    view.value = period===props.requestedPeriod && props.requestedView || 'detail'
    await nextTick()
    if (request !== statusSequence || unmounted) return
  }
  await load(true)
  if (request === statusSequence) checkRefresh()
}, {immediate:true})
watch(options, (values) => {
  if (!values.some(option => option.value === selected.value)) {
    selected.value = values.length === 1 ? values[0].value : undefined
  }
}, {immediate:true})
watch([linkedPeriod, () => props.requestedView], ([period, requestedView]) => {
  if (period) {
    selected.value = period
    if (requestedView) view.value = requestedView
  }
}, {immediate:true})
watch(() => props.unitId, () => load())

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

async function pullRoster() {
  const periodId = selected.value
  if (!canPullRoster.value || pullingRoster.value || calculating.value) return
  pullingRoster.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await props.api('/practice-rosters/pull', { method: 'POST', body: JSON.stringify({ periodId }) })
    if (selected.value !== periodId || unmounted) return
    notice.value = result.changed ? `已读取签到名单 v${result.version}，可重新试算。` : `签到名单已是 v${result.version}。`
    if (result.changed && view.value === 'review') await load()
    await checkRefresh()
  } catch (err) {
    if (selected.value === periodId && !unmounted) error.value = err.message
  } finally {
    pullingRoster.value = false
  }
}

function openRecovery(record) {
  recoveryOutcome.value = undefined
  recoveryError.value = ''
  recovery.value = { ...record.recovery, periodId: selected.value, version: draft.value.version, issueId: record.id }
}

async function saveRecovery() {
  if (!recovery.value || !recoveryOutcome.value || savingRecovery.value) return
  const { periodId, version, issueId } = recovery.value
  savingRecovery.value = true
  recoveryError.value = ''
  notice.value = ''
  try {
    await props.api('/correction-recovery', {method:'POST', body:JSON.stringify({periodId, version, issueId, outcome:recoveryOutcome.value})})
    if (selected.value !== periodId || unmounted) return
    recovery.value = undefined
    notice.value = '扣回处理已登记。'
    await calculate()
  } catch (err) {
    if (selected.value === periodId && !unmounted) recoveryError.value = err.message
  } finally {
    savingRecovery.value = false
  }
}

async function loadMore(section, cursorName) {
  if (loading.value || calculating.value) return
  const request = sequence
  const current = draft.value
  const cursor = current[section].at(-1).id
  const params = new URLSearchParams({ periodId: selected.value, version: current.version, [cursorName]: cursor })
  if (props.unitId) params.set('unitId',props.unitId)
  appendDetailFilters(params)
  loading.value = true
  error.value = ''
  try {
    const next = await props.api('/drafts?' + params)
    if (request === sequence) {
      if (section === 'issues' && next.calculatedAt !== current.calculatedAt) await load()
      else {
        current[section].push(...next[section])
        if (current.detailHasMore && next.detailHasMore && section!=='issues') current.detailHasMore[section] = next.detailHasMore[section]
      }
    }
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}
</script>

<template>
  <section class="records workbench-controls" aria-label="核算操作">
    <div class="workbench-selection">
      <div class="workbench-toolbar">
        <div class="workbench-period">
          <label for="draft-period">产品和核算期</label>
          <a-select id="draft-period" v-model:value="selected" :options="options" placeholder="选择产品和核算期" :loading="periodsLoading"
            show-search option-filter-prop="label" :disabled="calculating || pullingRoster || savingRecovery" />
        </div>
        <div v-if="selected" class="workbench-actions">
          <a-button v-if="needsRosterEdit" :disabled="!rosterEditReady || loading || pullingRoster || calculating"
            @click="emit('show-roster',selectedPeriod)">{{ selectedPeriod.sourcePeriodKey ? '更正签到关联' : '关联签到名单' }}</a-button>
          <a-button v-if="canPullRoster && (view!=='review' || !draft)" :disabled="loading || calculating" :loading="pullingRoster" @click="pullRoster">拉取签到名单</a-button>
          <a-button v-if="!matchingOnly && !draft?.settlement && (view!=='review' || !draft)" :disabled="loading || pullingRoster" :loading="calculating"
            :type="draft ? 'default' : 'primary'" @click="calculate">{{ draft ? '重新试算' : '生成草稿' }}</a-button>
          <a-button v-if="!matchingOnly && (view!=='review' || !draft)" :disabled="loading || calculating || pullingRoster" @click="load()">刷新结果</a-button>
          <a-button v-if="hasSettlementReview && !draft?.settlement && view!=='review'" type="primary" :disabled="loading || calculating || pullingRoster" @click="view='review'">去结算核对</a-button>
        </div>
      </div>
      <p v-if="selectedPeriod" class="draft-context">
        <a-tag :color="draft?.settlement ? 'green' : draft ? 'blue' : 'default'">{{ draft?.settlement ? '已结算' : draft ? '草稿 v'+draft.version : '未试算' }}</a-tag>
        <strong class="mobile-period-name">{{ selectedPeriod.name }}</strong>
        {{ selectedPeriod.startsOn }} 至 {{ selectedPeriod.endsOn }}
        <span v-if="draft?.practiceRosterVersion"> · 签到名单 v{{ draft.practiceRosterVersion }} · 到课 {{ draft.attendanceCount }} 人</span>
        <span v-if="draft?.settlement"> · 确认于 {{ formatTime(draft.settlement.confirmedAt) }}</span>
        <span v-else-if="draft"> · 试算于 {{ formatTime(draft.calculatedAt) }}</span>
        <span v-if="!draft?.settlement && refreshStatus?.pending && !refreshStatus.message"> · 有变更待试算</span>
        <span v-if="view==='pools' && newerDraft"> · 草稿已更新，请刷新结果后核对分配</span>
      </p>
    </div>
    <div v-if="draft" class="workbench-totals">
      <div v-if="draft.refundsReady || draft.aggregatePackageId" class="workbench-net">
        <small>{{ draft.aggregatePackageId ? '整期计提' : draft.practiceRosterVersion ? (draft.issueCount ? '已核算净分润' : '本班净分润') : '本期净分润' }}</small><strong>{{ formatMoney(draft.totalMinor) }}</strong>
      </div>
      <div v-if="!draft.aggregatePackageId && !draft.practiceRefundMonth && (!draft.practiceRosterVersion || draft.correctionCount || !draft.refundsReady)"><small>计提分润</small><strong>{{ formatMoney(draft.accruedMinor) }}</strong></div>
      <div v-if="!draft.aggregatePackageId && !draft.practiceRosterVersion"><small>{{ draft.practiceRefundMonth ? '退款调整' : '退款扣减' }}</small><strong>{{ draft.refundsReady ? formatMoney(draft.refundMinor) : '待重新试算' }}</strong></div>
      <div v-if="draft.correctionCount"><small>受益人更正</small><strong>{{ formatMoney(draft.correctionMinor) }}</strong></div>
      <div><small>{{ draft.practiceRefundMonth ? '退款及更正' : '计提明细' }}</small><strong>{{ draft.practiceRefundMonth ? draft.refundCount + draft.correctionCount : draft.lineCount }} <span>条</span></strong></div>
    </div>
    <a-tabs v-if="draft" v-model:active-key="view" :tab-bar-gutter="20" class="workbench-tabs">
      <a-tab-pane key="detail" tab="分润明细"/>
      <a-tab-pane v-if="reportDimensions.length" key="report" tab="业绩与分润"/>
      <a-tab-pane key="issues" :tab="draft.issueCount ? '待核 '+draft.issueCount : draft.advisoryCount ? '处理提示 '+draft.advisoryCount : '待核'"/>
      <a-tab-pane v-if="draft.poolAllocationAvailable" key="pools" tab="团队池分配"/>
      <a-tab-pane v-if="hasSettlementReview" key="review" tab="结算核对"/>
      <a-tab-pane v-if="hasMatching" key="matching" tab="订单规则匹配"/>
    </a-tabs>
  </section>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon />
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon />
  <a-alert v-if="!draft?.settlement && refreshStatus?.message" class="feedback" type="warning" :message="refreshStatus.message" show-icon/>
  <p v-if="unitId && draft" class="workbench-scope-note">明细与汇总按组织筛选，合计及结算核对为全期</p>
  <DraftReport v-if="draft && view==='report' && reportDimensions.length" :draft="draft" :dimensions="reportDimensions" :api="api" :format-money="formatMoney" :unit-id="unitId"/>
  <DraftPoolAllocations v-if="draft?.poolAllocationAvailable && view==='pools'" :draft="draft" :api="api" :format-money="formatMoney"/>
  <DraftSettlementReview v-if="hasSettlementReview && view==='review'" :draft="draft" :period="selectedPeriod" :api="api" :format-money="formatMoney"
    :format-time="formatTime" :source-streams="sourceStreams" :working="calculating || pullingRoster" :can-pull-roster="canPullRoster"
    :related-period-link="relatedPeriodLink"
    @show-pools="view='pools'" @show-issues="view='issues'" @recalculate="calculate" @pull-roster="pullRoster" @refresh-draft="load()" @confirmed="load()"/>
  <template v-if="draft && view==='detail'">
  <section v-if="draft.aggregateGroups?.length" class="records" aria-label="整期分组">
    <div class="records-head"><h2>整期分组</h2></div>
    <div class="table-container">
      <a-table :data-source="draft.aggregateGroups" row-key="key" :pagination="false" :scroll="{x:650}" :columns="[
        {title:'规则版本',key:'ruleVersion'},{title:'本期收款',key:'receiptsMinor'},
        {title:'本期退款',key:'refundsMinor'},{title:'上期转入退款',key:'carryInMinor'},
        {title:'转出到下期',key:'carryOutMinor'},{title:'明细',key:'events'}]">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='ruleVersion'">v{{ record.ruleVersion }}<small v-if="record.fixedOnly" class="order-version">整期固定</small></template>
          <button v-else-if="column.key==='events'" type="button" class="order-link" @click="aggregateGroup=record.key">查看收退款</button>
          <span v-else class="numeric">{{ formatMoney(Math.abs(record[column.key])) }}</span>
        </template>
      </a-table>
    </div>
  </section>
  <section v-if="draft.lineCount || draft.refundCount || draft.correctionCount"
    class="records draft-detail-search" aria-label="明细筛选">
    <form class="draft-detail-fields" @submit.prevent="searchDetails">
      <div>
        <label for="draft-order-search">原订单号</label>
        <a-input id="draft-order-search" v-model:value="orderInput" placeholder="订单号或来源订单 ID" :maxlength="128" allow-clear/>
      </div>
      <div>
        <label for="draft-beneficiary-search">受益对象</label>
        <a-input id="draft-beneficiary-search" v-model:value="beneficiaryInput" placeholder="人员、公司或团队池" :maxlength="128" allow-clear/>
      </div>
      <div class="draft-detail-actions">
        <a-button type="primary" html-type="submit" :disabled="calculating" :loading="loading && hasDetailFilter">查询</a-button>
        <a-button v-if="hasDetailFilter || orderInput || beneficiaryInput" :disabled="calculating" @click="resetDetails">清除</a-button>
      </div>
    </form>
    <p v-if="hasDetailFilter" class="draft-report-note">上方金额为全期汇总。</p>
  </section>
  <section v-if="!draft.practiceRefundMonth" class="records">
    <div class="records-head"><h2>{{ draft.settlement ? '结算明细' : '计提明细' }}</h2></div>
    <div class="table-container">
      <a-table :columns="lineColumns" :data-source="draft.lines" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: 755 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">
            <button v-if="record.aggregateGroupKey" type="button" class="order-link"
              @click="aggregateGroup=record.aggregateGroupKey">查看本组收退款</button>
            <template v-else>
            <button type="button" class="order-link" @click="emit('show-order', record.sourceVersionId)">{{ record.orderNo || record.orderId }}</button>
            <small class="order-version">订单版本 {{ record.sourceVersion }}<template v-if="record.enrollmentId"> · {{ memberLabel(record.membershipType) }} {{ record.enrollmentId }}</template><template v-if="record.salesCompany"> · {{ record.salesCompany }}</template></small>
            <small v-if="record.completedAt" class="order-version">完成 {{ formatTime(record.completedAt) }}<template v-if="record.completionCarried"> · 补入本期</template></small>
            </template>
          </template>
          <template v-else-if="column.key === 'beneficiary'">
            {{ record.personName }}<small v-if="record.beneficiaryKind==='pool'" class="order-version">团队池 · 待分配</small>
            <small v-else-if="record.beneficiaryKind==='company'" class="order-version">{{ record.beneficiaryUnitKind==='DEPARTMENT' ? '部门分润' : '公司分润' }}</small>
          </template>
          <template v-else-if="column.key === 'component'">{{ salesRoles[record.componentRole] || record.componentLabel || componentLabel(record.component) }}</template>
          <template v-else-if="column.key === 'rule'">
            <button v-if="record.ruleVersion" type="button" class="order-link" :aria-label="'查看规则 v'+record.ruleVersion"
              @click="showAppliedRule('lines',record)">v{{ record.ruleVersion }}</button>
            <span v-else>—</span>
            <small v-if="record.ruleLabel" class="order-version rule-reference" :title="record.ruleLabel">{{ record.ruleLabel }}</small>
          </template>
          <template v-else-if="column.key === 'base'">
            {{ formatMoney(record.baseMinor) }}<small v-if="record.classRefundMinor>0" class="order-version">已扣退款 {{ formatMoney(record.classRefundMinor) }}</small>
          </template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
        <template #emptyText><a-empty :description="hasDetailFilter ? '未找到符合条件的计提明细' : '本期没有可计提明细'" /></template>
      </a-table>
    </div>
    <div v-if="hasMoreDetails('lines', draft.scopeLineCount ?? draft.lineCount)" class="paging"><a-button :loading="loading" @click="loadMore('lines', 'afterLineId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft.scopeRefundCount ?? draft.refundCount" class="records draft-issues">
    <div class="records-head"><h2>{{ draft.practiceRefundMonth ? '退款调整明细' : '退款扣减明细' }}</h2></div>
    <div class="table-container">
      <a-table :columns="refundColumns" :data-source="draft.refunds" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: 840 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">
            <button type="button" class="order-link" @click="emit('show-order', record.sourceVersionId)">{{ record.originalOrderNo || record.originalOrderId }}</button>
            <small class="order-version">
              <button v-if="record.ruleVersion" type="button" class="order-link" @click="showAppliedRule('refunds',record)">计提时规则 v{{ record.ruleVersion }}</button>
              <template v-if="record.ruleLabel"> · {{ record.ruleLabel }}</template>
              <template v-if="record.replayTriggerOrderId"> · 班次联动 · 订单 {{ record.replayTriggerOrderId }}</template>
              <template v-if="record.refundId"> ·
                <button type="button" class="order-link" @click="emit('show-order', record.refundSourceVersionId)">退款 {{ record.refundId }}</button>
              </template>
            </small>
          </template>
          <template v-else-if="column.key === 'beneficiary'">
            {{ record.personName }}<small v-if="record.beneficiaryKind==='pool'" class="order-version">团队池</small>
            <small v-else-if="record.beneficiaryKind==='company'" class="order-version">公司分润</small>
          </template>
          <template v-else-if="column.key === 'amount'">
            <strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong>
            <small v-if="record.tierCredit" class="order-version">阶梯复算补计</small>
          </template>
        </template>
        <template #emptyText>未找到符合条件的退款明细</template>
      </a-table>
    </div>
    <div v-if="hasMoreDetails('refunds', draft.scopeRefundCount ?? draft.refundCount)" class="paging"><a-button :loading="loading" @click="loadMore('refunds', 'afterRefundId')">加载下一页</a-button></div>
  </section>
  <section v-if="draft.scopeCorrectionCount ?? draft.correctionCount" class="records draft-issues">
    <div class="records-head"><h2>受益人更正明细</h2></div>
    <div class="table-container">
      <a-table :columns="correctionColumns" :data-source="draft.corrections" :loading="loading" :pagination="false" row-key="id" :scroll="{x:710}">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='order'">
            <button type="button" class="order-link" @click="emit('show-order', record.sourceVersionId)">{{ record.originalOrderNo || record.orderId }}</button>
            <small class="order-version">
              <button v-if="record.ruleVersion" type="button" class="order-link" @click="showAppliedRule('corrections',record)">结算时规则 v{{ record.ruleVersion }}</button>
              <template v-if="record.ruleLabel"> · {{ record.ruleLabel }}</template>
            </small>
            <small class="order-version">
              <button type="button" class="order-link" @click="emit('show-order', record.correctionSourceVersionId)">更正来源版本 {{ record.sourceVersion }}</button>
            </small>
            <small v-if="record.triggerOrderId || record.triggerRefundId" class="order-version">
              联动来源 {{ record.triggerOrderId || record.orderId }}<template v-if="record.triggerRefundId"> · 退款 {{ record.triggerRefundId }}</template>
            </small>
          </template>
          <template v-else-if="column.key==='amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
        <template #emptyText>未找到符合条件的受益人更正</template>
      </a-table>
    </div>
    <div v-if="hasMoreDetails('corrections', draft.scopeCorrectionCount ?? draft.correctionCount)" class="paging"><a-button :loading="loading" @click="loadMore('corrections','afterCorrectionId')">加载下一页</a-button></div>
  </section>
  </template>
  <section v-if="draft && view==='issues'" class="records">
    <div class="records-head"><h2>{{ draft.issueCount ? (draft.advisoryCount ? '待核项与处理提示' : '待核项') : '处理提示' }}</h2></div>
    <div v-if="draft.issueCount || draft.advisoryCount" class="table-container">
      <a-table :columns="issueColumns" :data-source="draft.issues" :pagination="false" row-key="id" :scroll="{ x: 510 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'source'">
            <button v-if="record.sourceVersionId" type="button" class="order-link" @click="emit('show-issue-order', record.sourceVersionId)">{{ record.orderNo || record.orderId }}</button>
            <span v-else>{{ record.orderId || '—' }}</span>
            <small v-if="record.sourceVersion" class="order-version">订单版本 {{ record.sourceVersion }}</small>
            <small v-if="record.enrollmentId" class="order-version">报名 {{ record.enrollmentId }}</small>
            <small v-if="record.enrollmentOrderIds?.length" class="order-version">
              关联付款单
              <template v-for="(orderId,index) in record.enrollmentOrderIds" :key="orderId">
                <template v-if="index">、</template><a :href="relatedOrderLink({orderId,source:record.source})">{{ orderId }}</a>
              </template>
            </small>
          </template>
          <template v-else-if="column.key === 'component'">{{ record.componentLabel || componentLabel(record.component) || '—' }}</template>
          <template v-else-if="column.key === 'reason'">
            <a-tag v-if="record.advisory" color="blue">提示</a-tag>{{ draftIssueLabels[record.code] || record.code }}
            <small v-if="record.code==='SALES_COMPANY_REVIEW'" class="order-version">请在大春 AI 核对成交人和销售公司，更正后重新拉取。</small>
            <small v-if="record.componentLabel && record.ruleVersion" class="order-version">
              <button type="button" class="order-link" @click="showAppliedRule('issues',record)">查看试算规则 v{{ record.ruleVersion }}</button>
            </small>
            <small v-else-if="issueRuleLink && ruleIssues.has(record.code)" class="order-version"><a :href="issueRuleLink">查看产品规则</a></small>
            <template v-if="record.recovery">
              <small class="order-version">{{ record.recovery.personName }}（人员 {{ record.recovery.personId }}） · {{ formatMoney(-record.recovery.amountMinor) }}</small>
              <a-button v-if="canReviewRecovery" type="link" size="small" :disabled="refreshStatus?.pending || calculating || savingRecovery" @click="openRecovery(record)">登记扣回处理</a-button>
            </template>
          </template>
        </template>
      </a-table>
    </div>
    <a-empty v-else description="本期暂无待核项"/>
    <div v-if="draft.issueCount + (draft.advisoryCount || 0) > draft.issues.length" class="paging"><a-button :loading="loading" @click="loadMore('issues', 'afterIssueId')">加载下一页</a-button></div>
  </section>
  <DraftAggregateOrders v-if="draft && aggregateGroup" :period-id="draft.periodId" :version="draft.version" :group="aggregateGroup"
    :api="api" :format-money="formatMoney" :format-time="formatTime"
    @close="aggregateGroup=undefined" @show-order="id=>{aggregateGroup=undefined; emit('show-order',id)}"/>
  <CatalogRuleMatches v-if="hasMatching && (!draft || view==='matching')" :period-id="selected" :draft-available="selectedPeriod.catalogDraftAvailable"
    :api="api" :format-money="formatMoney" :format-time="formatTime" @show-order="emit('show-issue-order',$event)"/>
  <section v-if="!draft && !error && !hasMatching" class="records empty">
    <a-empty :description="periodsLoading || loading ? '正在读取核算期' : ruleSetupLink ? '本期签到名单已就绪，待配置规则' : selected ? '本期暂无草稿' : options.length ? '请选择核算期' : month ? '所选月份暂无核算期' : '暂无核算期'" />
    <a-button v-if="!periodsLoading && !loading && ruleSetupLink" type="primary" :href="ruleSetupLink">配置本期规则</a-button>
    <a-button v-if="!periodsLoading && !loading && !options.length" href="#periods">查看核算期次</a-button>
  </section>
  <Modal :open="!!recovery" title="登记扣回处理" ok-text="保存并试算" cancel-text="取消" :confirm-loading="savingRecovery"
    :ok-button-props="{ disabled: !recoveryOutcome }" :cancel-button-props="{ disabled: savingRecovery }" :closable="!savingRecovery"
    :mask-closable="!savingRecovery" :keyboard="!savingRecovery" @ok="saveRecovery" @cancel="recovery=undefined">
    <template v-if="recovery">
      <p>{{ recovery.personName }}（人员 {{ recovery.personId }}） · 待扣回 {{ formatMoney(-recovery.amountMinor) }}</p>
      <p>原结算期 {{ recovery.originalPeriod }} · 结算时规则 v{{ recovery.ruleVersion }}</p>
      <a-form layout="vertical">
        <a-form-item label="处理方式" html-for="correction-recovery-outcome">
          <a-select id="correction-recovery-outcome" v-model:value="recoveryOutcome" :options="recoveryOptions" :disabled="savingRecovery" placeholder="请选择处理方式"/>
        </a-form-item>
      </a-form>
      <a-alert v-if="recoveryError" :message="recoveryError" type="error" show-icon/>
    </template>
  </Modal>
</template>

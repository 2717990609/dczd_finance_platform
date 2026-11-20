<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'
import CatalogRuleMatches from './CatalogRuleMatches.vue'
import DraftReport from './DraftReport.vue'
import DraftPoolAllocations from './DraftPoolAllocations.vue'
import DraftSettlementReview from './DraftSettlementReview.vue'
import DraftAggregateOrders from './DraftAggregateOrders.vue'
import { benefitStatus, salesRoles, salesStatus } from './catalog-labels'

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
const reason = {
  ...salesStatus,
  ...benefitStatus,
  INVALID_PAYMENT: '付款金额或付款编号待核',
  UNCLASSIFIED: '订单场景分类待核',
  MAPPING_MISSING: '订单没有匹配的场景映射',
  MAPPING_CONFLICT: '订单同时命中多条场景映射',
  AGGREGATE_SCENARIO_UNMAPPED: '参与场景缺少本期订单映射',
  AGGREGATE_PRIOR_UNSETTLED: '上一账期尚未确认，结转退款待核',
  AGGREGATE_ORIGINAL_AMOUNT_CHANGED: '已结算的原付款金额有变更',
  AGGREGATE_SALES_ORGANIZATION_MISSING: '原付款缺少有效的部门或小组归属',
  REFUND_ADJUSTMENT: '来源退款更正待核对',
  REFUND_ORIGIN_MISSING: '退款未能唯一关联原付款',
  REFUND_EXCEEDS_PAYMENT: '累计退款超出原实收',
  BENEFIT_ORG_INACTIVE: '收款组织当前停用',
  ORIGINAL_RULE_MISSING: '原付款时没有适用的整期规则',
  ORIGINAL_RULE_CONFLICT: '原付款时有多份整期规则生效',
  RULE_MISSING: '付款时点没有适用规则',
  RULE_CONFLICT: '同一付款命中多份规则',
  COMPOUND_RECOGNITION_UNSUPPORTED: '组合确认待启用',
  COMPOUND_PRIOR_REFUND_INVALID: '请在子系统核对完成前的退款金额及原付款，再重新拉取',
  VIP_SALES_RULE_UNSUPPORTED: '销售分项待启用',
  UNCONFIGURED_COMPONENT: '此分项尚未配置可用的来源角色或计算方式',
  MISSING_PAYMENT_ID: '缺少原付款 ID',
  DUPLICATE_PAYMENT: '同一付款出现多条记录，需核对商品行及金额分摊',
  PAYMENT_ALREADY_SETTLED: '此付款已在其他账期结算，请核对已结算明细和订单变更',
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
  CORRECTION_SCOPE_CHANGED: '付款标识、产品、金额或时间有变动，需核对原结算与最新订单',
  CORRECTION_LINKED_REVIEW: '归属变更涉及关联分项，需核对计提金额、受益人及资金池',
  CORRECTION_REFUND_WAITING: '该退款关联的受益人更正仍在其他账期待办理，请先完成更正',
  CORRECTION_MISSING_FACT_OR_RULE: '缺少结算时使用的推荐规则或有效的最新付款金额',
  CORRECTION_REFUND_CONFLICT: '最新付款与已登记退款无法按结算时规则对平',
  CORRECTION_RECOVERY_PENDING: '待确认扣回方式',
  PRACTICE_CORRECTION_WAITING: '该班次有较早账期的受益人更正待处理',
  PRACTICE_CORRECTION_REPLAY_REQUIRED: '无法按原班次规则完成复算，请核对订金、尾款、人员归属和累计人数',
  PRACTICE_CORRECTION_LINKED_REVIEW: '归属变更涉及公司、团队池或新增计提分项，请核对班次结算',
  PRACTICE_CORRECTION_LATER_TIERS: '本次更正可能影响后续已结算班次的累计人数，请核对后续班次',
  PRACTICE_DUPLICATE_ENROLLMENT: '同一报名和付款仍在其他期次，请在来源名单核对延期',
  PRACTICE_PAYMENT_ALREADY_SETTLED: '报名中的付款已结算，请核对原班次和延期记录',
  PRACTICE_REFUND_PENDING: '对应付款已有退款，请重新试算',
  PRACTICE_REFUND_AFTER_ROSTER: '名单确认后发生退款，按退款账期办理',
  PRACTICE_REFUND_PRODUCT_MISMATCH: '退款与原付款的产品归属不一致，请在子系统核对',
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
  PRACTICE_POOL_MISSING: '资金池或归集组织待核',
  PRACTICE_REFUND_MONTH_MISSING: '退款发生月份尚未建立退款账期',
  PRACTICE_REFUND_REPLAY_REQUIRED: '退款联动分润待核，请核对结算时的名单、付款和规则',
  PRACTICE_TIER_COMPANY_NOT_CONFIGURED: '该销售公司未配置阶梯',
  PRACTICE_TIER_OPENING_MISSING: '所选冻结期初中缺少该人员的期初人数',
  PRACTICE_TIER_HISTORY_MISSING: '累计起点或历史期次缺少已确认名单',
  PRACTICE_TIER_HISTORY_INCOMPLETE: '历史新报的付款或人员归属待核',
  PRACTICE_TIER_GROUP_INCOMPLETE: '本期新报的付款或人员归属待核，暂无法选档',
  PRACTICE_TIER_NOT_MATCHED: '本笔没有匹配的累计人数档或实付档',
  PRACTICE_TIER_EXCEEDS_PAYMENT: '命中的档位计提超过实付金额',
  PRACTICE_PERIOD_APPOINTMENT_MISSING: '找不到分项指定的组织任职记录',
  PRACTICE_SALES_ORGANIZATION_MISSING: '无法确认付款时的销售部门或小组',
  PRACTICE_PERIOD_BENEFIT_INCOMPLETE: '适用报名或付款待核，暂未汇总此分项',
  PRACTICE_REGISTRATION_ATTENDANCE_MISSING: '缺少本期报名的签到记录，请重新拉取大春之道订单',
  PRACTICE_REGISTRATION_ORDER_MISMATCH: '签到关联订单与确认名单不一致，请到大春之道核对',
  PRACTICE_REGISTRATION_PERIOD_MISMATCH: '签到班次或时间与确认名单不一致，请到大春之道核对',
  PRACTICE_REGISTRATION_ATTRIBUTION_MISSING: '报名缺少销售账号或公司，请在大春之道更正后重新拉取',
  PRACTICE_PERIOD_BENEFIT_TIER_MISSING: '净实收金额没有匹配的班次档位',
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
        <small>{{ draft.aggregatePackageId ? '整期计提' : draft.practiceRosterVersion ? '本班净分润' : '本期净分润' }}</small><strong>{{ formatMoney(draft.totalMinor) }}</strong>
      </div>
      <div v-if="!draft.aggregatePackageId && !draft.practiceRefundMonth && (!draft.practiceRosterVersion || draft.correctionCount || !draft.refundsReady)"><small>计提分润</small><strong>{{ formatMoney(draft.accruedMinor) }}</strong></div>
      <div v-if="!draft.aggregatePackageId && !draft.practiceRosterVersion"><small>退款扣减</small><strong>{{ draft.refundsReady ? formatMoney(draft.refundMinor) : '待重新试算' }}</strong></div>
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
          <template v-if="column.key==='ruleVersion'">v{{ record.ruleVersion }}</template>
          <button v-else-if="column.key==='events'" type="button" class="order-link" @click="aggregateGroup=record.key">查看收退款</button>
          <span v-else class="numeric">{{ formatMoney(Math.abs(record[column.key])) }}</span>
        </template>
      </a-table>
    </div>
  </section>
  <section v-if="!draft.aggregatePackageId && (draft.lineCount || draft.refundCount || draft.correctionCount)"
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
    <div class="records-head"><h2>退款扣减明细</h2></div>
    <div class="table-container">
      <a-table :columns="refundColumns" :data-source="draft.refunds" :loading="loading" :pagination="false" row-key="id" :scroll="{ x: 840 }">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'order'">
            <button type="button" class="order-link" @click="emit('show-order', record.sourceVersionId)">{{ record.originalOrderNo || record.originalOrderId }}</button>
            <small class="order-version">
              <button v-if="record.ruleVersion" type="button" class="order-link" @click="showAppliedRule('refunds',record)">计提时规则 v{{ record.ruleVersion }}</button>
              <template v-if="record.ruleLabel"> · {{ record.ruleLabel }}</template>
              <template v-if="record.replayTriggerOrderId"> · 班次联动 · 订单 {{ record.replayTriggerOrderId }} 的退款 {{ record.refundId }}</template>
              <template v-else> · 退款 {{ record.refundId }}</template>
            </small>
          </template>
          <template v-else-if="column.key === 'beneficiary'">
            {{ record.personName }}<small v-if="record.beneficiaryKind==='pool'" class="order-version">团队池</small>
            <small v-else-if="record.beneficiaryKind==='company'" class="order-version">公司分润</small>
          </template>
          <template v-else-if="column.key === 'amount'"><strong class="numeric">{{ formatMoney(record.amountMinor) }}</strong></template>
        </template>
        <template #emptyText>未找到符合条件的退款扣减</template>
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
            <button type="button" class="order-link" @click="emit('show-order', record.sourceVersionId)">{{ record.orderNo || record.orderId }}</button>
            <small class="order-version">
              <button v-if="record.ruleVersion" type="button" class="order-link" @click="showAppliedRule('corrections',record)">结算时规则 v{{ record.ruleVersion }}</button>
              <template v-if="record.ruleLabel"> · {{ record.ruleLabel }}</template>
            </small>
            <small class="order-version">更正来源版本 {{ record.sourceVersion }}</small>
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
            <a-tag v-if="record.advisory" color="blue">提示</a-tag>{{ reason[record.code] || record.code }}
            <small v-if="record.code==='SALES_COMPANY_REVIEW'" class="order-version">请在大春 AI 核对成交人和销售公司，更正后重新拉取。</small>
            <small v-if="issueRuleLink && ruleIssues.has(record.code)" class="order-version"><a :href="issueRuleLink">查看产品规则</a></small>
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

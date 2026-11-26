<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { DownOutlined } from '@ant-design/icons-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import CatalogCommissionScopeFields from './CatalogCommissionScopeFields.vue'
import CatalogOrganizationAppointmentFields from './CatalogOrganizationAppointmentFields.vue'
import CatalogTeamPoolFields from './CatalogTeamPoolFields.vue'
import DraftAggregateOrders from './DraftAggregateOrders.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogPeriodTiers from './CatalogPeriodTiers.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import { catalogAppointmentLevels, catalogOrderScope, emptyCatalogOrderScope } from './rule-component-form'

const props = defineProps({rule:Object, applied:Object, focusItemKey:String, products:Array, api:Function, formatMoney:Function, formatTime:Function, optionLabel:Function, canReviewActivity:Boolean})
const emit = defineEmits(['close','published','open-draft','copy','open-version','resumed','show-order'])
const creating = computed(() => Boolean(props.rule?.create))
const copiedFrom = computed(() => props.rule?.copiedFrom)
const productId = ref(props.rule?.productId || (props.products?.length===1 ? props.products[0].value : undefined))
const ruleName = ref('')
const creationCycle = ref()
const sheet = ref()
const rows = ref([])
const mixed = computed(() => Boolean(props.rule?.mixedArrangements || sheet.value?.mixedArrangements))
const title = computed(() => mixed.value ? creating.value ? '新增产品分润规则' : '产品分润配置'
  : copiedFrom.value ? '复制整期规则' : creating.value ? '新增整期规则' : props.applied ? '计提使用的整期规则' : '整期分润配置')
const activeComponent = ref()
const scenarioCodes = ref([])
const editingScenarios = ref(false)
let newComponentID = 0
const effectiveDate = ref('')
const resumeDate = ref('')
const resumeError = ref('')
const resuming = ref(false)
const amounts = ref({receipts:0,refunds:0,carry:0})
const quoteItem = ref()
const loading = ref(false)
const saving = ref(false)
const previewing = ref(false)
const error = ref('')
const quote = ref()
const activityMonth = ref(props.rule?.reviewMonth || new Date(Date.now()+8*3600000).toISOString().slice(0,7))
const reviewingActivity = ref(false)
const activity = ref()
const activityMode = ref()
const activityGroup = ref()
const showsPriorCarry = computed(() => activity.value?.grouping?.groups.some(group=>group.carryInMinor<0))
const otherGroupingIssues = computed(() => {
  const totals = {...activity.value?.grouping?.issues}
  for (const issue of activity.value?.grouping?.targetIssues || []) totals[issue.code] -= issue.count
  return Object.entries(totals).filter(([,count])=>count>0)
})
const activityError = ref('')
const savingDraft = ref(false)
let versionRequest = 0
let quoteRequest = 0
let activityRequest = 0

const cycleLabels = {MONTHLY:'自然月',MONTHLY_25_TO_24:'每月 25 日至次月 24 日'}
const cycleOptions = Object.entries(cycleLabels).map(([value,label])=>({value,label}))
const activityIssueLabels = {
  AGGREGATE_REFUND_MONTH_MISSING:'请按该整期规则打开退款发生月的草稿',
  REFUND_ADJUSTMENT:'退款更正待核对', INVALID_EVENT:'交易编号或金额缺失', DUPLICATE_EVENT:'交易编号重复',
  REFUND_ORIGIN_MISSING:'退款未能唯一关联原付款', UNCLASSIFIED:'原付款未分类', MAPPING_MISSING:'缺少场景映射',
  MAPPING_CONFLICT:'场景映射冲突', INVALID_PAYMENT:'原付款信息不完整', REFUND_EXCEEDS_PAYMENT:'累计退款超过原付款',
  SALES_PERSON_MISSING:'原付款缺少销售人员', SALES_PERSON_CONFLICT:'销售人员无法唯一确定',
  SALES_SOURCE_CONFLICT:'订单销售归属冲突', SALES_COMPANY_CONFLICT:'销售公司无法唯一确定',
  AGGREGATE_SALES_ORGANIZATION_MISSING:'原付款缺少有效的部门或小组归属',
  SALES_CATEGORY_MISSING:'原付款的销售类别待核',
  SALES_CATEGORY_CONFLICT:'原付款的销售类别有冲突',
  AGGREGATE_PERFORMANCE_APPOINTMENT_MISSING:'缺少规则指定的任职记录',
  SALES_ORGANIZATION_SCOPE_CONFLICT:'付款时的销售归属存在重叠，无法确认是否计入所选范围',
}
const groupingIssueLabels = {
  BENEFIT_APPOINTMENT_CONFLICT:'付款时存在多位在任人员',
  COMMISSION_SALES_ORGANIZATION_MISSING:'付款时的销售组织待核',
  AGGREGATE_SALES_ORGANIZATION_MISSING:'付款时的销售组织待核',
  SALES_ORGANIZATION_SCOPE_CONFLICT:'付款时的销售组织归属有冲突',
  ORIGINAL_RULE_MISSING:'原付款时没有适用规则',
  ORIGINAL_RULE_CONFLICT:'原付款时有多个适用版本',
  BENEFIT_ORG_MISSING:'缺少收款组织映射',
  BENEFIT_ORG_INACTIVE:'收款组织当前停用',
  BENEFIT_ORG_CONFLICT:'收款组织无法唯一确定',
  BENEFIT_PERSON_MISSING:'找不到指定收款人',
  BENEFIT_POOL_OWNER_MISSING:'付款时缺少池负责人',
  BENEFIT_POOL_OWNER_CONFLICT:'付款时有多位池负责人',
  BENEFIT_POOL_MEMBERS_MISSING:'付款时没有可分配成员',
  MEMBERSHIP_TYPE_PENDING:'缺少新购或续费标记，请在子系统补齐后重新拉取',
}
const sourceStatusLabels = {READY:'覆盖本期',NOT_CONFIGURED:'来源未配置',NOT_STARTED:'未同步',INCOMPLETE:'同步未完成',
  OUTSIDE_RANGE:'未覆盖本期',STALE:'同步已过期',SOURCE_CHANGED:'来源已变更',FAILED:'同步失败',CLOCK_GAP:'来源时钟待核',UNSUPPORTED:'来源待核'}
const localDate = stamp => new Date(Date.parse(stamp) + 8*3600000).toISOString().slice(0,10)
function nextStart(stamp, cycle) {
  const from = new Date(Date.parse(stamp)+8*3600000)
  const day = cycle==='MONTHLY' ? 1 : 25
  let start = new Date(Date.UTC(from.getUTCFullYear(),from.getUTCMonth(),day))
  if (start<=from) start = new Date(Date.UTC(from.getUTCFullYear(),from.getUTCMonth()+1,day))
  return start.toISOString().slice(0,10)
}
function accountingMonthAt(time, cycle) {
  const date = new Date(Date.parse(time)+8*3600000)
  if (cycle==='MONTHLY_25_TO_24' && date.getUTCDate()>=25) {
    return new Date(Date.UTC(date.getUTCFullYear(),date.getUTCMonth()+1,1)).toISOString().slice(0,7)
  }
  return date.toISOString().slice(0,7)
}
const firstStart = computed(() => {
  if (!sheet.value || creating.value) return
  const afterPrevious = nextStart(sheet.value.effectiveFrom,sheet.value.cycle)
  if (!mixed.value) return afterPrevious
  const next = nextStart(new Date().toISOString(),sheet.value.cycle)
  return next > afterPrevious ? next : afterPrevious
})
const cutoff = computed(() => sheet.value?.effectiveTo
  ? localDate(new Date(Date.parse(sheet.value.effectiveTo)-86400000).toISOString()) : undefined)
const canSave = computed(() => sheet.value?.mayPublish && (!cutoff.value || firstStart.value<=cutoff.value))
function defaultEffectiveDate() {
  const next = nextStart(new Date().toISOString(),sheet.value.cycle)
  return next>=firstStart.value && (!cutoff.value || next<=cutoff.value) ? next : firstStart.value
}
const firstResumeDate = computed(() => {
  if (creating.value || !sheet.value?.paused) return ''
  const next = nextStart(new Date().toISOString(),sheet.value.cycle)
  const first = nextStart(new Date(Date.parse(sheet.value.effectiveFrom)-1).toISOString(),sheet.value.cycle)
  return next > first ? next : first
})
const canResume = computed(() => sheet.value?.mayPublish && !props.applied && firstResumeDate.value &&
  (!cutoff.value || firstResumeDate.value<=cutoff.value))
const componentsChanged = computed(() => JSON.stringify(rows.value.map(configuredRow)) !==
  JSON.stringify(editableRows(sheet.value?.items || []).map(configuredRow)))
const scenariosChanged = computed(() => {
  const published = sheet.value?.scenarios || []
  return scenarioCodes.value.length !== published.length || !published.every(sku=>scenarioCodes.value.includes(sku.code))
})
const changed = computed(() => creating.value ? Boolean(ruleName.value.trim() && scenarioCodes.value.length && rows.value.length)
  : componentsChanged.value || scenariosChanged.value)
const scenarioOptions = computed(() => {
  const label = sku => sku.label+(sku.requiresCompletion ? ' · 完成后计提' : '')
  const available = new Map((sheet.value?.options?.scenarios || []).map(sku=>[sku.code,{value:sku.code,label:label(sku),membershipSku:sku.membershipSku}]))
  for (const sku of copiedFrom.value?.scenarios || sheet.value?.scenarios || []) {
    if (!available.has(sku.code) && (!copiedFrom.value || scenarioCodes.value.includes(sku.code))) {
      available.set(sku.code,{value:sku.code,label:copiedFrom.value ? sku.label+'（当前不可选）'
        : sku.mapped ? label(sku) : sku.label+'（尚未映射）',disabled:!copiedFrom.value && sku.locked,membershipSku:sku.membershipSku})
    }
  }
  return [...available.values()]
})
const skuLabel = code => scenarioOptions.value.find(option=>option.value===code)?.label || code
const membershipCodes = computed(() => new Set(scenarioOptions.value.filter(option=>option.membershipSku).map(option=>option.value)))
const selectedSKUs = item => item.scenarioCodes?.length ? item.scenarioCodes : scenarioCodes.value
const hasMembershipSKU = item => selectedSKUs(item).some(code=>membershipCodes.value.has(code))
function itemOrderTypes(item, limit = Infinity) {
  const codes = selectedSKUs(item)
  const names = codes.slice(0,limit).map(skuLabel).join('、')
  const text = (names || '待选择 SKU')+(codes.length>limit ? ' 等 '+codes.length+' 个 SKU' : '')
  return item.membershipNewOnly ? text+' · 会员仅计新报' : text
}
function selectItemSKUs(item,codes) {
  item.scenarioCodes = codes
  if (!hasMembershipSKU(item)) item.membershipNewOnly = false
}
function itemScenarioOptions(item) {
  return [...new Set([...scenarioCodes.value,...item.scenarioCodes])].map(code=>({
    value:code,label:skuLabel(code)+(scenarioCodes.value.includes(code) ? '' : '（已移出规则）'),
  }))
}

function editableRows(items) {
  return items.map(item => {
    const tiers = item.tiers || []
    return {uid:item.itemKey,itemKey:item.itemKey,label:item.label,targetKind:item.targetKind,
      organizationKey:item.organizationKey,personId:item.personId,personName:item.personId ? item.targetName : undefined,
      poolName:item.poolName || '',poolId:item.poolId,legacyPool:item.targetKind==='pool' && !item.poolId,
      ownerRole:item.ownerRole || '',memberRole:item.memberRole || '',
      ownerPersonId:item.ownerPersonId,ownerName:item.ownerName,
      scenarioCodes:item.scenarioCodes || [],membershipNewOnly:Boolean(item.membershipNewOnly),
      salesScope:catalogOrderScope(item),recipientDuty:item.recipientDuty,
      appointment:item.appointment ? {...item.appointment} : undefined,
      appointmentLevel:item.appointmentLevel,appointmentRole:item.appointmentRole,
      method:item.fixedMinor!=null ? 'fixed' : tiers.length===1 ? 'rate' : 'tiers',
      fixedAmount:item.fixedMinor==null ? undefined : item.fixedMinor/100,fixedPerOrder:item.fixedPerOrder,perOrder:Boolean(item.perOrder),
      uniformRate:tiers.length===1 ? tiers[0].rate_bps/100 : undefined,
      tiers:tiers.length===1 ? [] : tiers.map(row=>({from:(row.min_inclusive || 0)/100,rate:row.rate_bps/100}))}
  })
}
function componentTarget(item) {
  return {itemKey:item.itemKey,label:item.label.trim(),targetKind:item.targetKind,organizationKey:item.poolId ? undefined : item.organizationKey,
    personId:item.personId,perOrder:item.perOrder || undefined,poolId:item.poolId,poolName:item.poolId ? undefined : item.poolName,
    ownerRole:item.poolId ? undefined : item.ownerRole,ownerPersonId:item.poolId ? undefined : item.ownerPersonId,
    appointmentLevel:item.appointmentLevel,appointmentRole:item.appointmentRole,
    memberRole:item.memberRole,scenarioCodes:[...item.scenarioCodes].sort(),membershipNewOnly:Boolean(item.membershipNewOnly),
    ...orderScopeBody(item)}
}
function configuredRow(item) {
  const target = componentTarget(item)
  if (item.method==='fixed') return {...target,fixedAmount:item.fixedAmount}
  return {...target,tiers:item.method==='rate' ? [{from:0,rate:item.uniformRate}] : item.tiers}
}
const periodScopeUnits = computed(() => sheet.value?.options?.salesUnits || [])
const periodScopeCategories = computed(() => sheet.value?.options?.categories || [])
const sharedScope = computed(() => copiedFrom.value?.performanceScope || sheet.value?.performanceScope)
const sharedScopeLabels = computed(() => copiedFrom.value?.scopeLabels || sheet.value?.scopeLabels)
const salesUnitNames = computed(() => ({...sharedScopeLabels.value?.units,
  ...Object.fromEntries(periodScopeUnits.value.map(unit=>[unit.value,unit.label]))}))
const salesCategoryNames = computed(() => ({...sharedScopeLabels.value?.categories,
  ...Object.fromEntries(periodScopeCategories.value.map(category=>[category.value,category.label]))}))
const salesUnitsText = ids => ids?.map(id=>salesUnitNames.value[id] || '组织待核').join('、')
function appointmentText(appointment, person) {
  if (!appointment) return ''
  const role = props.optionLabel('catalogAppointmentRole',appointment.role) || appointment.role
  return [person,salesUnitNames.value[appointment.unitId],role,'任职期间'].filter(Boolean).join(' · ')
}
const sharedScopeText = computed(() => [
  salesUnitsText(sharedScope.value?.unitIds),
  appointmentText(sharedScope.value?.appointment,sharedScopeLabels.value?.person),
].filter(Boolean).join('；'))
function orderScopeText(item) {
  const scope = item.salesScope
  const label = scope==='own' ? '本人销售' : scope==null ? (sharedScope.value ? '规则共同范围' : '整个适用账期') : emptyCatalogOrderScope(scope) ? '请选择销售范围' :
    [salesUnitsText(scope.unitIds),
      scope.categoryIds.map(id=>salesCategoryNames.value[id] || '类别待核').join('、')].filter(Boolean).join(' · ')
  const appointment = appointmentText(item.appointment)
  return appointment ? label+' · '+appointment : label
}
function orderScopeBody(item) {
  return {ownSales:item.salesScope==='own' || undefined,
    salesScopeUnitIds:item.salesScope?.unitIds,salesScopeCategoryIds:item.salesScope?.categoryIds,recipientDuty:item.recipientDuty,
    appointment:item.appointment ? {...item.appointment,personId:item.personId} : undefined}
}
const methodLabels = {rate:'比例',fixed:'固定金额',tiers:'阶梯'}
const methodOptions = Object.entries(methodLabels).map(([value,label])=>({value,label}))
const calculationBasisOptions = [{value:'order',label:'逐单'},{value:'period',label:'整期'}]
function setCalculationBasis(item, basis) {
  item.perOrder = basis==='order'
  if (item.perOrder && item.method==='tiers') setMethod(item,'rate')
  if (!item.perOrder && item.targetKind==='order_seller') setTarget(item,'company')
}
function setMethod(item, method) {
  if (method==='tiers' && !item.tiers.length) item.tiers = [{from:0,rate:item.uniformRate}]
  if (method==='rate' && item.uniformRate==null && item.tiers.length===1) item.uniformRate = item.tiers[0].rate
  item.method = method
}
function methodSummary(item) {
  if (item.method==='fixed') {
    const unit = item.perOrder || item.fixedPerOrder || !mixed.value && item.targetKind==='appointment' && canSave.value ? '笔' : '期'
    return item.fixedAmount==null ? methodLabels.fixed : props.formatMoney(Math.round(item.fixedAmount*100))+' / '+unit
  }
  if (item.method==='tiers') return item.tiers.length+' 档阶梯'
  return item.uniformRate==null ? methodLabels.rate : '统一 '+item.uniformRate+'%'
}
const targetTypes = [{value:'person',label:'指定个人'},{value:'company',label:'指定公司'},{value:'pool',label:'团队池'},
  {value:'appointment',label:'销售组织的在任人员'}]
const orderSeller = {value:'order_seller',label:'订单上的成交销售'}
const targetLabels = {person:'个人',company:'公司',pool:'团队池',appointment:'在任人员',order_seller:'成交销售'}
const peopleFilters = computed(() => creating.value ? {productId:productId.value,scope:'period'} : {ruleId:props.rule?.id,scope:'period'})
const unitOptions = computed(() => (sheet.value?.options?.organizations || [])
  .map(unit=>({value:unit.unitId,label:unit.label,kind:unit.kind})))
function targetLabel(item) {
  const duty = item.recipientDuty && props.optionLabel('organizationDuty',item.recipientDuty)
  if (item.targetKind==='appointment') return [catalogAppointmentLevels.find(option=>option.value===item.appointmentLevel)?.label,
    item.appointmentRole && props.optionLabel('catalogAppointmentRole',item.appointmentRole),duty].filter(Boolean).join(' · ') || '待选择岗位'
  if (item.targetKind==='order_seller') return orderSeller.label
  if (item.targetKind==='person') return [item.personName || '待选择收款人',duty].filter(Boolean).join(' · ')
  const pool = item.poolId && sheet.value.options?.teamPools?.find(pool=>pool.id===item.poolId)
  const name = pool?.unitName || availableOrganizations(item).find(org=>org.value===item.organizationKey)?.label ||
    sheet.value.items.find(old=>old.itemKey===item.itemKey)?.targetName
  const owner = item.targetKind==='pool' && (item.ownerName || (item.ownerRole && props.optionLabel('catalogAppointmentRole',item.ownerRole)))
  return [pool?.name || item.poolName,name,owner].filter(Boolean).join(' · ') || '待选择收款对象'
}
function availableOrganizations(row) {
  const options = (sheet.value?.options?.organizations || []).filter(item=>row.targetKind==='pool' || item.kind==='COMPANY')
  if (!row.organizationKey || options.some(item=>item.value===row.organizationKey)) return options
  const original = sheet.value.items.find(item=>item.itemKey===row.itemKey)
  return [...options,{value:row.organizationKey,label:original?.targetName || row.organizationName || row.organizationKey}]
}
const copiedOrganizationUnavailable = row => copiedFrom.value && row.organizationKey &&
  !row.poolId &&
  !sheet.value.options?.organizations?.some(option=>option.value===row.organizationKey)
function availableRoles(role) {
  const options = (sheet.value?.options?.roles || []).filter(item=>item.organization)
  if (!role || options.some(item=>item.value===role)) return options
  return [...options,{value:role,label:props.optionLabel('catalogAppointmentRole',role) || role}]
}
function setTarget(row, target) {
  row.targetKind = target
  row.organizationKey = undefined
  row.personId = undefined
  row.personName = undefined
  row.ownerRole = ''
  row.ownerPersonId = undefined
  row.ownerName = undefined
  row.memberRole = ''
  row.poolName = ''
  row.poolId = undefined
  row.legacyPool = false
  row.appointment = undefined
  row.appointmentLevel = undefined
  row.appointmentRole = undefined
  if (target!=='person' && row.salesScope==='own') row.salesScope = null
}
function addComponent() {
  const uid = 'new-' + (++newComponentID)
  rows.value.push({uid,label:'',targetKind:'company',method:'rate',tiers:[],scenarioCodes:[],salesScope:null})
  activeComponent.value = uid
}
function initializeCreation(data, product) {
  const copy = copiedFrom.value
  creationCycle.value = copy?.cycle || data.cycle
  effectiveDate.value = nextStart(new Date().toISOString(),creationCycle.value)
  if (copy) {
    scenarioCodes.value = copy.scenarios.map(sku=>sku.code)
    rows.value = editableRows(copy.items).map(({itemKey,...row}, index)=>({
      ...row,uid:'new-'+(++newComponentID),organizationName:copy.items[index].targetName,
    }))
    activeComponent.value = rows.value[0]?.uid
    return
  }
  const options = data.options.scenarios
  const selected = product===props.rule.productId && options.find(option=>option.code===props.rule.sku)
  scenarioCodes.value = selected ? [selected.code] : options.length===1 ? [options[0].code] : []
  addComponent()
  if (mixed.value) {
    const sale = rows.value[0]
    sale.label = '销售提成'
    sale.perOrder = true
    sale.membershipNewOnly = true
    setTarget(sale,'order_seller')
  }
}
function removeComponent(row) {
  const index = rows.value.indexOf(row)
  rows.value.splice(index,1)
  activeComponent.value = rows.value[Math.min(index,rows.value.length-1)].uid
}
async function openArrangement(uid) {
  if (!uid || !rows.value.some(row=>row.uid===uid)) return
  activeComponent.value = uid
  await nextTick()
  const heading = document.getElementById('period-rule-control-'+uid)
  heading?.scrollIntoView({block:'start'})
  heading?.focus({preventScroll:true})
}
const targetIssueRow = issue => issue.ruleId ? rows.value.find(row=>row.itemKey===issue.itemKey)
  : rows.value[issue.proposedItemIndex]
const issueInOtherVersion = issue => issue.ruleId && issue.ruleId!==props.rule.id
function followTargetIssue(issue) {
  if (!issueInOtherVersion(issue)) return openArrangement(targetIssueRow(issue)?.uid)
  emit('open-version',{ruleId:issue.ruleId,itemKey:issue.itemKey,month:activityMonth.value})
}
function minor(value, message) {
  if (value == null || !Number.isSafeInteger(Math.round(value*100)) || value < 0) throw new Error(message)
  return Math.round(value*100)
}
function componentChanges() {
  const outside = rows.value.find(item=>item.scenarioCodes?.some(code=>!scenarioCodes.value.includes(code)))
  if (outside) throw new Error('请更新「'+outside.label+'」的计提订单类型，所选 SKU 已移出规则。')
  const missingMembership = rows.value.find(item=>item.membershipNewOnly && !hasMembershipSKU(item))
  if (missingMembership) throw new Error('请为「'+missingMembership.label+'」选择适用的会员 SKU，或关闭会员仅计新报。')
  if (!canSave.value || (!creating.value && !componentsChanged.value)) return {}
  return {components:rows.value.map(item=>{
    if (!item.label.trim() ||
      (item.targetKind==='person' ? !item.personId : item.targetKind==='appointment'
        ? !item.appointmentRole || !item.appointmentLevel : item.targetKind==='order_seller' ? !item.perOrder : !item.organizationKey && !item.poolId)) {
      throw new Error('请填写收款安排名称并选择收款对象。')
    }
    if (item.targetKind==='pool' && (!(item.poolId || item.legacyPool && item.poolName?.trim()) || !item.memberRole)) {
      throw new Error('请选择团队池和成员职责。')
    }
    if (emptyCatalogOrderScope(item.salesScope)) throw new Error('请为「'+item.label+'」选择适用的销售组织或销售类别。')
    if (item.appointment && (!item.appointment.unitId || !item.appointment.role)) throw new Error('请为「'+item.label+'」选择任职组织和岗位。')
    const target = componentTarget(item)
    if (item.method==='fixed') return {...target,fixedMinor:minor(item.fixedAmount,'请输入固定金额。')}
    const {tiers} = configuredRow(item)
    const starts = tiers.map(row=>minor(row.from,'请输入每档净额起点。'))
    if (starts.some((from,index)=>index && from<=starts[index-1])) throw new Error('每一档净额起点须高于上一档。')
    return {...target,tiers:tiers.map((row,index)=>{
      const rate = minor(row.rate,item.method==='rate' ? '请输入统一比例。' : '请输入每档比例。')
      if (rate>10000) throw new Error('比例须在 0% 到 100% 之间。')
      return {min_inclusive:starts[index],max_exclusive:starts[index+1],rate_bps:rate}
    })}
  })}
}
function removeTier(tiers, index) {
  tiers.splice(index,1)
  tiers[0].from = 0
}

watch(()=>[props.rule?.id,props.applied,productId.value], async ([id,applied,product])=>{
  const request = ++versionRequest
  error.value = ''
  sheet.value = undefined
  if (creating.value && !product) return
  loading.value = true
  try {
    const params = creating.value ? {productId:product} : {ruleId:id}
    const data = applied || await props.api('/catalog-period-rules?'+new URLSearchParams(params))
    if (request !== versionRequest) return
    sheet.value = data
    rows.value = editableRows(data.items)
    scenarioCodes.value = data.scenarios.map(sku=>sku.code)
    editingScenarios.value = false
    activeComponent.value = rows.value.length===1 ? rows.value[0].uid : undefined
    if (creating.value) initializeCreation(data,product)
    else {
      effectiveDate.value = canSave.value ? defaultEffectiveDate() : ''
      if (mixed.value && !props.rule?.reviewMonth && !applied) {
        const now = new Date().toISOString()
        activityMonth.value = accountingMonthAt(now>data.effectiveFrom ? now : data.effectiveFrom,data.cycle)
      }
    }
    const focus = props.focusItemKey || props.rule?.focusItemKey
    if (focus) void openArrangement(focus)
  } catch (err) {
    if (request === versionRequest) error.value = err.message
  } finally {
    if (request === versionRequest) loading.value = false
  }
}, {immediate:true})
watch(creationCycle, cycle=>{
  if (creating.value && cycle) effectiveDate.value = nextStart(new Date().toISOString(),cycle)
})
watch([rows,amounts,scenarioCodes,creationCycle,effectiveDate,ruleName,quoteItem], ()=>{
  ++quoteRequest
  previewing.value = false
  quote.value = undefined
}, {deep:true})
function clearActivity() {
  ++activityRequest
  activity.value = undefined
  activityGroup.value = undefined
  activityMode.value = undefined
  activityError.value = ''
  reviewingActivity.value = false
}
watch([activityMonth,()=>props.rule?.id], clearActivity)
watch([rows,scenarioCodes,creationCycle,effectiveDate,ruleName], ()=>{
  if (activityMode.value==='edits') clearActivity()
},{deep:true})

async function readActivity(mode = 'published') {
  const request = ++activityRequest
  reviewingActivity.value = true
  activityError.value = ''
  activity.value = undefined
  activityGroup.value = undefined
  activityMode.value = mode
  try {
    const result = await fetchActivity(mode)
    if (request === activityRequest) activity.value = result
  } catch (err) {
    if (request === activityRequest) activityError.value = err.message
  } finally {
    if (request === activityRequest) reviewingActivity.value = false
  }
}

function fetchActivity(mode, params = {}) {
  const query = new URLSearchParams(params)
  if (mode==='edits') {
    return props.api('/catalog-period-activity'+(query.size ? '?'+query : ''),{method:'POST',body:JSON.stringify(versionInput())})
  }
  query.set('ruleId',props.rule.id)
  query.set('month',activityMonth.value)
  if (mode==='paused') query.set('trialPaused','1')
  return props.api('/catalog-period-activity?'+query)
}
function loadActivityEvents(page) {
  const {id, eventToken} = activityGroup.value
  return fetchActivity(activityMode.value,{group:id,eventToken,page})
}
function showActivityOrder(versionId) {
  activityGroup.value = undefined
  emit('show-order',versionId)
}

async function openDraft() {
  savingDraft.value = true
  activityError.value = ''
  try {
    const result = await props.api('/catalog-aggregate-drafts',{method:'POST',
      body:JSON.stringify({ruleId:props.rule.id,month:activityMonth.value})})
    emit('open-draft',result)
  } catch (err) {
    activityError.value = err.message
  } finally {
    savingDraft.value = false
  }
}

async function preview() {
  const request = ++quoteRequest
  quote.value = undefined
  error.value = ''
  previewing.value = true
  try {
    const index = rows.value.findIndex(item=>item.uid===quoteItem.value)
    if (scopedQuote.value && index<0) throw new Error('请选择要演算的收款安排。')
    const result = await props.api('/catalog-period-rules/preview',{method:'POST',body:JSON.stringify({
      ...(creating.value ? creationInput() : scenariosChanged.value ? versionInput() : {baseId:props.rule.id,...componentChanges()}),
      ...(scopedQuote.value ? {itemIndex:index} : {}),
      receiptsMinor:minor(amounts.value.receipts,'请输入本期实收。'),
      refundsMinor:minor(amounts.value.refunds,'请输入本期退款。'),
      carryInMinor:-minor(amounts.value.carry,'请输入上期未抵扣退款。'),
    })})
    if (request === quoteRequest) quote.value = result
  } catch (err) {
    if (request === quoteRequest) error.value = err.message
  } finally {
    if (request === quoteRequest) previewing.value = false
  }
}
const scopedQuote = computed(() => !!sharedScope.value || rows.value.some(item=>item.salesScope!=null || item.appointment || item.appointmentLevel || item.scenarioCodes?.length || item.membershipNewOnly))
const quoteUsesOrders = computed(() => rows.value.some(item=>item.uid===quoteItem.value && (item.perOrder || !mixed.value && item.method==='fixed' && item.targetKind==='appointment')))
function creationInput() {
  if (!changed.value || !effectiveDate.value) throw new Error('请填写规则名称、生效日期，并选择参与场景。')
  const selectable = new Set(sheet.value.options.scenarios.map(sku=>sku.code))
  if (scenarioCodes.value.some(code=>!selectable.has(code))) throw new Error('请移除当前不可选的 SKU，并核对参与场景。')
  return {create:{productId:productId.value,label:ruleName.value.trim(),cycle:creationCycle.value,mixedArrangements:mixed.value || undefined},
    effectiveFrom:new Date(effectiveDate.value+'T00:00:00+08:00').toISOString(),scenarioCodes:scenarioCodes.value,
    performanceScope:copiedFrom.value?.performanceScope,
    ...componentChanges()}
}
function versionInput() {
  if (creating.value) return creationInput()
  if (!scenarioCodes.value.length) throw new Error('请至少选择一个参与场景。')
  if (!effectiveDate.value) throw new Error('请选择新版本的生效日期。')
  return {baseId:props.rule.id,effectiveFrom:new Date(effectiveDate.value+'T00:00:00+08:00').toISOString(),
    ...componentChanges(),
    ...(scenariosChanged.value ? {scenarioCodes:scenarioCodes.value} : {})}
}
async function publish() {
  saving.value = true
  error.value = ''
  try {
    const result = await props.api('/catalog-period-rules',{method:'POST',body:JSON.stringify(versionInput())})
    emit('published',result,creating.value ? productId.value : undefined)
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
function openResume() {
  resumeError.value = ''
  resumeDate.value = firstResumeDate.value
}
async function resumeRule() {
  if (!resumeDate.value || resuming.value) return
  resumeError.value = ''
  resuming.value = true
  try {
    const result = await props.api('/catalog-period-rules/resume',{method:'POST',body:JSON.stringify({
      ruleId:props.rule.id,effectiveFrom:new Date(resumeDate.value+'T00:00:00+08:00').toISOString(),
    })})
    resumeDate.value = ''
    emit('resumed',result)
  } catch (err) {
    resumeError.value = err.message
  } finally {
    resuming.value = false
  }
}
</script>

<template>
  <a-drawer open :title="title" width="min(1260px, 100vw)" root-class-name="finance-rule-editor"
    :closable="!saving && !savingDraft" :mask-closable="!saving && !savingDraft" :keyboard="!saving && !savingDraft" @close="emit('close')">
    <a-alert v-if="error" class="feedback" :message="error" type="error" show-icon/>
    <a-form v-if="creating" layout="vertical" size="small" class="rule-basics" :disabled="saving">
      <a-form-item label="所属产品" html-for="period-create-product" required>
        <a-select id="period-create-product" v-model:value="productId" :options="products" :disabled="!!copiedFrom" placeholder="选择产品"/>
      </a-form-item>
      <a-form-item label="规则名称" html-for="period-create-name" required>
        <a-input id="period-create-name" v-model:value="ruleName" :maxlength="80"/>
      </a-form-item>
      <a-form-item label="结算周期" html-for="period-create-cycle" required>
        <a-select id="period-create-cycle" v-model:value="creationCycle" :options="cycleOptions" :disabled="!sheet || mixed"/>
      </a-form-item>
      <a-form-item label="生效账期起点" html-for="period-rule-effective" required>
        <a-input id="period-rule-effective" v-model:value="effectiveDate" type="date" :disabled="!sheet"/>
      </a-form-item>
    </a-form>
    <a-spin :spinning="loading">
      <section v-if="sheet">
        <header v-if="!creating" class="period-component-heading">
          <h2>{{ sheet.label }} <a-tag>v{{ sheet.version }}</a-tag></h2>
          <a-space wrap>
            <a-button v-if="sheet.mayPublish && !applied && !mixed" size="small" :disabled="changed || saving || savingDraft" @click="emit('copy',sheet)">
              复制为新规则
            </a-button>
            <a-button v-if="canResume" size="small" :disabled="changed || saving || savingDraft" @click="openResume">恢复执行</a-button>
          </a-space>
        </header>
        <p v-if="copiedFrom" class="rule-formula">复制自 {{ copiedFrom.label }} v{{ copiedFrom.version }}，按各 SKU 当前的确认时点归期。</p>
        <a-alert v-if="sheet.paused && !applied" class="feedback" type="info" show-icon
          message="当前暂停执行。保存新版本后，可另选账期恢复执行。"/>
        <a-form v-if="canSave && !creating" layout="vertical" size="small" class="rule-basics">
          <a-form-item label="新版本生效账期（北京时间）" html-for="period-rule-effective">
            <a-input id="period-rule-effective" v-model:value="effectiveDate" type="date" :min="firstStart" :max="cutoff" :disabled="saving"/>
          </a-form-item>
        </a-form>
        <a-descriptions size="small" :column="{xs:1,sm:1,md:2}" bordered>
          <a-descriptions-item v-if="!creating" label="结算周期">{{ cycleLabels[sheet.cycle] }}</a-descriptions-item>
          <a-descriptions-item v-if="!creating" label="有效范围">{{ formatTime(sheet.effectiveFrom) }}<template v-if="sheet.effectiveTo"> 至 {{ formatTime(sheet.effectiveTo) }}</template></a-descriptions-item>
          <a-descriptions-item v-if="sheet.resumeFrom && !sheet.paused" label="恢复执行时间" :span="2">{{ formatTime(sheet.resumeFrom) }}</a-descriptions-item>
          <a-descriptions-item v-if="sharedScopeText" label="规则共同范围" :span="2">{{ sharedScopeText }}</a-descriptions-item>
          <a-descriptions-item label="适用 SKU" :span="2">
            <template v-if="creating || editingScenarios">
              <a-select id="period-rule-scenarios" v-model:value="scenarioCodes" :options="scenarioOptions"
                mode="multiple" show-search option-filter-prop="label" aria-label="适用 SKU"
                placeholder="选择参与整期计提的 SKU" :disabled="saving" class="period-scenario-select"/>
              <a-button v-if="!creating" type="link" @click="editingScenarios=false">完成选择</a-button>
            </template>
            <template v-else>
              <a-tag v-for="code in scenarioCodes" :key="code">{{ scenarioOptions.find(option=>option.value===code)?.label || code }}</a-tag>
              <a-button v-if="canSave" type="link" :disabled="saving" @click="editingScenarios=true">编辑适用 SKU</a-button>
            </template>
          </a-descriptions-item>
        </a-descriptions>
        <div class="rule-blocks period-component-layout" aria-label="收款安排">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">收款安排</h3>
            <a-button v-if="canSave" size="small" :disabled="saving" @click="addComponent">添加收款安排</a-button>
          </div>
        <section v-for="item in rows" :key="item.uid" class="rule-block">
          <button type="button" class="rule-block-header" :id="'period-rule-control-'+item.uid" :aria-expanded="activeComponent===item.uid"
            :aria-controls="'period-rule-part-'+item.uid" @click="activeComponent=activeComponent===item.uid ? undefined : item.uid">
            <DownOutlined aria-hidden="true"/>
            <strong>{{ item.label || '新收款安排' }}</strong>
            <span class="rule-block-badge">{{ targetLabels[item.targetKind] }}</span>
            <span v-if="mixed" class="rule-block-badge">{{ item.perOrder ? '逐单' : '整期' }}</span>
            <span class="rule-block-recipient" :title="itemOrderTypes(item)">{{ itemOrderTypes(item,2) }}</span>
            <span class="rule-block-recipient" :title="orderScopeText(item)">{{ orderScopeText(item) }}</span>
            <small>{{ targetLabel(item) }} · {{ methodSummary(item) }}</small>
          </button>
          <div v-if="activeComponent===item.uid" :id="'period-rule-part-'+item.uid" class="rule-block-body">
          <a-form v-if="canSave" layout="vertical" size="small">
            <RuleArrangementFields name-id="period-benefit-label" v-model:name="item.label" :disabled="saving">
              <a-form-item label="收款对象" html-for="period-benefit-target" required>
                <a-select id="period-benefit-target" :value="item.targetKind" :options="item.perOrder ? [orderSeller,...targetTypes] : targetTypes" :disabled="saving" @change="setTarget(item,$event)"/>
              </a-form-item>
              <a-form-item v-if="item.targetKind==='person'" label="指定收款人" html-for="period-benefit-person" required>
                <AppointmentPersonSelect id="period-benefit-person" v-model:value="item.personId" :api="api" endpoint="/catalog-rule-people"
                  :filters="peopleFilters" :current="item.personId ? {id:item.personId,name:item.personName} : undefined"
                  :disabled="saving" @change="(_,option)=>item.personName=option?.name"/>
              </a-form-item>
              <a-form-item v-else-if="item.targetKind==='company'" label="收款公司" html-for="period-benefit-org" required
                :extra="copiedOrganizationUnavailable(item) ? '请重新选择当前有效的收款组织。' : undefined">
                <a-select id="period-benefit-org" v-model:value="item.organizationKey" :options="availableOrganizations(item)"
                  :disabled="saving" show-search option-filter-prop="label"/>
              </a-form-item>
              <template v-if="item.targetKind==='person'">
                <a-form-item>
                  <a-checkbox :checked="!!item.appointment" :disabled="saving"
                    @update:checked="item.appointment=$event ? {} : undefined">仅计提任职期间的订单</a-checkbox>
                </a-form-item>
                <template v-if="item.appointment">
                  <a-form-item label="任职组织" html-for="period-benefit-appointment-unit">
                    <a-select id="period-benefit-appointment-unit" v-model:value="item.appointment.unitId" :options="unitOptions"
                      :disabled="saving" show-search option-filter-prop="label"/>
                  </a-form-item>
                  <a-form-item label="任职岗位" html-for="period-benefit-appointment-role">
                    <a-select id="period-benefit-appointment-role" v-model:value="item.appointment.role" :options="availableRoles(item.appointment.role)"
                      :disabled="saving" show-search option-filter-prop="label"/>
                  </a-form-item>
                </template>
              </template>
              <CatalogOrganizationAppointmentFields v-if="item.targetKind==='appointment'" prefix="period-benefit"
                v-model:level="item.appointmentLevel" v-model:role="item.appointmentRole"
                :roles="availableRoles(item.appointmentRole)" :disabled="saving"/>
              <template v-if="item.targetKind==='pool'">
                <CatalogTeamPoolFields v-model="item.poolId" prefix="period-benefit" :pools="sheet.options?.teamPools"
                  :disabled="saving"
                  :saved-label="[item.poolName,item.organizationName || sheet.items.find(old=>old.itemKey===item.itemKey)?.targetName].filter(Boolean).join(' · ')"
                  :existing="item.legacyPool"/>
                <a-form-item label="成员职责" html-for="period-benefit-member" required>
                  <a-select id="period-benefit-member" v-model:value="item.memberRole" :options="availableRoles(item.memberRole)" :disabled="saving"/>
                </a-form-item>
              </template>
              <template #calculation>
                <a-form-item v-if="mixed" label="计提单位" html-for="period-benefit-basis" required>
                  <a-select id="period-benefit-basis" :value="item.perOrder ? 'order' : 'period'" :options="calculationBasisOptions"
                    :disabled="saving" @change="setCalculationBasis(item,$event)"/>
                </a-form-item>
                <a-form-item label="计提方式" html-for="period-benefit-method" required>
                  <a-select id="period-benefit-method" :value="item.method" :options="item.perOrder ? methodOptions.filter(option=>option.value!=='tiers') : methodOptions" :disabled="saving"
                    @update:value="setMethod(item,$event)"/>
                </a-form-item>
              </template>
              <template #orderType>
                <a-form-item label="计提订单类型" :html-for="scenarioCodes.length>1 || item.scenarioCodes?.length ? 'period-benefit-skus' : undefined">
                  <a-select v-if="scenarioCodes.length>1 || item.scenarioCodes?.length" id="period-benefit-skus"
                    :value="item.scenarioCodes" :options="itemScenarioOptions(item)" :disabled="saving" mode="multiple" allow-clear show-search
                    option-filter-prop="label" :max-tag-count="1" placeholder="全部适用 SKU" @update:value="selectItemSKUs(item,$event)"/>
                  <span v-else>{{ selectedSKUs(item).map(skuLabel).join('、') || '待选择 SKU' }}</span>
                  <a-checkbox v-if="hasMembershipSKU(item) || item.membershipNewOnly" v-model:checked="item.membershipNewOnly"
                    class="period-membership-type" :disabled="saving">会员仅计新报</a-checkbox>
                </a-form-item>
              </template>
              <template #scope>
                <CatalogCommissionScopeFields :prefix="'period-scope-'+item.uid" v-model="item.salesScope"
                  :options="periodScopeUnits" :unit-names="salesUnitNames" :categories="periodScopeCategories" :category-names="salesCategoryNames"
                  :can-own="item.targetKind==='person'" :disabled="saving"/>
              </template>
              <template #standards>
                <RuleCommissionStandards>
                  <RuleStandardValue v-if="item.method==='rate'" id="period-benefit-uniform-rate" v-model="item.uniformRate" :disabled="saving"/>
                  <RuleStandardValue v-else-if="item.method==='fixed'" id="period-benefit-fixed-amount" method="fixed"
                    v-model="item.fixedAmount" :disabled="saving"
                    :label="item.perOrder || !mixed && item.targetKind==='appointment' ? '每笔订单金额（元）' : '整期金额（元）'"/>
                  <CatalogPeriodTiers v-else :tiers="item.tiers" :disabled="saving" :format-money="formatMoney"
                    @remove="removeTier(item.tiers,$event)"/>
                </RuleCommissionStandards>
              </template>
            </RuleArrangementFields>
          </a-form>
          <RuleArrangementFields v-else :order-type-text="itemOrderTypes(item)" :calculation-text="methodSummary(item)" :scope-text="orderScopeText(item)">
            <a-form-item label="收款对象"><span class="rule-field-value">{{ targetLabel(item) }}</span></a-form-item>
          </RuleArrangementFields>
          <a-form v-if="!canSave && item.method==='tiers'" layout="vertical" size="small">
            <RuleCommissionStandards>
              <CatalogPeriodTiers :tiers="item.tiers" :format-money="formatMoney" disabled/>
            </RuleCommissionStandards>
          </a-form>
          <div v-if="canSave && rows.length>1" class="form-actions">
            <a-button type="link" danger size="small" :disabled="saving" @click="removeComponent(item)">移除此安排</a-button>
          </div>
          </div>
        </section>
        </div>
        <template v-if="canReviewActivity">
          <h3 class="form-subhead">订单和退款核对</h3>
          <a-form layout="inline" class="period-activity-query">
            <a-form-item v-if="!creating" label="核对账期" html-for="period-activity-month">
              <a-input id="period-activity-month" v-model:value="activityMonth" type="month"/>
            </a-form-item>
            <a-form-item>
              <a-button v-if="!creating" :disabled="!activityMonth || savingDraft || reviewingActivity"
                :loading="reviewingActivity && activityMode==='published'" @click="readActivity()">读取已同步数据</a-button>
              <a-button v-if="activity?.settlement" @click="emit('open-draft',activity.settlement)">查看结算</a-button>
              <a-button v-else-if="!creating && sheet.paused" :disabled="!activityMonth || reviewingActivity"
                :loading="reviewingActivity && activityMode==='paused'" @click="readActivity('paused')">试算暂停规则</a-button>
              <a-button v-else-if="!creating" :disabled="!activityMonth || reviewingActivity" :loading="savingDraft" @click="openDraft">{{ mixed ? '打开账期草稿' : '打开整期草稿' }}</a-button>
              <a-button v-if="canSave" :disabled="!changed || !effectiveDate || saving || savingDraft || reviewingActivity"
                :loading="reviewingActivity && activityMode==='edits'" @click="readActivity('edits')">用订单试算</a-button>
            </a-form-item>
          </a-form>
          <a-alert v-if="activityError" class="feedback" :message="activityError" type="error" show-icon/>
          <section v-if="activity?.settlement" class="period-activity-result" aria-label="已结算账期">
            <p class="rule-formula" role="status">本账期已结算 · 净分润 {{ formatMoney(activity.settlement.totalMinor) }}</p>
          </section>
          <section v-else-if="activity" class="period-activity-result" aria-label="已同步数据核对结果">
            <p v-if="activityMode==='edits'" class="rule-formula" role="status">按待发布配置试算首个生效账期。</p>
            <p class="rule-formula">{{ formatTime(activity.start) }} 至 {{ formatTime(activity.through) }}（截止时刻不含）</p>
            <div class="period-activity-source">
              <a-tag v-for="source in activity.sources" :key="source.stream" :color="source.status==='READY'?'green':'orange'">
                {{ optionLabel('dcaiOrderStream',source.stream) || source.stream }} · {{ sourceStatusLabels[source.status] }}
              </a-tag>
              <a-tag v-if="!activity.sources.length" color="orange">缺少可核对的来源映射</a-tag>
            </div>
            <div class="period-activity-table">
              <table>
                <thead><tr><th>场景</th><th>已匹配收款</th><th>已匹配退款</th><th>其中跨期原付款退款</th></tr></thead>
                <tbody>
                  <tr v-for="scope in activity.scenarios" :key="scope.code">
                    <th scope="row">{{ scenarioOptions.find(option=>option.value===scope.code)?.label || scope.code }}
                      <small v-if="!scope.mapped" class="order-version">本期缺少映射</small>
                    </th>
                    <td>
                      <template v-if="scope.mapped || scope.paymentCount">{{ formatMoney(scope.receiptsMinor) }} <small>· {{ scope.paymentCount }} 笔</small></template>
                      <template v-else>—</template>
                    </td>
                    <td>
                      <template v-if="scope.mapped || scope.refundCount">{{ formatMoney(scope.refundsMinor) }} <small>· {{ scope.refundCount }} 笔</small></template>
                      <template v-else>—</template>
                    </td>
                    <td>{{ scope.mapped || scope.refundCount ? formatMoney(scope.priorPeriodRefundsMinor) : '—' }}</td>
                  </tr>
                  <tr><th scope="row">已匹配合计</th><td>{{ formatMoney(activity.receiptsMinor) }}</td>
                    <td>{{ formatMoney(activity.refundsMinor) }}</td><td>{{ formatMoney(activity.priorPeriodRefundsMinor) }}</td></tr>
                </tbody>
                <tfoot v-if="activity.outsidePerformanceScope" class="period-activity-excluded">
                  <tr>
                    <th scope="row">业绩范围外</th>
                    <td>{{ formatMoney(activity.outsidePerformanceScope.receiptsMinor) }} <small>· {{ activity.outsidePerformanceScope.paymentCount }} 笔</small></td>
                    <td>{{ formatMoney(activity.outsidePerformanceScope.refundsMinor) }} <small>· {{ activity.outsidePerformanceScope.refundCount }} 笔</small></td>
                    <td>—</td>
                  </tr>
                </tfoot>
              </table>
            </div>
            <ul v-if="Object.keys(activity.issues).length" class="period-activity-issues" aria-label="待核交易">
              <li v-for="(count,issue) in activity.issues" :key="issue">{{ activityIssueLabels[issue] || issue }} · {{ count }} 笔</li>
            </ul>
            <section v-if="activity.grouping" aria-label="按原付款分组">
              <h4 class="form-subhead">按原付款分组</h4>
              <p v-if="activity.grouping.paused && !activity.grouping.trialOnPaused" class="rule-formula">规则包已暂停，本次仅核对场景收退款。</p>
              <template v-else>
                <p v-if="activity.grouping.trialOnPaused" class="rule-formula">
                  {{ activityMode==='edits' ? '规则包目前暂停。' : '按已发布的暂停规则试算，规则仍保持暂停。' }}
                </p>
                <p v-if="activity.grouping.priorUnsettled" class="rule-formula" role="status">上一账期尚未确认，暂显示结转前试算。</p>
                <div v-if="activity.grouping.groups.length" class="period-activity-table">
                  <table>
                    <thead><tr><th>规则版本</th><th>收款对象</th><th>安排实收</th><th>安排退款</th><th>计提净额</th>
                      <th v-if="showsPriorCarry">上期结转退款</th>
                      <th>{{ activity.grouping.priorUnsettled ? '结转前试算' : '分润试算' }}</th><th>明细</th></tr></thead>
                    <tbody>
                      <tr v-for="group in activity.grouping.groups" :key="group.id">
                        <td>{{ group.ruleId ? 'v'+group.version : '待发布' }}
                          <small v-if="group.perOrder || group.fixedOnly" class="order-version">{{ group.perOrder ? '逐单计提' : group.fixedPerOrder ? '按订单固定金额' : '整期固定' }}</small></td>
                        <td>
                          <div v-for="target in group.targets" :key="target.itemKey">
                            {{ target.name }}
                            <small v-if="target.organizationName" class="order-version">{{ target.organizationName }}</small>
                            <small v-if="target.ownerName" class="order-version">{{ target.ownerName }}负责 · {{ target.memberCount }} 人可分配</small>
                          </div>
                        </td>
                        <td>{{ formatMoney(group.receiptsMinor) }}</td>
                        <td>{{ formatMoney(group.refundsMinor) }}</td>
                        <td>{{ formatMoney(group.netMinor) }}</td>
                        <td v-if="showsPriorCarry">{{ formatMoney(-(group.carryInMinor || 0)) }}</td>
                        <td>
                          <small v-if="group.fixedOnly && !group.fixedPerOrder && !group.perOrder" class="order-version">
                            可计提净额 {{ formatMoney((group.afterCarry || group.beforeCarry).basisMinor) }}
                          </small>
                          <div v-for="award in (group.afterCarry || group.beforeCarry).awards" :key="award.itemKey">
                            {{ formatMoney(award.amountMinor) }} <small>· {{ award.label }}<template v-if="!award.fixed"> {{ award.rateBps/100 }}%</template></small>
                          </div>
                          <small v-if="(group.afterCarry || group.beforeCarry).carryForwardMinor<0" class="order-version">
                            {{ activity.grouping.priorUnsettled ? '本期未抵扣退款' : '转下期退款' }} {{ formatMoney(-(group.afterCarry || group.beforeCarry).carryForwardMinor) }}
                          </small>
                        </td>
                        <td>
                          <button v-if="group.eventToken" type="button" class="order-link" @click="activityGroup=group">查看收退款</button>
                          <span v-else>—</span>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <p v-else class="rule-formula">本期没有可归组的交易。</p>
                <ul v-if="Object.keys(activity.grouping.issues).length" class="period-activity-issues" aria-label="待核分组">
                  <li v-for="issue in activity.grouping.targetIssues || []" :key="issue.ruleId+'-'+issue.itemKey+'-'+issue.code">
                    <strong>{{ issue.label }}</strong> · {{ issue.ruleId ? 'v'+issue.version : '待发布' }} · {{ groupingIssueLabels[issue.code] || issue.code }} · {{ issue.count }} 笔
                    <a-button v-if="issue.ruleId || targetIssueRow(issue)" type="link" size="small"
                      :disabled="saving || (issueInOtherVersion(issue) ? changed : !targetIssueRow(issue))"
                      @click="followTargetIssue(issue)">{{ issueInOtherVersion(issue) ? '查看规则 v'+issue.version : canSave ? '编辑收款安排' : '查看收款安排' }}</a-button>
                  </li>
                  <li v-for="[issue,count] in otherGroupingIssues" :key="issue">{{ groupingIssueLabels[issue] || issue }} · {{ count }} 笔</li>
                </ul>
              </template>
            </section>
          </section>
        </template>
        <template v-if="!applied">
        <h3 class="form-subhead">金额演算</h3>
        <p v-if="!scopedQuote" class="rule-formula">填写汇总金额，演算各收款安排的计提金额。</p>
        <a-form layout="vertical">
          <div class="period-quote-inputs">
            <a-form-item v-if="scopedQuote" label="演算收款安排" html-for="period-quote-item">
              <a-select id="period-quote-item" v-model:value="quoteItem" placeholder="选择后填写该安排的收退款"
                :options="rows.map(item=>({value:item.uid,label:item.label || '未命名收款安排'}))"/>
            </a-form-item>
            <template v-if="!quoteUsesOrders">
            <a-form-item label="本期实收（元）" html-for="period-quote-received">
              <a-input-number id="period-quote-received" v-model:value="amounts.receipts" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="本期退款（元）" html-for="period-quote-refunds">
              <a-input-number id="period-quote-refunds" v-model:value="amounts.refunds" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="上期未抵扣退款（元）" html-for="period-quote-carry">
              <a-input-number id="period-quote-carry" v-model:value="amounts.carry" :min="0" :precision="2"/>
            </a-form-item>
            </template>
          </div>
          <p v-if="quoteUsesOrders" class="rule-formula">按订单计提，请使用订单试算。</p>
          <a-button v-else :loading="previewing" @click="preview">演算金额</a-button>
        </a-form>
        <a-descriptions v-if="quote" size="small" :column="1" bordered class="period-quote-result" aria-label="金额演算结果">
          <a-descriptions-item label="可计提净额">{{ formatMoney(quote.basisMinor) }}</a-descriptions-item>
          <a-descriptions-item v-for="award in quote.awards" :key="award.itemKey"
            :label="award.label || sheet.items.find(item=>item.itemKey===award.itemKey)?.label">{{ award.fixed ? '整期固定' : award.rateBps/100+'%' }} · {{ formatMoney(award.amountMinor) }}</a-descriptions-item>
          <a-descriptions-item label="下期结转退款">{{ formatMoney(-quote.carryForwardMinor) }}</a-descriptions-item>
        </a-descriptions>
        </template>
      </section>
    </a-spin>
    <DraftAggregateOrders v-if="activityGroup" :key="activityGroup.id" :group="activityGroup.id" :load-page="loadActivityEvents"
      :format-money="formatMoney" :format-time="formatTime" @close="activityGroup=undefined" @show-order="showActivityOrder"/>
    <template #footer>
      <div class="workspace-edit-footer">
        <a-button :disabled="saving" @click="emit('close')">关闭</a-button>
        <a-button v-if="canSave" type="primary" :disabled="!changed" :loading="saving" @click="publish">{{ creating ? '保存并生效' : '保存新版本' }}</a-button>
      </div>
    </template>
    <a-modal v-if="resumeDate" open title="恢复整期规则" ok-text="确认恢复" cancel-text="返回" :confirm-loading="resuming"
      :mask-closable="!resuming" :closable="!resuming" :keyboard="!resuming" @cancel="!resuming && (resumeDate='')" @ok="resumeRule">
      <a-alert v-if="resumeError" type="error" show-icon class="feedback" :message="resumeError"/>
      <p>{{ sheet.label }} · v{{ sheet.version }}</p>
      <a-form layout="vertical">
        <a-form-item label="恢复账期起点（北京时间）" html-for="period-resume-date" required>
          <a-input id="period-resume-date" :value="resumeDate" type="date" :min="firstResumeDate" :max="cutoff" :disabled="resuming"
            @update:value="resumeDate=$event || firstResumeDate"/>
        </a-form-item>
      </a-form>
    </a-modal>
  </a-drawer>
</template>

<style scoped>
.period-component-layout { margin:8px 0 12px; }
.period-scenario-select { width:min(100%,720px); }
.period-membership-type { display:block; margin-top:4px; }
.period-component-heading { display:flex; justify-content:space-between; align-items:center; gap:4px 8px; flex-wrap:wrap; margin-bottom:6px; }
.period-component-heading h2 { margin:0; font-size:16px; }
.period-quote-inputs :deep(.ant-input-number) { width:100%; }
.period-appointment-caption { max-width:100%; white-space:normal; vertical-align:middle; }
.period-quote-inputs { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
.period-quote-result { margin-top:12px; }
.period-activity-query,.period-activity-result { margin-bottom:14px; }
.period-activity-source { margin-bottom:8px; }
.period-activity-table { overflow-x:auto; }
.period-activity-table table { width:100%; min-width:520px; border-collapse:collapse; font-size:13px; }
.period-activity-table th,.period-activity-table td { padding:8px; border-bottom:1px solid var(--line); text-align:left; }
.period-activity-table th { font-weight:500; }
.period-activity-table td { white-space:nowrap; font-variant-numeric:tabular-nums; }
.period-activity-table td small { color:var(--muted); }
.period-activity-excluded { color:var(--muted); }
.period-activity-issues { margin:8px 0 0; padding-inline-start:18px; color:var(--muted); }
@media (max-width:540px) { .period-quote-inputs { grid-template-columns:1fr; gap:0; } }
</style>

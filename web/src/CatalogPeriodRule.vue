<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { DownOutlined } from '@ant-design/icons-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({rule:Object, applied:Object, products:Array, api:Function, formatMoney:Function, formatTime:Function, optionLabel:Function, canReviewActivity:Boolean})
const emit = defineEmits(['close','published','open-draft','copy','open-version','resumed'])
const creating = computed(() => Boolean(props.rule?.create))
const copiedFrom = computed(() => props.rule?.copiedFrom)
const productId = ref(props.rule?.productId || (props.products?.length===1 ? props.products[0].value : undefined))
const ruleName = ref('')
const creationCycle = ref()
const sheet = ref()
const rows = ref([])
const activeComponent = ref()
const editingComponent = ref()
const scenarioCodes = ref([])
const editingScenarios = ref(false)
const performanceScope = ref({unitIds:[]})
const appointmentName = ref('')
const editingPerformance = ref(false)
let newComponentID = 0
const effectiveDate = ref('')
const resumeDate = ref('')
const resumeError = ref('')
const resuming = ref(false)
const amounts = ref({receipts:0,refunds:0,carry:0})
const loading = ref(false)
const saving = ref(false)
const previewing = ref(false)
const error = ref('')
const quote = ref()
const activityMonth = ref(props.rule?.reviewMonth || new Date(Date.now()+8*3600000).toISOString().slice(0,7))
const reviewingActivity = ref(false)
const activity = ref()
const activityMode = ref()
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
  REFUND_ADJUSTMENT:'退款更正待核对', INVALID_EVENT:'交易编号或金额缺失', DUPLICATE_EVENT:'交易编号重复',
  REFUND_ORIGIN_MISSING:'退款未能唯一关联原付款', UNCLASSIFIED:'原付款未分类', MAPPING_MISSING:'缺少场景映射',
  MAPPING_CONFLICT:'场景映射冲突', INVALID_PAYMENT:'原付款信息不完整', REFUND_EXCEEDS_PAYMENT:'累计退款超过原付款',
  SALES_PERSON_MISSING:'原付款缺少销售人员', SALES_PERSON_CONFLICT:'销售人员无法唯一确定',
  SALES_SOURCE_CONFLICT:'订单销售归属冲突', SALES_COMPANY_CONFLICT:'销售公司无法唯一确定',
  AGGREGATE_SALES_ORGANIZATION_MISSING:'原付款缺少有效的部门或小组归属',
  SALES_ORGANIZATION_SCOPE_CONFLICT:'付款时的销售归属存在重叠，无法确认是否计入所选范围',
}
const groupingIssueLabels = {
  ORIGINAL_RULE_MISSING:'原付款时没有适用规则',
  ORIGINAL_RULE_CONFLICT:'原付款时有多个适用版本',
  BENEFIT_ORG_MISSING:'缺少收款组织映射',
  BENEFIT_ORG_INACTIVE:'收款组织当前停用',
  BENEFIT_ORG_CONFLICT:'收款组织无法唯一确定',
  BENEFIT_PERSON_MISSING:'找不到指定收款人',
  BENEFIT_POOL_OWNER_MISSING:'付款时缺少池负责人',
  BENEFIT_POOL_OWNER_CONFLICT:'付款时有多位池负责人',
  BENEFIT_POOL_MEMBERS_MISSING:'付款时没有可分配成员',
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
const firstStart = computed(() => sheet.value && !creating.value ? nextStart(sheet.value.effectiveFrom,sheet.value.cycle) : undefined)
const cutoff = computed(() => sheet.value?.effectiveTo
  ? localDate(new Date(Date.parse(sheet.value.effectiveTo)-86400000).toISOString()) : undefined)
const canSave = computed(() => sheet.value?.mayPublish && (!cutoff.value || firstStart.value<=cutoff.value))
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
const performanceChanged = computed(() => JSON.stringify(performanceScope.value) !== JSON.stringify(performanceForm(sheet.value?.performanceScope)))
const changed = computed(() => creating.value ? Boolean(ruleName.value.trim() && scenarioCodes.value.length && rows.value.length)
  : componentsChanged.value || scenariosChanged.value || performanceChanged.value)
const scenarioOptions = computed(() => {
  const label = sku => sku.label+(sku.requiresCompletion ? ' · 完成后计提' : '')
  const available = new Map((sheet.value?.options?.scenarios || []).map(sku=>[sku.code,{value:sku.code,label:label(sku)}]))
  for (const sku of copiedFrom.value?.scenarios || sheet.value?.scenarios || []) {
    if (!available.has(sku.code) && (!copiedFrom.value || scenarioCodes.value.includes(sku.code))) {
      available.set(sku.code,{value:sku.code,label:copiedFrom.value ? sku.label+'（当前不可选）'
        : sku.mapped ? label(sku) : sku.label+'（尚未映射）',disabled:!copiedFrom.value && sku.locked})
    }
  }
  return [...available.values()]
})

function editableRows(items) {
  return items.map(item => ({uid:item.itemKey,itemKey:item.itemKey,label:item.label,targetKind:item.targetKind,
    organizationKey:item.organizationKey,personId:item.personId,personName:item.personId ? item.targetName : undefined,
    poolName:item.poolName || '',ownerRole:item.ownerRole || '',memberRole:item.memberRole || '',
    method:item.tiers.length===1 ? 'rate' : 'tiers',
    uniformRate:item.tiers.length===1 ? item.tiers[0].rate_bps/100 : undefined,
    tiers:item.tiers.length===1 ? [] : item.tiers.map(row=>({from:(row.min_inclusive || 0)/100,rate:row.rate_bps/100}))}))
}
function configuredRow({method,uniformRate,tiers,...item}) {
  return {...item,tiers:method==='rate' ? [{from:0,rate:uniformRate}] : tiers}
}
const methodLabels = {rate:'统一比例',tiers:'按净额阶梯'}
function setMethod(item, method) {
  if (method==='tiers' && !item.tiers.length) item.tiers = [{from:0,rate:item.uniformRate}]
  if (method==='rate' && item.uniformRate==null && item.tiers.length===1) item.uniformRate = item.tiers[0].rate
  item.method = method
}
function methodSummary(item) {
  if (item.method==='tiers') return item.tiers.length+' 档阶梯'
  return item.uniformRate==null ? methodLabels.rate : '统一 '+item.uniformRate+'%'
}
const targetTypes = [{value:'person',label:'指定个人'},{value:'company',label:'指定公司'},{value:'pool',label:'团队池'}]
const targetLabels = {person:'个人',company:'公司',pool:'团队池'}
const peopleFilters = computed(() => creating.value ? {productId:productId.value,scope:'period'} : {ruleId:props.rule?.id,scope:'period'})
function performanceForm(scope) {
  const duty = scope?.appointment
  return {unitIds:[...(scope?.unitIds || [])],appointment:duty ? {personId:duty.personId,unitId:duty.unitId,role:duty.role} : undefined}
}
const unitOptions = computed(() => {
  const choices = new Map((sheet.value?.options?.organizations || []).map(unit=>[unit.unitId,{value:unit.unitId,label:unit.label,kind:unit.kind}]))
  for (const [id,label] of Object.entries(copiedFrom.value?.scopeLabels?.units || sheet.value?.scopeLabels?.units || {})) {
    if (!choices.has(id)) choices.set(id,{value:id,label})
  }
  return Array.from(choices.values())
})
const sourceUnitOptions = computed(() => unitOptions.value.filter(unit=>
  ['DEPARTMENT','TEAM'].includes(unit.kind) || performanceScope.value.unitIds.includes(unit.value)))
const unitLabel = id => unitOptions.value.find(unit=>unit.value===id)?.label || '组织待核'
function performanceChanges() {
  if (!performanceChanged.value) return {}
  const {unitIds,appointment} = performanceScope.value
  if (appointment && (!appointment.personId || !appointment.unitId || !appointment.role)) {
    throw new Error('请选择人员、任职组织和岗位。')
  }
  return {performanceScope:{unitIds,appointment}}
}
function targetLabel(item) {
  if (item.targetKind==='person') return item.personName || '待选择收款人'
  const name = availableOrganizations(item).find(org=>org.value===item.organizationKey)?.label ||
    sheet.value.items.find(old=>old.itemKey===item.itemKey)?.targetName
  return [item.poolName,name].filter(Boolean).join(' · ') || '待选择收款对象'
}
function availableOrganizations(row) {
  const options = (sheet.value?.options?.organizations || []).filter(item=>row.targetKind==='pool' || item.kind==='COMPANY')
  if (!row.organizationKey || options.some(item=>item.value===row.organizationKey)) return options
  const original = sheet.value.items.find(item=>item.itemKey===row.itemKey)
  return [...options,{value:row.organizationKey,label:original?.targetName || row.organizationName || row.organizationKey}]
}
const copiedOrganizationUnavailable = row => copiedFrom.value && row.organizationKey &&
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
  row.memberRole = ''
  row.poolName = ''
}
function addComponent() {
  const uid = 'new-' + (++newComponentID)
  rows.value.push({uid,label:'',targetKind:'company',method:'rate',tiers:[]})
  activeComponent.value = uid
  editingComponent.value = uid
}
function initializeCreation(data, product) {
  const copy = copiedFrom.value
  creationCycle.value = copy?.cycle || data.cycle
  effectiveDate.value = nextStart(new Date().toISOString(),creationCycle.value)
  if (copy) {
    scenarioCodes.value = copy.scenarios.map(sku=>sku.code)
    performanceScope.value = performanceForm(copy.performanceScope)
    appointmentName.value = copy.scopeLabels?.person || ''
    rows.value = editableRows(copy.items).map(({itemKey,...row}, index)=>({
      ...row,uid:'new-'+(++newComponentID),organizationName:copy.items[index].targetName,
    }))
    activeComponent.value = rows.value[0]?.uid
    editingComponent.value = activeComponent.value
    return
  }
  const options = data.options.scenarios
  const selected = product===props.rule.productId && options.find(option=>option.code===props.rule.sku)
  scenarioCodes.value = selected ? [selected.code] : options.length===1 ? [options[0].code] : []
  addComponent()
}
function removeComponent(row) {
  const index = rows.value.indexOf(row)
  rows.value.splice(index,1)
  activeComponent.value = rows.value[Math.min(index,rows.value.length-1)].uid
  editingComponent.value = undefined
}
async function openArrangement(itemKey) {
  const item = rows.value.find(row=>row.itemKey===itemKey)
  if (!item) return
  activeComponent.value = item.uid
  editingComponent.value = canSave.value ? item.uid : undefined
  await nextTick()
  const heading = document.getElementById('period-rule-control-'+item.uid)
  heading?.scrollIntoView({block:'start'})
  heading?.focus({preventScroll:true})
}
function followTargetIssue(issue) {
  if (!issue.ruleId || issue.ruleId===props.rule.id) return openArrangement(issue.itemKey)
  emit('open-version',{ruleId:issue.ruleId,itemKey:issue.itemKey,month:activityMonth.value})
}
function minor(value, message) {
  if (value == null || !Number.isSafeInteger(Math.round(value*100)) || value < 0) throw new Error(message)
  return Math.round(value*100)
}
function componentChanges() {
  if (!canSave.value || (!creating.value && !componentsChanged.value)) return {}
  return {components:rows.value.map(item=>{
    if (!item.label.trim() || (item.targetKind==='person' ? !item.personId : !item.organizationKey)) {
      throw new Error('请填写收款安排名称并选择收款对象。')
    }
    if (item.targetKind==='pool' && (!item.poolName?.trim() || !item.ownerRole || !item.memberRole)) {
      throw new Error('请填写团队池名称、负责人职责和成员职责。')
    }
    const {tiers} = configuredRow(item)
    const starts = tiers.map(row=>minor(row.from,'请输入每档净额起点。'))
    if (starts.some((from,index)=>index && from<=starts[index-1])) throw new Error('每一档净额起点须高于上一档。')
    return {itemKey:item.itemKey,label:item.label,targetKind:item.targetKind,organizationKey:item.organizationKey,
      personId:item.personId,poolName:item.poolName,ownerRole:item.ownerRole,memberRole:item.memberRole,tiers:tiers.map((row,index)=>{
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
    performanceScope.value = performanceForm(data.performanceScope)
    appointmentName.value = data.scopeLabels?.person || ''
    editingPerformance.value = false
    activeComponent.value = rows.value.length===1 ? rows.value[0].uid : undefined
    editingComponent.value = undefined
    if (creating.value) initializeCreation(data,product)
    else effectiveDate.value = canSave.value ? firstStart.value : ''
    if (props.rule?.focusItemKey) void openArrangement(props.rule.focusItemKey)
  } catch (err) {
    if (request === versionRequest) error.value = err.message
  } finally {
    if (request === versionRequest) loading.value = false
  }
}, {immediate:true})
watch(creationCycle, cycle=>{
  if (creating.value && cycle) effectiveDate.value = nextStart(new Date().toISOString(),cycle)
})
watch([rows,amounts,scenarioCodes,creationCycle,effectiveDate,ruleName,performanceScope], ()=>{
  ++quoteRequest
  previewing.value = false
  quote.value = undefined
}, {deep:true})
function clearActivity() {
  ++activityRequest
  activity.value = undefined
  activityMode.value = undefined
  activityError.value = ''
  reviewingActivity.value = false
}
watch([activityMonth,()=>props.rule?.id], clearActivity)
watch([rows,scenarioCodes,creationCycle,effectiveDate,ruleName,performanceScope], ()=>{
  if (activityMode.value==='edits') clearActivity()
},{deep:true})

async function readActivity(mode = 'published') {
  const request = ++activityRequest
  reviewingActivity.value = true
  activityError.value = ''
  activity.value = undefined
  activityMode.value = mode
  try {
    let result
    if (mode==='edits') {
      result = await props.api('/catalog-period-activity',{method:'POST',body:JSON.stringify(versionInput())})
    } else {
      const query = new URLSearchParams({ruleId:props.rule.id,month:activityMonth.value})
      if (mode==='paused') query.set('trialPaused','1')
      result = await props.api('/catalog-period-activity?'+query)
    }
    if (request === activityRequest) activity.value = result
  } catch (err) {
    if (request === activityRequest) activityError.value = err.message
  } finally {
    if (request === activityRequest) reviewingActivity.value = false
  }
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
    const result = await props.api('/catalog-period-rules/preview',{method:'POST',body:JSON.stringify({
      ...(creating.value ? creationInput() : {baseId:props.rule.id,...componentChanges(),...performanceChanges()}),
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
function creationInput() {
  if (!changed.value || !effectiveDate.value) throw new Error('请填写规则名称、生效日期，并选择参与场景。')
  const selectable = new Set(sheet.value.options.scenarios.map(sku=>sku.code))
  if (scenarioCodes.value.some(code=>!selectable.has(code))) throw new Error('请移除当前不可选的 SKU，并核对参与场景。')
  return {create:{productId:productId.value,label:ruleName.value.trim(),cycle:creationCycle.value},
    effectiveFrom:new Date(effectiveDate.value+'T00:00:00+08:00').toISOString(),scenarioCodes:scenarioCodes.value,
    ...componentChanges(),...performanceChanges()}
}
function versionInput() {
  if (creating.value) return creationInput()
  if (!scenarioCodes.value.length) throw new Error('请至少选择一个参与场景。')
  if (!effectiveDate.value) throw new Error('请选择新版本的生效日期。')
  return {baseId:props.rule.id,effectiveFrom:new Date(effectiveDate.value+'T00:00:00+08:00').toISOString(),
    ...componentChanges(),...performanceChanges(),
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
  <a-drawer open :title="copiedFrom ? '复制整期规则' : creating ? '新增整期规则' : applied ? '计提使用的整期规则' : '整期分润配置'" width="min(1260px, 100vw)" root-class-name="finance-rule-editor"
    :closable="!saving && !savingDraft" :mask-closable="!saving && !savingDraft" :keyboard="!saving && !savingDraft" @close="emit('close')">
    <a-alert v-if="error" class="feedback" :message="error" type="error" show-icon/>
    <a-form v-if="creating" layout="vertical" class="rule-basics" :disabled="saving">
      <a-form-item label="所属产品" html-for="period-create-product" required>
        <a-select id="period-create-product" v-model:value="productId" :options="products" :disabled="!!copiedFrom" placeholder="选择产品"/>
      </a-form-item>
      <a-form-item label="规则名称" html-for="period-create-name" required>
        <a-input id="period-create-name" v-model:value="ruleName" :maxlength="80"/>
      </a-form-item>
      <a-form-item label="结算周期" html-for="period-create-cycle" required>
        <a-select id="period-create-cycle" v-model:value="creationCycle" :options="cycleOptions" :disabled="!sheet"/>
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
            <a-button v-if="sheet.mayPublish && !applied" size="small" :disabled="changed || saving || savingDraft" @click="emit('copy',sheet)">
              复制为新规则
            </a-button>
            <a-button v-if="canResume" size="small" :disabled="changed || saving || savingDraft" @click="openResume">恢复执行</a-button>
          </a-space>
        </header>
        <p v-if="copiedFrom" class="rule-formula">复制自 {{ copiedFrom.label }} v{{ copiedFrom.version }}，按各 SKU 当前的确认时点归期。</p>
        <a-alert v-if="sheet.paused && !applied" class="feedback" type="info" show-icon
          message="当前暂停执行。保存新版本后，可另选账期恢复执行。"/>
        <a-descriptions size="small" :column="{xs:1,sm:1,md:2}" bordered>
          <a-descriptions-item v-if="!creating" label="结算周期">{{ cycleLabels[sheet.cycle] }}</a-descriptions-item>
          <a-descriptions-item v-if="!creating" label="有效范围">{{ formatTime(sheet.effectiveFrom) }}<template v-if="sheet.effectiveTo"> 至 {{ formatTime(sheet.effectiveTo) }}</template></a-descriptions-item>
          <a-descriptions-item v-if="sheet.resumeFrom && !sheet.paused" label="恢复执行时间" :span="2">{{ formatTime(sheet.resumeFrom) }}</a-descriptions-item>
          <a-descriptions-item label="参与场景" :span="2">
            <template v-if="creating || editingScenarios">
              <a-select id="period-rule-scenarios" v-model:value="scenarioCodes" :options="scenarioOptions"
                mode="multiple" show-search option-filter-prop="label" aria-label="参与场景"
                placeholder="选择参与整期计提的 SKU" :disabled="saving" class="period-scenario-select"/>
              <a-button v-if="!creating" type="link" @click="editingScenarios=false">完成选择</a-button>
            </template>
            <template v-else>
              <a-tag v-for="code in scenarioCodes" :key="code">{{ scenarioOptions.find(option=>option.value===code)?.label || code }}</a-tag>
              <a-button v-if="canSave" type="link" :disabled="saving" @click="editingScenarios=true">编辑参与场景</a-button>
            </template>
          </a-descriptions-item>
          <a-descriptions-item label="业绩范围" :span="2">
            <template v-if="editingPerformance">
              <a-form layout="vertical" class="rule-basics">
                <a-form-item label="部门、小组" html-for="period-scope-units">
                  <a-select id="period-scope-units" v-model:value="performanceScope.unitIds" :options="sourceUnitOptions"
                    mode="multiple" show-search option-filter-prop="label" aria-label="部门、小组" placeholder="不限部门和小组" :disabled="saving"/>
                </a-form-item>
                <a-form-item>
                  <a-checkbox :checked="!!performanceScope.appointment" :disabled="saving"
                    @update:checked="performanceScope.appointment=$event ? {} : undefined">仅在任职期间计提</a-checkbox>
                </a-form-item>
                <template v-if="performanceScope.appointment">
                  <a-form-item label="任职人员" html-for="period-scope-person">
                    <AppointmentPersonSelect id="period-scope-person" v-model:value="performanceScope.appointment.personId"
                      :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters"
                      :current="performanceScope.appointment.personId ? {id:performanceScope.appointment.personId,name:appointmentName} : undefined"
                      :disabled="saving" @change="(_,option)=>appointmentName=option.name"/>
                  </a-form-item>
                  <a-form-item label="任职组织" html-for="period-scope-appointment-unit">
                    <a-select id="period-scope-appointment-unit" v-model:value="performanceScope.appointment.unitId" :options="unitOptions"
                      show-search option-filter-prop="label" :disabled="saving"/>
                  </a-form-item>
                  <a-form-item label="任职岗位" html-for="period-scope-appointment-role">
                    <a-select id="period-scope-appointment-role" v-model:value="performanceScope.appointment.role"
                      :options="availableRoles(performanceScope.appointment.role)" :disabled="saving"/>
                  </a-form-item>
                </template>
              </a-form>
              <a-button type="link" @click="editingPerformance=false">完成选择</a-button>
            </template>
            <template v-else>
              <a-tag v-for="id in performanceScope.unitIds" :key="id">{{ unitLabel(id) }}</a-tag>
              <span v-if="!performanceScope.unitIds.length">全部部门和小组</span>
              <a-tag v-if="performanceScope.appointment" class="period-appointment-caption">{{ appointmentName || '人员待核' }} ·
                {{ unitLabel(performanceScope.appointment.unitId) }} ·
                {{ optionLabel('catalogAppointmentRole',performanceScope.appointment.role) || '岗位待选' }}任职期间</a-tag>
              <a-button v-if="canSave" type="link" :disabled="saving" @click="editingPerformance=true">编辑业绩范围</a-button>
            </template>
          </a-descriptions-item>
        </a-descriptions>
        <div class="rule-blocks period-component-layout" aria-label="收款安排">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">收款安排</h3>
            <a-button v-if="canSave" :disabled="saving" @click="addComponent">添加收款安排</a-button>
          </div>
        <section v-for="item in rows" :key="item.uid" class="rule-block">
          <button type="button" class="rule-block-header" :id="'period-rule-control-'+item.uid" :aria-expanded="activeComponent===item.uid"
            :aria-controls="'period-rule-part-'+item.uid" @click="activeComponent=activeComponent===item.uid ? undefined : item.uid">
            <DownOutlined aria-hidden="true"/>
            <strong>{{ item.label || '新收款安排' }}</strong>
            <span class="rule-block-badge">{{ targetLabels[item.targetKind] }}</span>
            <small>{{ targetLabel(item) }} · {{ methodSummary(item) }}</small>
          </button>
          <div v-if="activeComponent===item.uid" :id="'period-rule-part-'+item.uid" class="rule-block-body">
          <div class="period-component-heading">
            <h4 class="rule-item-section">发给谁</h4>
            <div v-if="canSave">
              <a-button v-if="editingComponent!==item.uid" type="link" :disabled="saving" @click="editingComponent=item.uid">编辑收款对象</a-button>
              <a-button v-else type="link" :disabled="saving" @click="editingComponent=undefined">收起编辑</a-button>
              <a-button v-if="rows.length>1" type="link" danger :disabled="saving" @click="removeComponent(item)">移除此安排</a-button>
            </div>
          </div>
          <a-form v-if="canSave && editingComponent===item.uid" layout="vertical" class="rule-basics">
            <a-form-item label="收款安排名称" html-for="period-benefit-label">
              <a-input id="period-benefit-label" v-model:value="item.label" :maxlength="80" :disabled="saving"/>
            </a-form-item>
            <a-form-item label="收款对象" html-for="period-benefit-target">
              <a-select id="period-benefit-target" :value="item.targetKind" :options="targetTypes" :disabled="saving" @change="setTarget(item,$event)"/>
            </a-form-item>
            <a-form-item v-if="item.targetKind==='person'" label="收款人" html-for="period-benefit-person">
              <AppointmentPersonSelect id="period-benefit-person" v-model:value="item.personId" :api="api" endpoint="/catalog-rule-people"
                :filters="peopleFilters" :current="item.personId ? {id:item.personId,name:item.personName} : undefined"
                :disabled="saving" @change="(_,option)=>item.personName=option.name"/>
            </a-form-item>
            <a-form-item v-else :label="item.targetKind==='pool' ? '归集组织' : '收款公司'" html-for="period-benefit-org"
              :extra="copiedOrganizationUnavailable(item) ? '请重新选择当前有效的收款组织。' : undefined">
              <a-select id="period-benefit-org" v-model:value="item.organizationKey" :options="availableOrganizations(item)"
                :disabled="saving" show-search option-filter-prop="label"/>
            </a-form-item>
            <template v-if="item.targetKind==='pool'">
              <a-form-item label="团队池名称" html-for="period-benefit-pool">
                <a-input id="period-benefit-pool" v-model:value="item.poolName" :maxlength="80" :disabled="saving"/>
              </a-form-item>
              <a-form-item label="负责人职责" html-for="period-benefit-owner">
                <a-select id="period-benefit-owner" v-model:value="item.ownerRole" :options="availableRoles(item.ownerRole)" :disabled="saving"/>
              </a-form-item>
              <a-form-item label="成员职责" html-for="period-benefit-member">
                <a-select id="period-benefit-member" v-model:value="item.memberRole" :options="availableRoles(item.memberRole)" :disabled="saving"/>
              </a-form-item>
            </template>
          </a-form>
          <template v-else>
            <p class="rule-formula">{{ targetLabel(item) }}</p>
            <p v-if="item.ownerRole" class="rule-formula">负责人 {{ optionLabel('catalogAppointmentRole',item.ownerRole) || item.ownerRole }} · 成员 {{ optionLabel('catalogAppointmentRole',item.memberRole) || item.memberRole || '未配置' }}</p>
          </template>
          <h4 class="rule-item-section">提成怎么算</h4>
          <div class="period-method">
            <a-radio-group v-if="canSave" :value="item.method" aria-label="计提方式" role="radiogroup" size="small" button-style="solid"
              :disabled="saving" @update:value="setMethod(item,$event)">
              <a-radio-button v-for="(label,key) in methodLabels" :key="key" :value="key">{{ label }}</a-radio-button>
            </a-radio-group>
            <a-form v-if="item.method==='rate' && canSave" layout="inline">
              <a-form-item label="统一比例（%）" html-for="period-benefit-uniform-rate">
                <a-input-number id="period-benefit-uniform-rate" v-model:value="item.uniformRate" size="small" :min="0" :max="100"
                  :precision="2" :disabled="saving"/>
              </a-form-item>
            </a-form>
            <span v-else-if="item.method==='rate'">{{ methodSummary(item) }}</span>
            <span v-if="item.method==='rate'" class="period-method-formula">本组净额 × 统一比例</span>
          </div>
          <p v-if="rows.length>1 && rows.some(row=>row.targetKind==='pool')" class="rule-formula">
            团队池负责人或可分配成员变动时分组，同组各收款安排共用本组净额。
          </p>
          <template v-if="item.method==='tiers'">
          <p class="rule-formula">按本组净额选档，净额乘该档比例。</p>
          <div class="period-tier-grid period-tier-header" aria-hidden="true">
            <span>净额起点（元，含）</span><span>净额止点（元，不含）</span><span>计提比例（%）</span><span v-if="canSave">操作</span>
          </div>
          <div v-for="(tier,i) in item.tiers" :key="i" class="period-tier-grid">
            <a-input-number v-model:value="tier.from" :aria-label="'第 '+(i+1)+' 档净额起点'"
              :min="0" :precision="2" :disabled="!canSave || saving || i===0"/>
            <span>{{ item.tiers[i+1]?.from == null ? (i===item.tiers.length-1 ? '不限' : '待填写') : formatMoney(Math.round(item.tiers[i+1].from*100)) }}</span>
            <a-input-number v-model:value="tier.rate" :aria-label="'第 '+(i+1)+' 档计提比例'"
              :min="0" :max="100" :precision="2" :disabled="!canSave || saving"/>
            <a-button v-if="canSave" danger type="link" size="small" :disabled="saving || item.tiers.length===1"
              @click="removeTier(item.tiers,i)">删除</a-button>
          </div>
          <a-button v-if="canSave" type="dashed" :disabled="saving" @click="item.tiers.push({from:undefined,rate:0})">新增档位</a-button>
          </template>
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
              <a-button v-if="!creating && sheet.paused" :disabled="!activityMonth || reviewingActivity"
                :loading="reviewingActivity && activityMode==='paused'" @click="readActivity('paused')">试算暂停规则</a-button>
              <a-button v-else-if="!creating" :disabled="!activityMonth || reviewingActivity" :loading="savingDraft" @click="openDraft">打开整期草稿</a-button>
              <a-button v-if="canSave" :disabled="!changed || !effectiveDate || saving || savingDraft || reviewingActivity"
                :loading="reviewingActivity && activityMode==='edits'" @click="readActivity('edits')">用订单试算</a-button>
            </a-form-item>
          </a-form>
          <a-alert v-if="activityError" class="feedback" :message="activityError" type="error" show-icon/>
          <section v-if="activity" class="period-activity-result" aria-label="已同步数据核对结果">
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
                    <thead><tr><th>规则版本</th><th>收款对象</th><th>本期收款</th><th>本期退款</th><th>本期净额</th>
                      <th v-if="showsPriorCarry">上期结转退款</th>
                      <th>{{ activity.grouping.priorUnsettled ? '结转前试算' : '分润试算' }}</th></tr></thead>
                    <tbody>
                      <tr v-for="group in activity.grouping.groups" :key="group.id">
                        <td>{{ group.ruleId ? 'v'+group.version : '待发布' }}</td>
                        <td>
                          <div v-for="target in group.targets" :key="target.itemKey">
                            {{ target.name }}
                            <small v-if="target.ownerName" class="order-version">{{ target.ownerName }}负责 · {{ target.memberCount }} 人可分配</small>
                          </div>
                        </td>
                        <td>{{ formatMoney(group.receiptsMinor) }}</td>
                        <td>{{ formatMoney(group.refundsMinor) }}</td>
                        <td>{{ formatMoney(group.netMinor) }}</td>
                        <td v-if="showsPriorCarry">{{ formatMoney(-(group.carryInMinor || 0)) }}</td>
                        <td>
                          <div v-for="award in (group.afterCarry || group.beforeCarry).awards" :key="award.itemKey">
                            {{ formatMoney(award.amountMinor) }} <small>· {{ award.label }} {{ award.rateBps/100 }}%</small>
                          </div>
                          <small v-if="(group.afterCarry || group.beforeCarry).carryForwardMinor<0" class="order-version">
                            {{ activity.grouping.priorUnsettled ? '本期未抵扣退款' : '转下期退款' }} {{ formatMoney(-(group.afterCarry || group.beforeCarry).carryForwardMinor) }}
                          </small>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <p v-else class="rule-formula">本期没有可归组的交易。</p>
                <ul v-if="Object.keys(activity.grouping.issues).length" class="period-activity-issues" aria-label="待核分组">
                  <li v-for="issue in activity.grouping.targetIssues || []" :key="issue.ruleId+'-'+issue.itemKey+'-'+issue.code">
                    <strong>{{ issue.label }}</strong> · {{ issue.ruleId ? 'v'+issue.version : '待发布' }} · {{ groupingIssueLabels[issue.code] || issue.code }} · {{ issue.count }} 笔
                    <a-button v-if="issue.ruleId || rows.some(item=>item.itemKey===issue.itemKey)" type="link" size="small"
                      :disabled="saving || (issue.ruleId && issue.ruleId!==rule.id ? changed : !rows.some(item=>item.itemKey===issue.itemKey))"
                      @click="followTargetIssue(issue)">{{ issue.ruleId && issue.ruleId!==rule.id ? '查看规则 v'+issue.version : canSave ? '编辑收款安排' : '查看收款安排' }}</a-button>
                  </li>
                  <li v-for="[issue,count] in otherGroupingIssues" :key="issue">{{ groupingIssueLabels[issue] || issue }} · {{ count }} 笔</li>
                </ul>
              </template>
            </section>
          </section>
        </template>
        <template v-if="!applied">
        <h3 class="form-subhead">金额演算</h3>
        <p class="rule-formula">填写汇总金额，演算各收款安排的计提金额。</p>
        <a-form layout="vertical">
          <div class="period-quote-inputs">
            <a-form-item label="本期实收（元）" html-for="period-quote-received">
              <a-input-number id="period-quote-received" v-model:value="amounts.receipts" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="本期退款（元）" html-for="period-quote-refunds">
              <a-input-number id="period-quote-refunds" v-model:value="amounts.refunds" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="上期未抵扣退款（元）" html-for="period-quote-carry">
              <a-input-number id="period-quote-carry" v-model:value="amounts.carry" :min="0" :precision="2"/>
            </a-form-item>
          </div>
          <a-button :loading="previewing" @click="preview">演算金额</a-button>
        </a-form>
        <a-descriptions v-if="quote" size="small" :column="1" bordered class="period-quote-result" aria-label="金额演算结果">
          <a-descriptions-item label="可计提净额">{{ formatMoney(quote.basisMinor) }}</a-descriptions-item>
          <a-descriptions-item v-for="award in quote.awards" :key="award.itemKey"
            :label="award.label || sheet.items.find(item=>item.itemKey===award.itemKey)?.label">{{ award.rateBps/100 }}% · {{ formatMoney(award.amountMinor) }}</a-descriptions-item>
          <a-descriptions-item label="下期结转退款">{{ formatMoney(-quote.carryForwardMinor) }}</a-descriptions-item>
        </a-descriptions>
        </template>
        <a-form v-if="canSave && !creating" layout="vertical" class="period-quote-result">
          <a-form-item label="新版本生效日期（北京时间零点）" html-for="period-rule-effective" :extra="cycleLabels[sheet.cycle]+'的账期起点生效。'">
            <a-input id="period-rule-effective" v-model:value="effectiveDate" type="date" :min="firstStart" :max="cutoff" :disabled="saving"/>
          </a-form-item>
        </a-form>
      </section>
    </a-spin>
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
.period-component-layout { margin:16px 0 22px; }
.period-scenario-select { width:min(100%,720px); }
.period-component-heading { display:flex; justify-content:space-between; align-items:baseline; gap:12px; flex-wrap:wrap; margin-bottom:8px; }
.period-component-heading h2 { margin:0; }
.period-method { display:flex; flex-wrap:wrap; align-items:center; gap:4px 12px; margin-bottom:6px; }
.period-method :deep(.ant-form-item) { margin:0; }
.period-method-formula { color:var(--muted); font-size:13px; }
.period-tier-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)) auto; gap:8px; align-items:center; margin-bottom:8px; }
.period-tier-header { color:var(--muted); font-size:12px; }
.period-tier-grid :deep(.ant-input-number),.period-quote-inputs :deep(.ant-input-number) { width:100%; }
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
.period-activity-issues { margin:8px 0 0; padding-inline-start:18px; color:var(--muted); }
@media (max-width:540px) { .period-quote-inputs { grid-template-columns:1fr; gap:0; } }
</style>

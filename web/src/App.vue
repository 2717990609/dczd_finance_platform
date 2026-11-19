<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AppstoreOutlined, BankOutlined, CalculatorOutlined, CalendarOutlined, CloudSyncOutlined,
  FileSearchOutlined, LinkOutlined, PlusOutlined, ProfileOutlined, ReloadOutlined, TeamOutlined,
} from '@ant-design/icons-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import DraftWorkbench from './DraftWorkbench.vue'
import OrganizationDirectory from './OrganizationDirectory.vue'
import PracticeTierEditor from './PracticeTierEditor.vue'

const navigation = [
  {key:'settlement', title:'分润试算', icon:CalculatorOutlined},
  {key:'products', title:'产品目录', icon:AppstoreOutlined, description:'维护核算产品，绑定子系统的原始商品或业务标识。'},
  {key:'periods', title:'核算期次', icon:CalendarOutlined, description:'设置实际开班或结算窗口，查看各期匹配的规则。'},
  {key:'rules', title:'规则配置', icon:ProfileOutlined, description:'组合分润组件，并指定适用期次、起始期数或产品默认范围。'},
  {key:'orders', title:'来源订单', icon:FileSearchOutlined, description:'按原订单 ID 从子系统重拉，查询财务目前收到的业务记录。'},
  {key:'organization', title:'组织与人员', icon:TeamOutlined},
  {key:'people', title:'人员映射', icon:TeamOutlined, description:'查询财务计算使用的来源人员标识及有效区间。'},
]
const getRoute = () => navigation.some(x => x.key === location.hash.slice(1)) ? location.hash.slice(1) : 'products'
const current = ref(getRoute())
const active = computed(() => navigation.find(x => x.key === current.value))
const theme = {token:{colorPrimary:'#315ab2', colorLink:'#315ab2', colorText:'#253044', borderRadius:6, fontSize:14, controlHeight:34}}
const productScope = ref()
const scopeSearch = ref('')
const productScopedPages = new Set(['products','periods','rules','settlement'])
const usesProductScope = computed(() => productScopedPages.has(current.value))
const showScope = computed(() => usesProductScope.value || current.value === 'orders')
const organization = ref()
const data = ref({products:[], mappings:[], periods:[], rules:[], orders:[], people:[], identities:[], sources:[], practiceCountOpenings:[], catalogRules:[]})
const loading = ref(false)
const saving = ref(false)
const globalError = ref('')
const notice = ref('')
const formError = ref('')
const drawer = ref('')
const tab = ref('main')
const draft = ref({})
const filter = ref('')
const sourceFilter = ref('')
const olderThan = ref('')
const pageStack = ref([])
let requestVersion = 0
const configuration = ref({})
const configurationReady = ref(false)
const options = group => configuration.value[group] || []
const firstOption = group => options(group)[0]?.value
const optionLabel = (group, value) => options(group).find(option => option.value === value)?.label || value
const sources = computed(() => options('source'))
const classMembers = computed(() => options('classMember'))
const baseModes = computed(() => options('amountMode').filter(option => !option.scope?.componentTypes))
function matchesOption(option, periodMode, type, member) {
  const scope = option.scope || {}
  return (!scope.periodModes || scope.periodModes.includes(periodMode)) &&
    (!scope.componentTypes || scope.componentTypes.includes(type)) &&
    (!scope.classMembers || scope.classMembers.includes(member))
}
const productOptions = computed(() => data.value.products.map(p => ({value:p.id,label:p.name})))
const visibleProducts = computed(() => {
  const term = scopeSearch.value.trim().toLowerCase()
  return term ? data.value.products.filter(p => (p.name+' '+p.code).toLowerCase().includes(term)) : data.value.products
})
const peopleOptions = computed(() => data.value.people.map(p => ({value:p.id,label:p.name})))
const periodOptions = computed(() => data.value.periods.filter(p => p.productId === draft.value.productId).map(p => ({value:p.id,label:p.name})))
const selectedProduct = computed(() => data.value.products.find(p => p.id === draft.value.productId))
const calculationPeriods = computed(() => data.value.periods.filter(p => !productScope.value || p.productId === productScope.value))
const classRule = computed(() => selectedProduct.value?.periodMode === 'class')
const amountModes = row => options('amountMode').filter(option =>
  matchesOption(option, selectedProduct.value?.periodMode, row.type, row.classMember))
function updateAmountMode(row, mode) {
  row.mode = mode
  if (mode === 'tiered' && !row.tierConfig) {
    row.tierConfig = {
      metric:firstOption('tierMetric'),
      startMode:firstOption('tierStartMode'),
      count:[{mode:baseModes.value[0]?.value}],
      performance:[{from:0,mode:baseModes.value[0]?.value}],
      personCountStarts:[],
      companies:[],
      onlyCompanies:false,
    }
  }
}
function resetAmountMode(row) {
  const modes = amountModes(row)
  if (!modes.some(option => option.value === row.mode)) row.mode = modes[0]?.value
}
function tierLabel(tiers) {
  if (tiers.companySchedules?.length) return '公司阶梯'
  if (tiers.count?.length && tiers.performance?.length) return '人数优先阶梯'
  return tiers.count?.length ? '累计人数阶梯' : '本期实付阶梯'
}
const componentTypes = computed(() => options('component').filter(option => matchesOption(option,selectedProduct.value?.periodMode)))
function availableComponentTypes(row) {
  return componentTypes.value.filter(type => !draft.value.components.some(c =>
    c !== row && c.type === type.value && (!classRule.value || !c.classMember || !row.classMember || c.classMember === row.classMember)))
}
function availableClassMembers(row) {
  return classMembers.value.filter(member => !draft.value.components.some(c =>
    c !== row && c.type === row.type && (!c.classMember || !member.value || c.classMember === member.value)))
}
const nextComponent = computed(() => {
  if (drawer.value !== 'rule') return undefined
  const members = classRule.value ? classMembers.value : [{value:undefined}]
  for (const type of componentTypes.value) {
    for (const {value:classMember} of members) {
      const row = {type:type.value,classMember,mode:baseModes.value[0]?.value,amount:0}
      if (availableComponentTypes(row).some(option => option.value === row.type)) return row
    }
  }
})
const enabledSources = computed(() => data.value.sources.filter(s => s.configured && sources.value.some(option => option.value===s.source)).length)
const sourceLabel = key => optionLabel('source',key)?.split(' · ')[0]
const titles = {product:'新增产品',mapping:'添加来源映射',period:'新建期次',rule:'配置分润规则',person:'新增人员',identity:'登记归属',pull:'重新拉取订单'}
const actions = computed(() => {
  if (current.value === 'products') return tab.value === 'mapping' ? 'mapping' : 'product'
  if (current.value === 'people') return tab.value === 'identity' ? 'identity' : 'person'
  return {periods:'period',rules:'rule',orders:'pull'}[current.value]
})
const formatTime = value => value ? new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'short',timeZone:'Asia/Shanghai'}).format(new Date(value)) : '—'
const formatMoney = minor => minor == null ? '—' : new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(minor/100)
const practiceOpeningOptions = computed(() => data.value.practiceCountOpenings
  .filter(row => row.productId===draft.value.productId)
  .map(row => ({value:row.id,label:openingLabel(row)})))
function openingLabel(row) {
  return '冻结结算 '+row.sourceSettlementId.slice(0,8)+' · 截至 '+formatTime(row.throughAt)+' · '+row.personCount+' 人'
}
function savedOpeningLabel(id) {
  const opening = data.value.practiceCountOpenings.find(row=>row.id===id)
  return opening ? openingLabel(opening) : '冻结期初 '+id
}

const labels = {ORDER_PAID:'付款',ORDER_REFUNDED:'退款',REFUND_SUCCEEDED:'退款',REFUND_ADJUSTED:'退款更正',DELIVERY_COMPLETED:'已交付'}
const canOpenAction = computed(() => current.value!=='rules' ||
  !data.value.products.some(product => product.id===productScope.value && product.catalogPending))
function catalogRuleBadge(row) {
  const now = Date.now()
  if (!row.inLatestSnapshot) return { text:'历史版本', color:'default' }
  if (!row.projectMatches) return { text:'其他项目范围', color:'default' }
  if (row.sourceStatus!=='PUBLISHED') return { text:'已停用', color:'default' }
  if (row.effectiveTo && new Date(row.effectiveTo).getTime()<=now) return { text:'已截止', color:'default' }
  if (row.paused) return { text:'暂停执行', color:'orange' }
  if (new Date(row.effectiveFrom).getTime()>now || row.resumeFrom && new Date(row.resumeFrom).getTime()>now) {
    return { text:'待生效', color:'blue' }
  }
  return { text:'待启用', color:'blue' }
}
const mainRows = computed(() => {
  const key = tab.value === 'mapping' ? 'mappings' : tab.value === 'identity' ? 'identities' : current.value
  const result = key === 'rules' ? [...data.value.rules,...data.value.catalogRules] : data.value[key]
  const rows = usesProductScope.value && productScope.value
    ? result.filter(row => (key === 'products' ? row.id : row.productId) === productScope.value)
    : result
  if (current.value === 'orders' || !filter.value.trim()) return rows
  const q = filter.value.trim().toLowerCase()
  return rows.filter(row => Object.values(row).some(value => typeof value === 'string' && value.toLowerCase().includes(q)))
})
const columns = computed(() => {
  const values = {
    product:[['产品名称','name'],['产品编码','code'],['核算方式','periodMode'],['来源映射','mappingCount']],
    mapping:[['产品名称','productName'],['来源系统','source'],['业务域','domain'],['来源标识','sourceKey']],
    period:[['期次名称','name',160],['所属产品','productName',180],['期数','ordinal',84],['开始日期','startsOn',118],['结束日期','endsOn',118],['匹配规则','ruleVersion',200]],
    rule:[['规则名称','label',200],['所属产品','productName',155],['版本','version',80],
      ['适用范围','scope',160],['生效时间','effectiveFrom',175],['状态与分润组件','components',195]],
    pull:[['原订单','orderNo',250],['来源系统','source',112],['业务状态','kind',104],['来源金额','amountMinor',116],['归属产品','productName',142],['业务时间','occurredAt',136]],
    person:[['人员名称','name'],['人员 ID','id']],
    identity:[['归属人员','personName'],['来源系统','source'],['来源人员 ID','externalId'],['生效时间','validFrom'],['结束时间','validTo']],
  }
  return values[actions.value].map(([title,dataIndex,width]) => ({title,dataIndex,key:dataIndex,width}))
})
const tableWidth = computed(() => Math.max(620, columns.value.reduce((width, column) => width + (column.width || 0), 0)))

async function api(path, options) {
  const response = await fetch('/api/v1' + path, {...options, headers:{'Content-Type':'application/json'}})
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const error = new Error(body?.message || '接口暂不可用，请检查本地服务')
    error.status = response.status
    throw error
  }
  return body
}
async function refresh(reloadOptions = false) {
  const version = ++requestVersion
  loading.value = true
  globalError.value = ''
  const params = new URLSearchParams()
  if (sourceFilter.value) params.set('source',sourceFilter.value)
  if (filter.value.trim()) params.set('orderId',filter.value.trim())
  if (olderThan.value) params.set('beforeId',olderThan.value)
  const sections = {settlement:['products','periods'], products: ['products','mappings','sources'], periods:['products','periods'], rules:['products','periods','rules','people','practiceCountOpenings','catalogRules'], orders:['orders','sources'], organization:[], people:['people','identities']}
  const requests = Object.fromEntries(sections[current.value].map(key => [key, key === 'orders' ? '/orders?' + params : '/'+key]))
  if (requests.catalogRules) requests.catalogRules = '/rule-catalog'
  if (requests.practiceCountOpenings) requests.practiceCountOpenings = '/practice-count-openings'
  if (!configurationReady.value || reloadOptions) requests.configuration = '/configuration-options'
  const entries = Object.entries(requests)
  const results = await Promise.allSettled(entries.map(([,url]) => api(url)))
  if (version !== requestVersion) return
  results.forEach((result,index) => {
    const key = entries[index][0]
    if (result.status === 'fulfilled') {
      if (key === 'configuration') {
        configuration.value = result.value
        configurationReady.value = true
      } else data.value[key] = result.value
    } else globalError.value ||= result.reason.message
  })
  loading.value = false
}
function refreshWorkspace() {
  refresh(true)
  organization.value?.refresh()
}
function changeRoute() {
  current.value = getRoute()
  tab.value = 'main'
  filter.value = ''
  sourceFilter.value = ''
  olderThan.value = ''
  pageStack.value = []
  notice.value = ''
  refresh()
}
onMounted(() => { addEventListener('hashchange',changeRoute); refresh() })
onBeforeUnmount(() => removeEventListener('hashchange',changeRoute))
watch(() => draft.value.productId, (next, previous) => {
  if (drawer.value === 'rule' && next !== previous) {
    draft.value.periodId = undefined
    draft.value.effectiveFrom = undefined
    if (previous != null) draft.value.components = [newRuleComponent()]
  }
})
function resetOrders() { olderThan.value = ''; pageStack.value=[]; refresh() }
function selectSource(source) {
  sourceFilter.value = source
  resetOrders()
}
function nextOrders() {
  pageStack.value.push(olderThan.value)
  olderThan.value = String(data.value.orders.at(-1).id)
  refresh()
}
function prevOrders() { olderThan.value = pageStack.value.pop() || ''; refresh() }

function newRuleComponent() {
  return {type:componentTypes.value[0]?.value, classMember:classRule.value ? firstOption('classMember') : undefined,
    mode:baseModes.value[0]?.value, amount:0}
}
function open(kind, row) {
  if (!configurationReady.value) return
  formError.value = ''
  notice.value = ''
  drawer.value = kind
  draft.value = {
    periodMode:firstOption('periodMode'),source:row?.source || firstOption('source'),
    stream:row?.source==='DCAI' && String(row.domain).includes('HEALTH') && options('dcaiOrderStream').some(option => option.value==='HEALTH_IDENTITY_ORDERS')
      ? 'HEALTH_IDENTITY_ORDERS' : firstOption('dcaiOrderStream'),
    orderId:row?.orderId || '',scope:firstOption('ruleScope'),
    productId:['mapping','period','rule'].includes(kind) ? productScope.value : undefined,
  }
  if (kind === 'rule') draft.value.components = [newRuleComponent()]
}
function close() { if (!saving.value) drawer.value = '' }
function must(value, msg = '请补齐必填内容') {
  if (value === undefined || value === null || String(value).trim() === '') throw new Error(msg)
  return value
}
const minor = value => Math.round(Number(value)*100)
function countStartBody(form) {
  must(form.startMode,'请选择累计起点')
  if (form.startMode === 'ordinal') return {countFromOrdinal:must(form.countFromOrdinal,'请填写累计起始期数')}
  const at = must(form.countFromAt,'请填写累计起始时间')
  return {countFromAt:new Date(at+'+08:00').toISOString()}
}
function ruleComponentBody(row) {
  const sourceRole = row.sourceRole?.trim().toUpperCase()
  const component = {type:must(row.type),mode:must(row.mode,'请选择计提方式'),value:0,
    ...(sourceRole ? {sourceRole} : {}), ...(row.refundPolicy ? {refundPolicy:row.refundPolicy} : {}),
    ...(row.classMember ? {classMember:row.classMember} : {})}
  if (row.mode !== 'tiered') {
    component.value = minor(must(row.amount,'请填写每个组件的比例或金额'))
    return component
  }
  const form = row.tierConfig
  must(form.metric,'请选择阶梯指标')
  const serialize = (rows, convert) => rows.map(tier => ({
    from:convert(must(tier.from,'请填写各档位下限')),
    ...(tier.through != null ? {through:convert(tier.through)} : {}),
    mode:must(tier.mode,'请选择各档位计提方式'),
    value:minor(must(tier.value,'请填写各档位计提值')),
  })).sort((a,b) => a.from-b.from)
  component.tiers = {}
  if (form.metric !== 'performance') {
    if (!form.onlyCompanies) component.tiers.count = serialize(form.count,Number)
    if (form.startMode === 'opening') component.tiers.openingId = must(form.openingId,'请选择冻结期初')
    else Object.assign(component.tiers,countStartBody(form))
    if (form.personCountStarts.length) component.tiers.personCountStarts = form.personCountStarts.map(item => ({
      personId:must(item.personId,'请选择设置累计起点的人员'),
      ...countStartBody(item),
    }))
  }
  if (form.metric !== 'count' && !form.onlyCompanies) component.tiers.performance = serialize(form.performance,minor)
  if (form.onlyCompanies && !form.companies.length) throw new Error('请添加销售公司档位')
  if (form.companies.length) component.tiers.companySchedules = form.companies.map(company => ({
    companyId:must(company.companyId,'请选择销售公司'),
    ...(form.metric !== 'performance' ? {count:serialize(company.count,Number)} : {}),
    ...(form.metric !== 'count' ? {performance:serialize(company.performance,minor)} : {}),
  }))
  return component
}
function bodyFor(kind,f) {
  if (kind === 'product') return {code:must(f.code),name:must(f.name),periodMode:f.periodMode,...(f.periodMode==='calendar' ? {cycleStartDay:must(f.cycleStartDay)} : {})}
  if (kind === 'mapping') return {productId:must(f.productId),source:f.source,domain:must(f.domain),sourceKey:must(f.sourceKey)}
  if (kind === 'period') {
    const productId = must(f.productId)
    if (selectedProduct.value?.cycleStartDay) {
      const settlementMonth = must(f.settlementMonth)
      return {productId, settlementMonth, name:f.name?.trim() || settlementMonth+' 账期'}
    }
    return {productId,name:must(f.name),ordinal:must(f.ordinal),startsOn:must(f.startsOn),endsOn:must(f.endsOn),...(f.sourcePeriodKey?.trim() ? {sourcePeriodKey:f.sourcePeriodKey.trim()} : {})}
  }
  if (kind === 'rule') return {productId:must(f.productId),label:must(f.label),
    ...(f.scope === 'period' ? {periodId:must(f.periodId)} : {}),
    ...(f.scope === 'ordinal' ? {fromOrdinal:must(f.fromOrdinal)} : {}),
    ...(f.effectiveFrom ? {effectiveFrom:new Date(f.effectiveFrom+'+08:00').toISOString()} : {}),
    components:f.components.map(ruleComponentBody),
  }
  if (kind === 'person') return {name:must(f.name)}
  if (kind === 'identity') return {personId:must(f.personId),source:f.source,externalId:must(f.externalId),validFrom:new Date(must(f.validFrom)+'T00:00:00+08:00').toISOString()}
  return {source:f.source,stream:f.stream,orderId:must(f.orderId)}
}
async function save() {
  if (saving.value) return
  const kind = drawer.value
  const paths = {product:'/products',mapping:'/mappings',period:'/periods',rule:'/rules',person:'/people',identity:'/identities',pull:'/orders/pull'}
  formError.value = ''
  saving.value = true
  try {
    const payload = bodyFor(kind,draft.value)
    const result = await api(paths[kind],{method:'POST',body:JSON.stringify(payload)})
    notice.value = kind === 'pull' ? '同步完成，读取 '+result.facts+' 条业务记录，新增 '+result.newVersions+' 个版本。'
      : kind === 'rule' ? '规则 v'+result.version+' 已保存。' : '保存成功。'
    drawer.value = ''
    if (kind === 'pull') {
      filter.value = payload.orderId
      sourceFilter.value = payload.source
      resetOrders()
    } else refresh()
  } catch (err) {
    formError.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
<a-config-provider :theme="theme" :locale="zhCN" :auto-insert-space-in-button="false">
  <div class="layout">
    <header class="topbar">
      <div class="brand">
        <span class="brand-icon"><BankOutlined aria-hidden="true"/></span>
        <div class="path"><b>财务工作台</b><span aria-hidden="true">/</span><span>{{ active.title }}</span></div>
      </div>
      <div class="topbar-actions">
        <a-button v-if="current!=='settlement'" :loading="loading" @click="refreshWorkspace"><template #icon><ReloadOutlined aria-hidden="true"/></template>刷新数据</a-button>
      </div>
    </header>
    <nav class="workspace-nav" aria-label="主导航">
      <a v-for="entry in navigation" :key="entry.key" :href="'#'+entry.key" :class="{active:current===entry.key}" :aria-current="current===entry.key ? 'page' : undefined">
        <component :is="entry.icon" aria-hidden="true"/><span>{{ entry.title }}</span>
      </a>
    </nav>
    <div class="workspace-body" :class="{ 'with-scope':showScope }">
      <aside v-if="showScope" class="scope-panel" aria-label="筛选范围">
        <template v-if="usesProductScope">
          <label for="scope-product-search" class="desktop-scope scope-title">产品范围</label>
          <div class="desktop-scope">
            <a-input id="scope-product-search" v-model:value="scopeSearch" placeholder="搜索产品名称或编码" allow-clear/>
            <div class="scope-options" aria-label="产品范围">
              <button type="button" :aria-pressed="!productScope" @click="productScope=undefined">全部产品</button>
              <button v-for="p in visibleProducts" :key="p.id" type="button" :aria-pressed="p.id===productScope" @click="productScope=p.id">{{ p.name }}</button>
            </div>
          </div>
          <div class="mobile-scope">
            <label for="scope-product">产品范围</label>
            <a-select id="scope-product" v-model:value="productScope" placeholder="全部产品" :options="productOptions" allow-clear/>
          </div>
        </template>
        <template v-else>
          <span class="desktop-scope scope-title">订单来源</span>
          <div class="desktop-scope scope-options" aria-label="订单来源">
            <button type="button" :aria-pressed="!sourceFilter" @click="selectSource('')">全部来源</button>
            <button v-for="entry in sources" :key="entry.value" type="button" :aria-pressed="sourceFilter===entry.value" @click="selectSource(entry.value)">{{ entry.label }}</button>
          </div>
          <div class="mobile-scope">
            <label for="scope-source">订单来源</label>
            <a-select id="scope-source" :value="sourceFilter || undefined" placeholder="全部来源" :options="sources" allow-clear @change="selectSource($event || '')"/>
          </div>
        </template>
      </aside>
      <main class="workspace-content">
        <template v-if="current==='settlement'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <DraftWorkbench :periods="calculationPeriods" :component-options="options('component')" :member-options="classMembers" :report-dimensions="options('draftReportDimension')" :api="api" :format-money="formatMoney" :format-time="formatTime" />
        </template>
        <template v-else-if="current==='organization'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <OrganizationDirectory ref="organization" :api="api" :option-label="optionLabel" :format-time="formatTime"/>
        </template>
        <template v-else>
        <section class="heading">
          <div><h1>{{ active.title }}</h1><p>{{ active.description }}</p></div>
          <a-button v-if="canOpenAction" type="primary" :disabled="!configurationReady" @click="open(actions)"><template #icon><component :is="current==='orders'?CloudSyncOutlined:PlusOutlined" aria-hidden="true"/></template>{{ titles[actions] }}</a-button>
        </section>
        <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <div v-if="current==='products'" class="summary-grid">
          <article><span class="metric-icon"><AppstoreOutlined/></span><div><small>目录产品</small><strong>{{ data.products.length }}</strong></div></article>
          <article><span class="metric-icon blue"><LinkOutlined/></span><div><small>来源映射</small><strong>{{ data.mappings.length }}</strong></div></article>
          <article><span class="metric-icon gold"><CloudSyncOutlined/></span><div><small>已配置来源</small><strong>{{ enabledSources }}<em> / {{ sources.length }}</em></strong></div></article>
        </div>
        <div v-if="current==='orders'" class="summary-grid">
          <article v-for="entry in data.sources" :key="entry.source"><span class="metric-icon"><CloudSyncOutlined/></span><div><small>{{ sourceLabel(entry.source) }}</small><strong class="source-state">{{ entry.configured ? '已配置' : '未配置' }}</strong><small v-if="entry.lastUpdate">最近入库 {{ formatTime(entry.lastUpdate) }}</small></div></article>
        </div>
        <section class="records">
          <div class="records-head">
            <a-tabs v-if="current==='products'||current==='people'" v-model:active-key="tab" @change="filter=''">
              <a-tab-pane key="main" :tab="current==='products'?'产品列表':'人员列表'"/>
              <a-tab-pane :key="current==='products'?'mapping':'identity'" :tab="current==='products'?'来源映射':'映射历史'"/>
            </a-tabs>
            <h2 v-else>{{ {periods:'期次列表',rules:'规则列表',orders:'订单业务记录'}[current] }}</h2>
            <div class="search">
              <a-input-search v-model:value="filter" :placeholder="current==='orders'?'输入原订单 ID':'在当前列表中搜索'" :aria-label="current==='orders'?'原订单 ID':'搜索列表'" allow-clear @search="current==='orders'&&resetOrders()"/>
            </div>
          </div>
          <div class="table-container">
            <a-table :columns="columns" :data-source="mainRows" :loading="loading" row-key="id" :pagination="current==='orders'?false:{pageSize:12,showSizeChanger:false}" :scroll="{x:tableWidth}">
              <template #bodyCell="{ column, record, text }">
                <template v-if="column.key==='name'||column.key==='label'">
                  <strong class="primary-cell">{{ text }}</strong>
                  <small v-if="record.catalogPending && record.packageCode" class="order-version">{{ record.packageCode }}</small>
                </template>
                <template v-else-if="column.key==='periodMode'"><a-tag :color="text==='class'?'green':'blue'">{{ optionLabel('periodMode',text) }}</a-tag></template>
                <template v-else-if="column.key==='mappingCount'">{{ data.mappings.filter(x=>x.productId===record.id).length }}</template>
                <template v-else-if="column.key==='ruleVersion'"><a-tag v-if="text" color="green">v{{ text }} · {{ record.ruleLabel }}</a-tag><a-tag v-else color="orange">{{ record.periodMode==='class' && !record.classStartsAt ? '待签到名单' : '待配规则' }}</a-tag></template>
                <template v-else-if="column.key==='version'"><a-tag color="green">v{{ text }}</a-tag></template>
                <template v-else-if="column.key==='effectiveFrom'">
                  {{ text ? formatTime(text) : '范围默认' }}
                  <small v-if="record.effectiveTo" class="order-version">截止 {{ formatTime(record.effectiveTo) }}</small>
                </template>
                <template v-else-if="column.key==='scope'">
                  <template v-if="record.catalogPending">
                    {{ record.scopeId || record.scopeType }}<small class="order-version">{{ record.project }} · {{ record.scopeType }}</small>
                  </template>
                  <template v-else>{{ record.periodName || (record.fromOrdinal ? '从第 '+record.fromOrdinal+' 期起' : '产品默认') }}</template>
                </template>
                <template v-else-if="column.key==='components' && record.catalogPending">
                  <a-tag :color="catalogRuleBadge(record).color">{{ catalogRuleBadge(record).text }}</a-tag>
                </template>
                <template v-else-if="column.key==='components'">
                  <div v-for="c in text" :key="c.type+':'+(c.classMember || '')">
                    <a-tag>{{ optionLabel('component',c.type) }}{{ c.classMember ? ' · '+optionLabel('classMember',c.classMember) : '' }} {{ c.mode==='tiered' ? tierLabel(c.tiers) : c.mode==='rate' ? (c.value/100)+'%' : formatMoney(c.value) }}</a-tag>
                    <small v-if="c.tiers?.companySchedules?.length" class="order-version">
                      {{ c.tiers.companySchedules.map(company=>optionLabel('practiceSalesCompany',company.companyId)).join('、') }}
                    </small>
                    <small v-if="c.tiers?.openingId" class="order-version">{{ savedOpeningLabel(c.tiers.openingId) }}</small>
                    <details v-if="c.tiers?.personCountStarts?.length" class="rule-count-review">
                      <summary>个人累计起点（{{ c.tiers.personCountStarts.length }}）</summary>
                      <ul>
                        <li v-for="start in c.tiers.personCountStarts" :key="start.personId">
                          {{ peopleOptions.find(person=>person.value===start.personId)?.label || '人员 '+start.personId }}
                          · {{ start.countFromOrdinal ? '第 '+start.countFromOrdinal+' 期' : formatTime(start.countFromAt) }}
                        </li>
                      </ul>
                    </details>
                  </div>
                </template>
                <template v-else-if="column.key==='source'">{{ sourceLabel(text) }}</template>
                <template v-else-if="column.key==='orderNo'"><button class="order-link" type="button" @click="open('pull',record)" :title="'原订单 ID '+record.orderId">{{ text || record.orderId }}</button><small class="order-version">版本 {{ record.version }}</small></template>
                <template v-else-if="column.key==='kind'"><a-tag>{{ labels[text] || text }}</a-tag></template>
                <template v-else-if="column.key==='productName'&&current==='orders'"><a-tag v-if="!text" color="orange">待映射</a-tag><span v-else>{{ text }}</span></template>
                <template v-else-if="column.key==='amountMinor'"><span class="numeric">{{ formatMoney(text) }}</span></template>
                <template v-else-if="['occurredAt','validFrom','validTo'].includes(column.key)">{{ formatTime(text) }}</template>
              </template>
              <template #emptyText>
                <div class="empty"><a-empty :image="undefined" :description="filter ? '未找到匹配记录' : '暂无记录'"/><a-button v-if="!filter && configurationReady && canOpenAction" @click="open(actions)">{{ titles[actions] }}</a-button></div>
              </template>
            </a-table>
          </div>
          <div v-if="current==='orders'&&mainRows.length" class="paging"><a-button :disabled="!pageStack.length||loading" @click="prevOrders">上一页</a-button><a-button :disabled="mainRows.length<50||loading" @click="nextOrders">下一页</a-button></div>
        </section>
        </template>
      </main>
    </div>
  </div>
  <a-drawer :open="Boolean(drawer)" :title="titles[drawer]" :width="'min(510px, 100vw)'" @close="close">
    <a-alert v-if="formError" class="feedback" type="error" :message="formError" show-icon/>
    <a-form :model="draft" layout="vertical" @finish="save">
      <template v-if="drawer==='product'">
        <a-form-item label="产品名称" required><a-input v-model:value="draft.name" aria-label="产品名称" :maxlength="80"/></a-form-item>
        <a-form-item label="产品编码" required><a-input v-model:value="draft.code" aria-label="产品编码" :maxlength="48"/></a-form-item>
        <a-form-item label="核算方式"><a-select v-model:value="draft.periodMode" aria-label="核算方式" :options="options('periodMode')"/></a-form-item>
        <a-form-item v-if="draft.periodMode==='calendar'" label="每月开始日" extra="填 1 表示自然月。填 25 表示上月 25 日至本月 24 日。" required><a-input-number v-model:value="draft.cycleStartDay" aria-label="每月开始日" :min="1" :max="28" :precision="0"/></a-form-item>
      </template>
      <a-form-item v-if="['mapping','period','rule'].includes(drawer)" label="所属产品" required><a-select v-model:value="draft.productId" aria-label="所属产品" placeholder="选择产品" :options="productOptions"/></a-form-item>
      <a-form-item v-if="['mapping','pull','identity'].includes(drawer)" label="来源系统"><a-select v-model:value="draft.source" aria-label="来源系统" :options="sources"/></a-form-item>
      <template v-if="drawer==='mapping'">
        <a-form-item label="来源业务域" required><a-input v-model:value="draft.domain" aria-label="来源业务域" placeholder="填写源系统返回的业务域"/></a-form-item>
        <a-form-item label="源产品标识" required><a-input v-model:value="draft.sourceKey" aria-label="源产品标识"/></a-form-item>
      </template>
      <template v-if="drawer==='period'">
        <template v-if="selectedProduct?.cycleStartDay">
          <a-form-item label="结算月份" required><a-input v-model:value="draft.settlementMonth" aria-label="结算月份" type="month"/></a-form-item>
          <a-form-item label="账期名称"><a-input v-model:value="draft.name" aria-label="账期名称" placeholder="默认使用结算月份"/></a-form-item>
        </template>
        <template v-else>
          <a-form-item label="期次名称" required><a-input v-model:value="draft.name" aria-label="期次名称"/></a-form-item>
          <a-form-item label="期数" required><a-input-number v-model:value="draft.ordinal" aria-label="期数" :min="1" :precision="0"/></a-form-item>
          <div class="field-grid"><a-form-item label="开始日期" required><a-input v-model:value="draft.startsOn" aria-label="开始日期" type="date"/></a-form-item><a-form-item label="结束日期" required><a-input v-model:value="draft.endsOn" aria-label="结束日期" type="date"/></a-form-item></div>
          <a-form-item label="来源期次标识" extra="填入子系统已确认签到名单的期次标识。">
            <a-input v-model:value="draft.sourcePeriodKey" aria-label="来源期次标识" placeholder="PRACTICE:1:1:123" :maxlength="80"/>
          </a-form-item>
        </template>
      </template>
      <template v-if="drawer==='rule'">
        <a-form-item label="规则名称" required><a-input v-model:value="draft.label" aria-label="规则名称"/></a-form-item>
        <a-form-item html-for="rule-scope" label="适用范围"><a-select id="rule-scope" v-model:value="draft.scope" :options="options('ruleScope')"/></a-form-item>
        <a-form-item v-if="draft.scope==='period'" html-for="rule-period" label="指定期次" required><a-select id="rule-period" v-model:value="draft.periodId" :options="periodOptions"/></a-form-item>
        <a-form-item v-if="draft.scope==='ordinal'" label="起始期数" required><a-input-number v-model:value="draft.fromOrdinal" aria-label="起始期数" :min="1" :precision="0"/></a-form-item>
        <a-form-item v-if="selectedProduct" label="生效时间" :extra="classRule ? '按北京时间的实际开班时点匹配。留空时作为该适用范围的默认规则。' : '按北京时间和原付款时间匹配。留空时作为该适用范围的默认规则。'">
          <a-input v-model:value="draft.effectiveFrom" aria-label="规则生效时间" type="datetime-local"/>
        </a-form-item>
        <h3 class="form-subhead">分润组件</h3>
        <div v-for="(c,i) in draft.components" :key="i" class="component-editor">
          <div class="component-row">
          <label class="sr-only" :for="'rule-component-'+i">组件 {{ i+1 }}</label>
          <a-select :id="'rule-component-'+i" v-model:value="c.type" :options="availableComponentTypes(c)" @change="resetAmountMode(c)"/>
          <label class="sr-only" :for="'rule-mode-'+i">计算方式 {{ i+1 }}</label>
          <a-select :id="'rule-mode-'+i" :value="c.mode" :options="amountModes(c)" @change="updateAmountMode(c,$event)"/>
          <a-input-number v-if="c.mode!=='tiered'" v-model:value="c.amount" :aria-label="'组件参数 '+(i+1)" :min="0" :max="c.mode==='rate'?100:1000000000" :precision="2"/>
          <a-button v-if="draft.components.length>1" type="link" danger @click="draft.components.splice(i,1)">移除</a-button>
          </div>
          <a-form-item v-if="classRule" :html-for="'rule-member-'+i" :label="'报名类型 '+(i+1)">
            <a-select :id="'rule-member-'+i" v-model:value="c.classMember" :options="availableClassMembers(c)" @change="resetAmountMode(c)"/>
          </a-form-item>
          <a-form-item v-if="['sales','referral'].includes(c.type)" :label="'分项 '+(i+1)+' 的来源角色'">
            <a-input v-model:value="c.sourceRole" :aria-label="'来源角色 '+(i+1)" placeholder="如 LOCK_SALES_USER 或 DIRECT_REFERRER" :maxlength="64"/>
          </a-form-item>
          <PracticeTierEditor v-if="c.mode==='tiered'" :config="c.tierConfig" :metrics="options('tierMetric')" :start-modes="options('tierStartMode')" :amounts="baseModes" :people="peopleOptions" :openings="practiceOpeningOptions" :companies="options('practiceSalesCompany')"/>
          <a-form-item v-if="selectedProduct?.periodMode==='calendar'" :label="'分项 '+(i+1)+' 的退款处理'" extra="历史退款沿用付款时的规则版本。">
            <a-select v-model:value="c.refundPolicy" :options="options('refundPolicy')" allow-clear placeholder="选择退款处理"/>
          </a-form-item>
        </div>
        <a-button v-if="nextComponent" type="dashed" block @click="draft.components.push(nextComponent)">添加组件</a-button>
      </template>
      <a-form-item v-if="drawer==='person'" label="人员名称" required><a-input v-model:value="draft.name" aria-label="人员名称"/></a-form-item>
      <template v-if="drawer==='identity'">
        <a-form-item label="归属人员" required><a-select v-model:value="draft.personId" aria-label="归属人员" :options="peopleOptions"/></a-form-item>
        <a-form-item label="来源人员 ID" required><a-input v-model:value="draft.externalId" aria-label="来源人员 ID"/></a-form-item>
        <a-form-item label="生效日期" required><a-input v-model:value="draft.validFrom" aria-label="生效日期" type="date"/></a-form-item>
      </template>
      <template v-if="drawer==='pull'">
        <a-alert v-if="!data.sources.find(x=>x.source===draft.source)?.configured" class="feedback" type="info" message="此来源尚未配置接口，请先在本机服务配置连接。" show-icon/>
        <a-form-item v-if="draft.source==='DCAI'" label="订单类型"><a-select v-model:value="draft.stream" aria-label="订单类型" :options="options('dcaiOrderStream')"/></a-form-item>
        <a-form-item label="原订单 ID" required><a-input v-model:value="draft.orderId" aria-label="原订单 ID"/></a-form-item>
      </template>
      <div class="form-actions"><a-button :disabled="saving" @click="close">取消</a-button><a-button html-type="submit" type="primary" :loading="saving">{{ drawer==='rule'?'保存并生效':drawer==='pull'?'开始拉取':'保存' }}</a-button></div>
    </a-form>
  </a-drawer>
</a-config-provider>
</template>

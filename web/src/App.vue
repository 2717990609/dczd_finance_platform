<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  AppstoreOutlined, CalculatorOutlined, CalendarOutlined, CloudSyncOutlined, DatabaseOutlined,
  FileSearchOutlined, LinkOutlined, PlusOutlined, ProfileOutlined, ReloadOutlined, TeamOutlined,
} from '@ant-design/icons-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import DraftWorkbench from './DraftWorkbench.vue'

const navigation = [
  {key:'settlement', title:'分润试算', icon:CalculatorOutlined},
  {key:'products', title:'产品目录', icon:AppstoreOutlined, description:'维护核算产品，绑定子系统的原始商品或业务标识。'},
  {key:'periods', title:'核算期次', icon:CalendarOutlined, description:'设置实际开班或结算窗口，查看各期匹配的规则。'},
  {key:'rules', title:'规则配置', icon:ProfileOutlined, description:'组合分润组件，并指定适用期次、起始期数或产品默认范围。'},
  {key:'orders', title:'来源订单', icon:FileSearchOutlined, description:'按原订单 ID 从子系统重拉，查询财务目前收到的业务记录。'},
  {key:'people', title:'人员映射', icon:TeamOutlined, description:'将子系统人员标识映射到财务人员，保留历次归属。'},
]
const getRoute = () => navigation.some(x => x.key === location.hash.slice(1)) ? location.hash.slice(1) : 'products'
const current = ref(getRoute())
const active = computed(() => navigation.find(x => x.key === current.value))
const theme = {token:{colorPrimary:'#19745e', colorLink:'#19745e', colorText:'#182a26', borderRadius:8, fontSize:14, controlHeight:38}}
const data = ref({products:[], mappings:[], periods:[], rules:[], orders:[], people:[], identities:[], sources:[]})
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
const sources = [{value:'DCZD',label:'大春之道 · DCZD'}, {value:'DCAI',label:'大春 AI · DCAI'}, {value:'DCJK',label:'大春见康 · DCJK'}]
const components = [
  {value:'sales',label:'销售提成'}, {value:'referral',label:'推荐提成'},
  {value:'manager',label:'负责人提成'}, {value:'delivery',label:'交付提成'},
]
const productOptions = computed(() => data.value.products.map(p => ({value:p.id,label:p.name})))
const peopleOptions = computed(() => data.value.people.map(p => ({value:p.id,label:p.name})))
const periodOptions = computed(() => data.value.periods.filter(p => p.productId === draft.value.productId).map(p => ({value:p.id,label:p.name})))
const selectedProduct = computed(() => data.value.products.find(p => p.id === draft.value.productId))
const calendarPeriods = computed(() => {
  const ids = new Set(data.value.products.filter(p => p.periodMode === 'calendar').map(p => p.id))
  return data.value.periods.filter(p => ids.has(p.productId))
})
const enabledSources = computed(() => data.value.sources.filter(s => s.configured).length)
const sourceLabel = key => sources.find(s => s.value === key)?.label.split(' · ')[0] || key
const titles = {product:'新增产品',mapping:'添加来源映射',period:'新建期次',rule:'配置分润规则',person:'新增人员',identity:'登记归属',pull:'重新拉取订单'}
const actions = computed(() => {
  if (current.value === 'products') return tab.value === 'mapping' ? 'mapping' : 'product'
  if (current.value === 'people') return tab.value === 'identity' ? 'identity' : 'person'
  return {periods:'period',rules:'rule',orders:'pull'}[current.value]
})
const formatTime = value => value ? new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'short',timeZone:'Asia/Shanghai'}).format(new Date(value)) : '—'
const formatMoney = minor => minor == null ? '—' : new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(minor/100)
const labels = {ORDER_PAID:'付款',ORDER_REFUNDED:'退款',REFUND_SUCCEEDED:'退款',REFUND_ADJUSTED:'退款更正',DELIVERY_COMPLETED:'已交付'}
const mainRows = computed(() => {
  const key = tab.value === 'mapping' ? 'mappings' : tab.value === 'identity' ? 'identities' : current.value
  const rows = data.value[key]
  if (current.value === 'orders' || !filter.value.trim()) return rows
  const q = filter.value.trim().toLowerCase()
  return rows.filter(row => Object.values(row).some(value => typeof value === 'string' && value.toLowerCase().includes(q)))
})
const columns = computed(() => {
  const values = {
    product:[['产品名称','name'],['产品编码','code'],['核算方式','periodMode'],['来源映射','mappingCount']],
    mapping:[['产品名称','productName'],['来源系统','source'],['业务域','domain'],['来源标识','sourceKey']],
    period:[['期次名称','name'],['所属产品','productName'],['期数','ordinal'],['开始日期','startsOn'],['结束日期','endsOn'],['匹配规则','ruleVersion']],
    rule:[['规则名称','label'],['所属产品','productName'],['版本','version'],['适用范围','scope'],['生效时间','effectiveFrom'],['分润组件','components']],
    pull:[['原订单','orderNo',250],['来源系统','source',112],['业务状态','kind',104],['来源金额','amountMinor',116],['归属产品','productName',142],['业务时间','occurredAt',136]],
    person:[['人员名称','name'],['人员 ID','id']],
    identity:[['归属人员','personName'],['来源系统','source'],['来源人员 ID','externalId'],['生效时间','validFrom'],['结束时间','validTo']],
  }
  return values[actions.value].map(([title,dataIndex,width]) => ({title,dataIndex,key:dataIndex,width}))
})

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
async function refresh() {
  const version = ++requestVersion
  loading.value = true
  globalError.value = ''
  const params = new URLSearchParams()
  if (sourceFilter.value) params.set('source',sourceFilter.value)
  if (filter.value.trim()) params.set('orderId',filter.value.trim())
  if (olderThan.value) params.set('beforeId',olderThan.value)
  const sections = {settlement:['products','periods'], products: ['products','mappings','sources'], periods:['products','periods'], rules:['products','periods','rules'], orders:['orders','sources'], people:['people','identities']}
  const requests = Object.fromEntries(sections[current.value].map(key => [key, key === 'orders' ? '/orders?' + params : '/'+key]))
  const entries = Object.entries(requests)
  const results = await Promise.allSettled(entries.map(([,url]) => api(url)))
  if (version !== requestVersion) return
  results.forEach((result,index) => {
    if (result.status === 'fulfilled') data.value[entries[index][0]] = result.value
    else globalError.value ||= result.reason.message
  })
  loading.value = false
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
  }
})
function resetOrders() { olderThan.value = ''; pageStack.value=[]; refresh() }
function nextOrders() {
  pageStack.value.push(olderThan.value)
  olderThan.value = String(data.value.orders.at(-1).id)
  refresh()
}
function prevOrders() { olderThan.value = pageStack.value.pop() || ''; refresh() }

function open(kind, row) {
  formError.value = ''
  notice.value = ''
  drawer.value = kind
  draft.value = {
    periodMode:'class',cycleStartDay:25,source:row?.source || 'DCZD',stream:row?.source === 'DCAI' && String(row.domain).includes('HEALTH') ? 'HEALTH_IDENTITY_ORDERS' : 'VIP_ORDERS',
    orderId:row?.orderId || '',scope:'default',components:[{type:'sales',mode:'rate',amount:0}],
  }
}
function close() { if (!saving.value) drawer.value = '' }
function must(value, msg = '请补齐必填内容') {
  if (value === undefined || value === null || String(value).trim() === '') throw new Error(msg)
  return value
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
    return {productId,name:must(f.name),ordinal:must(f.ordinal),startsOn:must(f.startsOn),endsOn:must(f.endsOn)}
  }
  if (kind === 'rule') return {productId:must(f.productId),label:must(f.label),
    ...(f.scope === 'period' ? {periodId:must(f.periodId)} : {}),
    ...(f.scope === 'ordinal' ? {fromOrdinal:must(f.fromOrdinal)} : {}),
    ...(f.effectiveFrom ? {effectiveFrom:new Date(f.effectiveFrom+'+08:00').toISOString()} : {}),
    components:f.components.map(c => ({type:must(c.type),mode:c.mode,value:Math.round(Number(must(c.amount,'请填写每个组件的比例或金额'))*100),...(c.sourceRole?.trim() ? {sourceRole:c.sourceRole.trim().toUpperCase()} : {})})),
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
    <aside class="sidebar">
      <div class="brand"><div class="brand-icon"><DatabaseOutlined /></div><div><b>财务核算平台</b><small>Finance Workspace</small></div></div>
      <nav aria-label="主导航">
        <a v-for="entry in navigation" :key="entry.key" :href="'#'+entry.key" :class="{active:current===entry.key}" :aria-current="current === entry.key ? 'page' : undefined"><component :is="entry.icon" aria-hidden="true"/><span>{{ entry.title }}</span></a>
      </nav>
      <div class="sidebar-bottom"><span class="live-dot"></span> 本地开发环境</div>
    </aside>
    <div class="surface">
      <header class="topbar"><div class="path">财务工作台 <i>/</i> <strong>{{ active.title }}</strong></div><a-button type="text" :loading="loading" @click="refresh"><template #icon><ReloadOutlined aria-hidden="true"/></template>刷新</a-button></header>
      <main class="workspace-content">
        <DraftWorkbench v-if="current==='settlement'" :periods="calendarPeriods" :api="api" :format-money="formatMoney" :format-time="formatTime" />
        <template v-else>
        <section class="heading">
          <div><h1>{{ active.title }}</h1><p>{{ active.description }}</p></div>
          <a-button type="primary" size="large" @click="open(actions)"><template #icon><component :is="current==='orders'?CloudSyncOutlined:PlusOutlined" aria-hidden="true"/></template>{{ titles[actions] }}</a-button>
        </section>
        <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <div v-if="current==='products'" class="summary-grid">
          <article><span class="metric-icon"><AppstoreOutlined/></span><div><small>目录产品</small><strong>{{ data.products.length }}</strong></div></article>
          <article><span class="metric-icon blue"><LinkOutlined/></span><div><small>来源映射</small><strong>{{ data.mappings.length }}</strong></div></article>
          <article><span class="metric-icon gold"><CloudSyncOutlined/></span><div><small>已配置来源</small><strong>{{ enabledSources }}<em> / 3</em></strong></div></article>
        </div>
        <div v-if="current==='orders'" class="summary-grid">
          <article v-for="entry in data.sources" :key="entry.source"><span class="metric-icon"><CloudSyncOutlined/></span><div><small>{{ sourceLabel(entry.source) }}</small><strong class="source-state">{{ entry.configured ? '已配置' : '未配置' }}</strong><small v-if="entry.lastUpdate">最近入库 {{ formatTime(entry.lastUpdate) }}</small></div></article>
        </div>
        <section class="records">
          <div class="records-head">
            <a-tabs v-if="current==='products'||current==='people'" v-model:active-key="tab" @change="filter=''">
              <a-tab-pane key="main" :tab="current==='products'?'产品列表':'人员列表'"/>
              <a-tab-pane :key="current==='products'?'mapping':'identity'" :tab="current==='products'?'来源映射':'归属历史'"/>
            </a-tabs>
            <h2 v-else>{{ {periods:'期次列表',rules:'已生效规则',orders:'订单业务记录'}[current] }}</h2>
            <div class="search">
              <a-select v-if="current==='orders'" v-model:value="sourceFilter" style="width:134px" allow-clear placeholder="全部来源" :options="sources" @change="resetOrders"/>
              <a-input-search v-model:value="filter" :placeholder="current==='orders'?'输入原订单 ID':'在当前列表中搜索'" :aria-label="current==='orders'?'原订单 ID':'搜索列表'" allow-clear @search="current==='orders'&&resetOrders()"/>
            </div>
          </div>
          <div class="table-container">
            <a-table :columns="columns" :data-source="mainRows" :loading="loading" row-key="id" :pagination="current==='orders'?false:{pageSize:12,showSizeChanger:false}" :scroll="{x: current==='orders'?860:620}">
              <template #bodyCell="{ column, record, text }">
                <template v-if="column.key==='name'||column.key==='label'"><strong class="primary-cell">{{ text }}</strong></template>
                <template v-else-if="column.key==='periodMode'"><a-tag :color="text==='class'?'green':'blue'">{{ text==='class'?'实际开班':'固定账期' }}</a-tag></template>
                <template v-else-if="column.key==='mappingCount'">{{ data.mappings.filter(x=>x.productId===record.id).length }}</template>
                <template v-else-if="column.key==='ruleVersion'"><a-tag v-if="text" color="green">v{{ text }} · {{ record.ruleLabel }}</a-tag><a-tag v-else color="orange">待配规则</a-tag></template>
                <template v-else-if="column.key==='version'"><a-tag color="green">v{{ text }}</a-tag></template>
                <template v-else-if="column.key==='effectiveFrom'">{{ text ? formatTime(text) : '范围默认' }}</template>
                <template v-else-if="column.key==='scope'">{{ record.periodName || (record.fromOrdinal ? '从第 '+record.fromOrdinal+' 期起' : '产品默认') }}</template>
                <template v-else-if="column.key==='components'"><a-tag v-for="c in text" :key="c.type">{{ components.find(x=>x.value===c.type)?.label }} {{ c.mode==='rate'?(c.value/100)+'%':formatMoney(c.value) }}</a-tag></template>
                <template v-else-if="column.key==='source'">{{ sourceLabel(text) }}</template>
                <template v-else-if="column.key==='orderNo'"><button class="order-link" type="button" @click="open('pull',record)" :title="'原订单 ID '+record.orderId">{{ text || record.orderId }}</button><small class="order-version">版本 {{ record.version }}</small></template>
                <template v-else-if="column.key==='kind'"><a-tag>{{ labels[text] || text }}</a-tag></template>
                <template v-else-if="column.key==='productName'&&current==='orders'"><a-tag v-if="!text" color="orange">待映射</a-tag><span v-else>{{ text }}</span></template>
                <template v-else-if="column.key==='amountMinor'"><span class="numeric">{{ formatMoney(text) }}</span></template>
                <template v-else-if="['occurredAt','validFrom','validTo'].includes(column.key)">{{ formatTime(text) }}</template>
              </template>
              <template #emptyText>
                <div class="empty"><a-empty :image="undefined" :description="filter ? '未找到匹配记录' : '暂无记录'"/><a-button v-if="!filter" @click="open(actions)">{{ titles[actions] }}</a-button></div>
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
        <a-form-item label="核算方式"><a-select v-model:value="draft.periodMode" aria-label="核算方式" :options="[{value:'class',label:'实际开班'},{value:'calendar',label:'固定账期'}]"/></a-form-item>
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
        </template>
      </template>
      <template v-if="drawer==='rule'">
        <a-form-item label="规则名称" required><a-input v-model:value="draft.label" aria-label="规则名称"/></a-form-item>
        <a-form-item label="适用范围"><a-select v-model:value="draft.scope" aria-label="适用范围" :options="[{value:'default',label:'产品默认'},{value:'ordinal',label:'从指定期数起'},{value:'period',label:'指定期次'}]"/></a-form-item>
        <a-form-item v-if="draft.scope==='period'" label="指定期次" required><a-select v-model:value="draft.periodId" aria-label="指定期次" :options="periodOptions"/></a-form-item>
        <a-form-item v-if="draft.scope==='ordinal'" label="起始期数" required><a-input-number v-model:value="draft.fromOrdinal" aria-label="起始期数" :min="1" :precision="0"/></a-form-item>
        <a-form-item v-if="selectedProduct?.periodMode==='calendar'" label="生效时间" extra="按北京时间和原付款时间匹配。留空时作为该适用范围的默认规则。">
          <a-input v-model:value="draft.effectiveFrom" aria-label="规则生效时间" type="datetime-local"/>
        </a-form-item>
        <h3 class="form-subhead">分润组件</h3>
        <div v-for="(c,i) in draft.components" :key="i" class="component-editor">
          <div class="component-row">
          <a-select v-model:value="c.type" :aria-label="'组件 '+(i+1)" :options="components.filter(x=>x.value===c.type||!draft.components.some(y=>y.type===x.value))"/>
          <a-select v-model:value="c.mode" :aria-label="'计算方式 '+(i+1)" :options="[{value:'rate',label:'比例 %'},{value:'fixed',label:'金额 ¥'}]"/>
          <a-input-number v-model:value="c.amount" :aria-label="'组件参数 '+(i+1)" :min="0" :max="c.mode==='rate'?100:1000000000" :precision="2"/>
          <a-button v-if="draft.components.length>1" type="link" danger @click="draft.components.splice(i,1)">移除</a-button>
          </div>
          <a-form-item v-if="['sales','referral'].includes(c.type)" :label="'分项 '+(i+1)+' 的来源角色'">
            <a-input v-model:value="c.sourceRole" :aria-label="'来源角色 '+(i+1)" placeholder="如 LOCK_SALES_USER 或 DIRECT_REFERRER" :maxlength="64"/>
          </a-form-item>
        </div>
        <a-button v-if="draft.components.length<4" type="dashed" block @click="draft.components.push({type:components.find(x=>!draft.components.some(y=>x.value===y.type)).value,mode:'rate',amount:0})">添加组件</a-button>
      </template>
      <a-form-item v-if="drawer==='person'" label="人员名称" required><a-input v-model:value="draft.name" aria-label="人员名称"/></a-form-item>
      <template v-if="drawer==='identity'">
        <a-form-item label="归属人员" required><a-select v-model:value="draft.personId" aria-label="归属人员" :options="peopleOptions"/></a-form-item>
        <a-form-item label="来源人员 ID" required><a-input v-model:value="draft.externalId" aria-label="来源人员 ID"/></a-form-item>
        <a-form-item label="生效日期" required><a-input v-model:value="draft.validFrom" aria-label="生效日期" type="date"/></a-form-item>
      </template>
      <template v-if="drawer==='pull'">
        <a-alert v-if="!data.sources.find(x=>x.source===draft.source)?.configured" class="feedback" type="info" message="此来源尚未配置接口，请先在本机服务配置连接。" show-icon/>
        <a-form-item v-if="draft.source==='DCAI'" label="订单类型"><a-select v-model:value="draft.stream" aria-label="订单类型" :options="[{value:'VIP_ORDERS',label:'AI 会员'},{value:'HEALTH_IDENTITY_ORDERS',label:'健康大使或创客'}]"/></a-form-item>
        <a-form-item label="原订单 ID" required><a-input v-model:value="draft.orderId" aria-label="原订单 ID"/></a-form-item>
      </template>
      <div class="form-actions"><a-button :disabled="saving" @click="close">取消</a-button><a-button html-type="submit" type="primary" :loading="saving">{{ drawer==='rule'?'保存并生效':drawer==='pull'?'开始拉取':'保存' }}</a-button></div>
    </a-form>
  </a-drawer>
</a-config-provider>
</template>

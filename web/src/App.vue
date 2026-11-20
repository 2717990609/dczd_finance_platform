<script setup>
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  BankOutlined, CloudSyncOutlined, LeftOutlined, PlusOutlined, ReloadOutlined, RightOutlined,
} from '@ant-design/icons-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import DraftWorkbench from './DraftWorkbench.vue'
import MonthlyReport from './MonthlyReport.vue'
import CatalogRuleRates from './CatalogRuleRates.vue'
import PracticePeriodBenefitEditor from './PracticePeriodBenefitEditor.vue'
import PracticeRetrainSplitEditor from './PracticeRetrainSplitEditor.vue'
import PracticeCompanySalesEditor from './PracticeCompanySalesEditor.vue'
import FinanceLogin from './FinanceLogin.vue'
import OrganizationDirectory from './OrganizationDirectory.vue'
import WorkspaceOrganizationScope from './WorkspaceOrganizationScope.vue'
import OrderDetails from './OrderDetails.vue'
import OrderSyncPanel from './OrderSyncPanel.vue'
import PracticeTierEditor from './PracticeTierEditor.vue'
import { organizationTree } from './organization-tree'
import { copiedRuleComponents } from './rule-component-form'

const RuleVersionDetails = defineAsyncComponent(() => import('./RuleVersionDetails.vue'))
const navigation = [
  {key:'settlement', area:'work', title:'分润试算'},
  {key:'monthly', area:'work', title:'月度汇总'},
  {key:'orders', area:'work', title:'来源订单'},
  {key:'products', area:'config', title:'产品目录'},
  {key:'periods', area:'config', title:'核算期次'},
  {key:'rules', area:'config', title:'产品规则'},
  {key:'organization', area:'data', title:'组织与人员'},
  {key:'people', area:'data', title:'人员映射'},
]
const workspaces = [{key:'work',title:'工作台'}, {key:'config',title:'配置'}, {key:'data',title:'系统与数据'}]
const lastWorkspacePages = ref({})
const session = ref()
const sessionError = ref('')
const loginRequested = ref(false)
const sessionBusy = ref(false)
const sessionChecking = ref(false)
const wholeWorkspace = computed(() => session.value?.user ? session.value.user.workspace : session.value?.development)
const visibleNavigation = computed(() => wholeWorkspace.value ? navigation : session.value?.user?.rules.length
  ? navigation.filter(entry => entry.key==='rules') : [])
const getRoute = key => {
  if (visibleNavigation.value.some(entry => entry.key === key)) return key
  return visibleNavigation.value[0]?.key
}
const current = ref()
const requestedPeriod = ref()
const requestedReview = ref(false)
const orderSyncTarget = ref()
const orderSyncReturnPeriod = ref()
const workbenchPeriod = ref()
const workbench = ref()
const active = computed(() => navigation.find(x => x.key === current.value))
const workspacePages = computed(() => visibleNavigation.value.filter(page => page.area === active.value?.area))
const visibleWorkspaces = computed(() => workspaces.flatMap(workspace => {
  const pages = visibleNavigation.value.filter(page => page.area === workspace.key)
  const target = pages.find(page => page.key === lastWorkspacePages.value[workspace.key]) || pages[0]
  return target ? [{...workspace, target:target.key}] : []
}))
const activeWorkspace = computed(() => workspaces.find(workspace => workspace.key === active.value?.area))
const theme = {token:{colorPrimary:'#315ab2', colorLink:'#315ab2', colorText:'#253044', borderRadius:6, fontSize:14, controlHeight:34}}
const productScope = ref()
const unitScope = ref()
const usesOrganizationScope = computed(() => ['orders','settlement','monthly'].includes(current.value))
const scopeSearch = ref('')
const configProductPages = new Set(['products','periods','rules'])
const productScopedPages = new Set([...configProductPages,'settlement','monthly'])
const usesProductScope = computed(() => productScopedPages.has(current.value))
const showScope = computed(() => usesProductScope.value || current.value === 'orders')
const organization = ref()
const monthlyReport = ref()
const workspaceMonth = ref(new Intl.DateTimeFormat('sv-SE', {year:'numeric',month:'2-digit',timeZone:'Asia/Shanghai'}).format(new Date()))
const usesWorkspaceMonth = computed(() => ['settlement','monthly'].includes(current.value))
const monthlyView = ref()
const monthlyDimension = ref()
const periodFromMonthly = ref(false)
const monthlyReturn = ref()
const monthlyResume = ref()
const orderSync = ref()
const orderDetails = ref()
const rateRule = ref()
const ruleDetails = ref()
const appliedRule = ref()
const rulePreview = ref()
const previewPane = ref()
const viewedRule = computed(() => ruleDetails.value || appliedRule.value?.rule)
const emptyData = () => ({products:[], mappings:[], periods:[], rules:[], orders:[], people:[], identities:[], sources:[], practiceCountOpenings:[], catalogRules:[]})
const data = ref(emptyData())
const loading = ref(false)
const saving = ref(false)
const globalError = ref('')
const notice = ref('')
const formError = ref('')
const formFeedback = ref()
const drawer = ref('')
const tab = ref('main')
const draft = ref({})
const filter = ref('')
const sourceFilter = ref('')
const olderThan = ref('')
const pageStack = ref([])
let requestVersion = 0
let sessionRequest = 0
const configuration = ref({})
const configurationReady = ref(false)
const scopeUnits = ref()
const selectedUnit = computed(() => scopeUnits.value?.find(unit=>unit.id===unitScope.value))
const options = group => configuration.value[group] || []
const firstOption = group => options(group)[0]?.value
const optionLabel = (group, value) => options(group).find(option => option.value === value)?.label || value
const orderRoleLabel = role => role==='SALES_COMPANY' ? '销售公司' : optionLabel('orderAttributionRole',role)
const scopeTree = computed(() => organizationTree(scopeUnits.value || [],kind=>optionLabel('organizationKind',kind)))
const sources = computed(() => options('source'))
const classMembers = computed(() => options('classMember'))
function componentSourceRoles(saved) {
  const configured = options('orderAttributionRole')
  return saved && !configured.some(option => option.value === saved)
    ? [...configured, {value:saved,label:saved}] : configured
}
const baseModes = computed(() => options('amountMode').filter(option => !option.scope?.componentTypes))
function matchesOption(option, periodMode, type, member) {
  const scope = option.scope || {}
  return (!scope.periodModes || scope.periodModes.includes(periodMode)) &&
    (!scope.componentTypes || scope.componentTypes.includes(type)) &&
    (!scope.classMembers || scope.classMembers.includes(member))
}
const productOptions = computed(() => data.value.products.map(p => ({value:p.id,label:p.name})))
const canReadRules = productId => session.value?.user
  ? session.value.user.rules.some(rule=>rule.productId===productId) : session.value?.development
const scopedProducts = computed(() => data.value.products.filter(p => current.value !== 'rules' || canReadRules(p.id)))
const scopeProductOptions = computed(() => scopedProducts.value.map(p=>({value:p.id,label:p.name})))
const canPublishRule = productId => session.value?.user
  ? session.value.user.rules.some(rule => rule.productId===productId && rule.publish) : session.value?.development
const ruleProductOptions = computed(() => data.value.products.filter(p=>(!p.catalogPending || p.periodMode==='class') && canPublishRule(p.id))
  .map(p=>({value:p.id,label:p.name})))
const visibleProducts = computed(() => {
  const term = scopeSearch.value.trim().toLowerCase()
  return term ? scopedProducts.value.filter(p => (p.name+' '+p.code).toLowerCase().includes(term)) : scopedProducts.value
})
const peopleOptions = computed(() => data.value.people.map(p => ({value:p.id,label:p.name})))
const classRuleDirectory = ref()
const classPeopleOptions = computed(() => classRuleDirectory.value?.people.map(person=>({value:person.id,label:person.name})) || peopleOptions.value)
const unitOptions = computed(() => classRuleDirectory.value?.units.map(unit=>({value:unit.id,label:unit.name})) || [])
const practicePoolOptions = computed(() => classRuleDirectory.value?.pools?.map(pool=>({value:pool.id,label:pool.name+' · '+pool.unitName})) || [])
const companyRecipientOptions = computed(() => classRuleDirectory.value?.units.filter(unit=>unit.kind==='COMPANY')
  .map(unit=>({value:unit.id,label:unit.name})) || [])
const periodOptions = computed(() => data.value.periods.filter(p => p.productId === draft.value.productId && !p.practiceRefundMonth)
  .map(p => ({value:p.id,label:p.name})))
const selectedProduct = computed(() => data.value.products.find(p => p.id === draft.value.productId))
const calculationPeriods = computed(() => data.value.periods.filter(p =>
  (!productScope.value || p.productId === productScope.value) && (!workspaceMonth.value || p.reportMonth === workspaceMonth.value)))
watch(() => data.value.periods.find(period => period.id === requestedPeriod.value)?.reportMonth, month => {
  if (month) workspaceMonth.value = month
})
const classRule = computed(() => selectedProduct.value?.periodMode === 'class')
const classRefundMonth = computed(() => classRule.value && draft.value.practicePeriodKind === 'refund_month')
const calendarPeriod = computed(() => classRefundMonth.value || !classRule.value && !!selectedProduct.value?.cycleStartDay)
const calendarDates = computed(() => {
  const month = /^(\d{4})-(0[1-9]|1[0-2])$/.exec(draft.value.settlementMonth || '')
  if (!calendarPeriod.value || !month || Number(month[1]) < 1) return
  const defaultRefundDay = options('practiceRefundCalendar').find(option=>/^(?:[1-9]|1\d|2[0-8])$/.test(option.value))?.value
  const day = Number(selectedProduct.value?.cycleStartDay ?? defaultRefundDay)
  if (!Number.isInteger(day) || day < 1 || day > 28) return
  const date = (monthNumber, dateNumber) => {
    const value = new Date(0)
    value.setUTCFullYear(Number(month[1]), monthNumber-1, dateNumber)
    return value.toISOString().slice(0,10)
  }
  const monthNumber = Number(month[2])
  const startsOn = date(day===1 ? monthNumber : monthNumber-1, day)
  const endsOn = date(day===1 ? monthNumber+1 : monthNumber, day-1)
  return startsOn+' 至 '+endsOn
})
const periodBenefitMode = computed(() => options('amountMode').find(option=>option.value==='period_rate'))
const isPeriodBenefit = type => classRule.value && ['manager','delivery'].includes(type)
const companySalesMode = computed(() => options('amountMode').find(option=>option.value==='company_sales'))
const componentMode = type => isPeriodBenefit(type) ? 'period_rate' : type==='company_sales' ? 'company_sales' : undefined
const amountModes = row => {
  const expected = componentMode(row.type)
  return options('amountMode').filter(option =>
    matchesOption(option, selectedProduct.value?.periodMode, row.type, row.classMember) &&
    (expected ? option.value===expected : !['period_rate','company_sales'].includes(option.value)))
}
function updateAmountMode(row, mode) {
  row.mode = mode
  if (mode==='period_rate' && !row.periodBenefit) {
    row.periodBenefit = {recipient:firstOption('practicePeriodRecipient'),pool:{},tiers:[{from:0}],useTiers:false}
  }
  if (mode==='company_sales' && !row.companySales) {
    row.sourceRole = row.sourceRole || firstOption('practicePeriodSourceRole')
    row.companySales = {internalCompanies:[],categories:[{mode:baseModes.value[0]?.value,companies:[],overrides:[]}]}
  }
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
  if (row.type==='company_sales' && !availableClassMembers(row).some(member=>member.value===row.classMember)) {
    row.classMember = availableClassMembers(row)[0]?.value
  }
  const modes = amountModes(row)
  if (!modes.some(option => option.value === row.mode)) updateAmountMode(row,modes[0]?.value)
}
function tierLabel(tiers) {
  if (tiers.companySchedules?.length) return '公司阶梯'
  if (tiers.count?.length && tiers.performance?.length) return '人数优先阶梯'
  return tiers.count?.length ? '累计人数阶梯' : '本期实付阶梯'
}
const componentTypes = computed(() => options('component').filter(option=>
  matchesOption(option,selectedProduct.value?.periodMode) &&
  (!isPeriodBenefit(option.value) || !!periodBenefitMode.value?.scope?.componentTypes?.includes(option.value)) &&
  (option.value!=='company_sales' || !!companySalesMode.value)))
function availableComponentTypes(row) {
  return componentTypes.value.filter(type => isPeriodBenefit(type.value) ||
    (type.value==='company_sales' ? availableClassMembers(row,type.value).length :
      !draft.value.components.some(c => c !== row && c.type === type.value &&
        (!classRule.value || !c.classMember || !row.classMember || c.classMember === row.classMember))))
}
function availableClassMembers(row,type = row.type) {
  if (isPeriodBenefit(type)) return classMembers.value
  return classMembers.value.filter(member =>
    (type!=='company_sales' || companySalesMode.value?.scope?.classMembers?.includes(member.value)) &&
    !draft.value.components.some(c =>
      c !== row && c.type === type && (!c.classMember || !member.value || c.classMember === member.value)))
}
const nextComponent = computed(() => {
  if (drawer.value !== 'rule') return undefined
  const members = classRule.value ? classMembers.value : [{value:undefined}]
  for (const type of componentTypes.value) {
    for (const {value:classMember} of members) {
      const row = {type:type.value,classMember,mode:baseModes.value[0]?.value,amount:0}
      if (availableComponentTypes(row).some(option => option.value === row.type)) {
        resetAmountMode(row)
        return row
      }
    }
  }
})
const syncStreams = computed(() => data.value.sources
  .filter(entry => entry.configured && (!sourceFilter.value || sourceFilter.value===entry.source))
  .flatMap(entry => options(entry.source.toLowerCase()+'OrderStream')))
const sourceLabel = key => optionLabel('source',key)?.split(' · ')[0]
const titles = {product:'新增产品',mapping:'添加来源映射',period:'新建期次',reportMonth:'设置报表月份',rule:'配置分润规则',person:'新增人员',identity:'登记归属',pull:'重新拉取订单'}
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
const catalogScopeLabels = {BUSINESS_SCENARIO:'业务场景',CROSS_SCENARIO_AGGREGATE:'跨场景汇总'}
const canOpenAction = computed(() => current.value!=='rules' ||
  ruleProductOptions.value.some(product => !productScope.value || product.value===productScope.value))
function catalogRuleBadge(row) {
  const now = Date.now()
  if (!row.inLatestSnapshot) return { text:'历史版本', color:'default' }
  if (!row.projectMatches) return { text:'其他核算项目', color:'default' }
  if (row.sourceStatus!=='PUBLISHED') return { text:'已停用', color:'default' }
  if (row.effectiveTo && new Date(row.effectiveTo).getTime()<=now) return { text:'已截止', color:'default' }
  if (row.paused) return { text:'暂停执行', color:'orange' }
  if (new Date(row.effectiveFrom).getTime()>now || row.resumeFrom && new Date(row.resumeFrom).getTime()>now) {
    return { text:'待生效', color:'blue' }
  }
  return row.draftSupported ? { text:'可试算', color:'green' } : { text:'未开放试算', color:'blue' }
}
function ratePublished(result) {
  notice.value = result.id === rateRule.value?.id ? '规则无变化。' : '规则 v'+result.version+' 已发布。'
  rateRule.value = undefined
  refresh()
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
    product:[['产品名称','name'],['产品编码','code'],['核算方式','periodMode'],['来源映射','mappingCount'],['操作','productAction',160]],
    mapping:[['产品名称','productName'],['业务来源','source'],['业务域','domain'],['来源标识','sourceKey']],
    period:[['期次名称','name',160],['所属产品','productName',180],['期数','ordinal',84],['开始日期','startsOn',118],['结束日期','endsOn',118],['报表月份','reportMonth',115],['适用规则','ruleVersion',200]],
    rule:[['规则名称','label',200],['所属产品','productName',155],['版本','version',80],
      ['适用范围','scope',160],['生效时间','effectiveFrom',175],['状态与分润组件','components',220],['操作','ruleAction',160]],
    pull:[['原订单','orderNo',250],['订单来源','source',112],['业务状态','kind',104],['来源金额','amountMinor',116],['归属产品','productName',142],['业务时间','occurredAt',136]],
    person:[['人员名称','name'],['人员 ID','id']],
    identity:[['归属人员','personName'],['业务来源','source'],['来源人员 ID','externalId'],['生效时间','validFrom'],['结束时间','validTo']],
  }
  return values[actions.value].map(([title,dataIndex,width]) => ({title,dataIndex,key:dataIndex,width}))
})
const tableWidth = computed(() => Math.max(620, columns.value.reduce((width, column) => width + (column.width || 0), 0)))

async function api(path, options) {
  const issuedFor = sessionRequest
  const response = await fetch('/api/v1' + path, {...options, headers:{'Content-Type':'application/json'}})
  const body = await response.json().catch(() => null)
  if (!response.ok) {
    const error = new Error(body?.message || '接口暂不可用，请检查本地服务')
    error.status = response.status
    if (response.status===401 && path!=='/session' && session.value?.user && issuedFor===sessionRequest) clearSession()
    throw error
  }
  return body
}
async function refresh(reloadOptions = false) {
  if (!current.value) return
  const version = ++requestVersion
  loading.value = true
  globalError.value = ''
  const params = new URLSearchParams()
  if (sourceFilter.value) params.set('source',sourceFilter.value)
  if (filter.value.trim()) params.set('orderId',filter.value.trim())
  if (olderThan.value) params.set('beforeId',olderThan.value)
  if (unitScope.value) params.set('unitId',unitScope.value)
  const sections = {settlement:['products','periods'], monthly:['products'], products: ['products','mappings','sources'], periods:['products','periods'], rules:['products','periods','rules','people','practiceCountOpenings','catalogRules'], orders:['orders','sources'], organization:[], people:['people','identities']}
  const requests = Object.fromEntries(sections[current.value].map(key => [key, key === 'orders' ? '/orders?' + params : '/'+key]))
  if (requests.catalogRules) requests.catalogRules = '/rule-catalog'
  if (requests.practiceCountOpenings) requests.practiceCountOpenings = '/practice-count-openings'
  if (!wholeWorkspace.value) delete requests.people
  if (!configurationReady.value || reloadOptions) requests.configuration = '/configuration-options'
  if (usesOrganizationScope.value && (!scopeUnits.value || reloadOptions)) requests.scopeUnits = '/organization-units'
  const entries = Object.entries(requests)
  const results = await Promise.allSettled(entries.map(([,url]) => api(url)))
  if (version !== requestVersion) return
  results.forEach((result,index) => {
    const key = entries[index][0]
    if (result.status === 'fulfilled') {
      if (key === 'configuration') {
        configuration.value = result.value
        configurationReady.value = true
      } else if (key === 'scopeUnits') scopeUnits.value = result.value
      else {
        data.value[key] = result.value
        if (key === 'products' && productScope.value && !result.value.some(product=>product.id===productScope.value)) {
          productScope.value = undefined
        }
      }
    } else globalError.value ||= result.reason.message
  })
  loading.value = false
}
function refreshWorkspace() {
  refresh(true)
  organization.value?.refresh()
  monthlyReport.value?.refresh()
  orderSync.value?.reload()
}
function changeRoute() {
  const previous = current.value
  const [key, query] = location.hash.slice(1).split('?', 2)
  current.value = getRoute(key)
  if (active.value) lastWorkspacePages.value[active.value.area] = current.value
  const params = new URLSearchParams(query)
  const productID = Number(params.get('productId'))
  if (current.value === key && configProductPages.has(key) && Number.isSafeInteger(productID) && productID > 0) {
    productScope.value = productID
  }
  const periodID = Number(params.get('periodId'))
  requestedPeriod.value = key === 'settlement' && current.value === key && Number.isSafeInteger(periodID) && periodID > 0 ? periodID : undefined
  requestedReview.value = Boolean(requestedPeriod.value && params.get('view') === 'review')
  const fromMonthly = Boolean(requestedPeriod.value && params.get('from') === 'monthly')
  monthlyResume.value = undefined
  if (fromMonthly && previous === 'monthly') {
    monthlyReturn.value = {month:workspaceMonth.value,view:monthlyView.value,dimension:monthlyDimension.value,
      product:productScope.value,unit:unitScope.value,browse:monthlyReport.value?.browseState()}
  }
  if (current.value === 'monthly' && previous === 'settlement' && periodFromMonthly.value && monthlyReturn.value) {
    const saved = monthlyReturn.value
    workspaceMonth.value = saved.month
    monthlyView.value = saved.view
    monthlyDimension.value = saved.dimension
    productScope.value = saved.product
    unitScope.value = saved.unit
    monthlyResume.value = saved.browse
  }
  if (!fromMonthly) monthlyReturn.value = undefined
  periodFromMonthly.value = fromMonthly
  const returnPeriod = Number(params.get('returnPeriodId'))
  const isOrders = key === 'orders' && current.value === key
  const stream = params.get('stream')
  const fromDate = params.get('fromDate')
  orderSyncTarget.value = isOrders && stream ? {stream,fromDate} : undefined
  orderSyncReturnPeriod.value = isOrders && Number.isSafeInteger(returnPeriod) && returnPeriod > 0 ? returnPeriod : undefined
  orderDetails.value = undefined
  rateRule.value = undefined
  ruleDetails.value = undefined
  appliedRule.value = undefined
  rulePreview.value = undefined
  if (requestedPeriod.value && productScope.value && data.value.periods.find(period => period.id === requestedPeriod.value)?.productId !== productScope.value) {
    productScope.value = undefined
  }
  if (current.value==='rules' && session.value?.user && !session.value.user.rules.some(rule=>rule.productId===productScope.value)) {
    productScope.value=undefined
  }
  tab.value = 'main'
  filter.value = ''
  sourceFilter.value = ''
  olderThan.value = ''
  pageStack.value = []
  notice.value = ''
  refresh()
}
function clearWorkspace() {
  ++requestVersion
  data.value = emptyData()
  productScope.value = undefined
  unitScope.value = undefined
  scopeUnits.value = undefined
  lastWorkspacePages.value = {}
  current.value = undefined
  requestedPeriod.value = undefined
  requestedReview.value = false
  orderSyncTarget.value = undefined
  orderSyncReturnPeriod.value = undefined
  workbenchPeriod.value = undefined
  periodFromMonthly.value = false
  monthlyReturn.value = undefined
  monthlyResume.value = undefined
  orderDetails.value = undefined
  drawer.value = ''
  draft.value = {}
  rateRule.value = undefined
  ruleDetails.value = undefined
  appliedRule.value = undefined
  rulePreview.value = undefined
  saving.value = false
  formError.value = ''
  globalError.value = ''
  notice.value = ''
  filter.value = ''
  sourceFilter.value = ''
  olderThan.value = ''
  pageStack.value = []
  configuration.value = {}
  configurationReady.value = false
}
function clearSession() {
  ++sessionRequest
  clearWorkspace()
  session.value = {development:session.value.development,user:null}
  loginRequested.value = session.value.development
  sessionChecking.value = false
}
function cancelLogin() {
  loginRequested.value = false
  changeRoute()
}
async function loadSession() {
  const check=++sessionRequest
  sessionChecking.value = true
  sessionError.value = ''
  try {
    const next = await api('/session')
    if (check!==sessionRequest) return
    clearWorkspace()
    session.value = next
    loginRequested.value = false
    changeRoute()
  } catch (err) {
    if (check===sessionRequest) {
      clearWorkspace()
      sessionError.value = err.message
    }
  } finally {
    if (check===sessionRequest) sessionChecking.value = false
  }
}
async function signOut() {
  if (sessionBusy.value) return
  sessionBusy.value = true
  try {
    await api('/session',{method:'DELETE'})
    clearSession()
  } catch (err) {
    sessionError.value = err.message
  } finally {
    sessionBusy.value = false
  }
}
onMounted(() => { addEventListener('hashchange',changeRoute); loadSession() })
onBeforeUnmount(() => removeEventListener('hashchange',changeRoute))
watch(() => draft.value.productId, (next, previous) => {
  if (drawer.value === 'rule' && next !== previous && !draft.value.copiedFrom) {
    draft.value.periodId = undefined
    draft.value.effectiveFrom = undefined
    draft.value.components = [newRuleComponent()]
  }
})
watch(() => drawer.value==='rule' && classRule.value ? draft.value.productId : undefined, async (product, _, onCleanup) => {
  classRuleDirectory.value = undefined
  if (!product) return
  let current = true
  onCleanup(() => { current = false })
  try {
    const directory = await api('/practice-rule-recipients?productId='+product)
    if (current) classRuleDirectory.value = directory
  } catch (err) {
    if (current) formError.value = err.message
  }
})
function resetOrders() { olderThan.value = ''; pageStack.value=[]; refresh() }
function selectOrganization(unit) {
  if (unitScope.value === unit) return
  unitScope.value = unit
  if (current.value === 'orders') resetOrders()
}
function selectProduct(product) {
  productScope.value = product
  const [key, search] = location.hash.slice(1).split('?', 2)
  if (key !== current.value || !configProductPages.has(key)) return
  const params = new URLSearchParams(search)
  if (product) params.set('productId', product)
  else params.delete('productId')
  const query = params.toString()
  history.replaceState(history.state, '', '#'+key+(query ? '?'+query : ''))
}
async function createPracticePool(name, unitId) {
  const productId = draft.value.productId
  const result = await api('/practice-pools', {method:'POST',body:JSON.stringify({productId,name,unitId})})
  if (classRuleDirectory.value && draft.value.productId===productId && !classRuleDirectory.value.pools.some(pool=>pool.id===result.id)) {
    classRuleDirectory.value.pools.push({id:result.id,name,unitName:unitOptions.value.find(unit=>unit.value===unitId)?.label})
  }
  return result.id
}
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
  rulePreview.value = undefined
  drawer.value = kind
  draft.value = {
    periodMode:firstOption('periodMode'),practicePeriodKind:firstOption('practicePeriodKind'),source:row?.source || firstOption('source'),
    orderId:row?.orderId || '',scope:firstOption('ruleScope'),
    productId:['mapping','period','rule'].includes(kind) ? productScope.value : undefined,
  }
  if (kind === 'pull') {
    const stream = row?.domain?.includes('HEALTH') ? 'HEALTH_IDENTITY_ORDERS' : row?.domain==='VIP' ? 'VIP_ORDERS' : undefined
    draft.value.stream = options('dcaiOrderStream').find(option=>option.value===stream)?.value || firstOption('dcaiOrderStream')
    draft.value.lockedOrder = Boolean(row?.orderId)
    draft.value.sourceConfigured = row?.sourceConfigured
  }
  if (kind === 'reportMonth') draft.value = {periodId:row.id, periodName:row.name, reportMonth:row.reportMonthOverride || ''}
  if (kind === 'rule') {
    if (row) {
      draft.value = {productId:row.productId, label:row.label, copiedFrom:row.version,
        scope:row.periodId ? 'period' : row.fromOrdinal ? 'ordinal' : 'default',
        components:copiedRuleComponents(row.components),
      }
    } else draft.value.components = [newRuleComponent()]
  }
}
function pullFromDetails(order) {
  orderDetails.value = undefined
  open('pull', order)
}
function copyRuleDetails() {
  const rule = ruleDetails.value
  ruleDetails.value = undefined
  open('rule', rule)
}
async function showAppliedRule(selection) {
  appliedRule.value = {loading:true}
  const request = appliedRule.value
  try {
    const result = await api('/drafts/rule?'+new URLSearchParams(selection))
    if (appliedRule.value === request) appliedRule.value = result
  } catch (err) {
    if (appliedRule.value === request) appliedRule.value = {error:err.message}
  }
}
function close() {
  if (saving.value) return
  drawer.value = ''
  rulePreview.value = undefined
}
function showFormError(error) {
  formError.value = error.message
  nextTick(() => formFeedback.value?.focus())
}
function previewRule() {
  formError.value = ''
  try {
    const rule = bodyFor('rule', draft.value)
    rulePreview.value = {...rule,productName:selectedProduct.value?.name,
      periodName:periodOptions.value.find(option=>option.value===rule.periodId)?.label}
    nextTick(() => previewPane.value?.focus())
  } catch (err) {
    showFormError(err)
  }
}
function returnToRuleEditor() {
  rulePreview.value = undefined
  nextTick(() => document.getElementById('rule-preview-button')?.focus())
}
function must(value, msg = '请补齐必填内容') {
  if (value === undefined || value === null || String(value).trim() === '') throw new Error(msg)
  return value
}
const minor = value => Math.round(Number(value)*100)
const periodRate = value => Math.round(Number(must(value,'请填写班次计提比例'))*10000)
function practicePoolBody(pool) {
  return {poolId:must(pool.poolId,'请选择资金池'),ownerId:must(pool.ownerId,'请选择分配负责人')}
}
function countStartBody(form) {
  must(form.startMode,'请选择累计起点')
  if (form.startMode === 'ordinal') return {countFromOrdinal:must(form.countFromOrdinal,'请填写累计起始期数')}
  const at = must(form.countFromAt,'请填写累计起始时间')
  return {countFromAt:new Date(at+'+08:00').toISOString()}
}
function ruleComponentBody(row) {
  const sourceRole = row.sourceRole?.trim().toUpperCase()
  if (['sales','referral'].includes(row.type)) must(sourceRole,'请选择销售或推荐分项的订单角色。')
  const component = {type:must(row.type),mode:must(row.mode,'请选择计提方式'),value:0,
    ...(sourceRole ? {sourceRole} : {}), ...(row.refundPolicy && !['period_rate','company_sales'].includes(row.mode) ? {refundPolicy:row.refundPolicy} : {}),
    ...(row.classMember ? {classMember:row.classMember} : {})}
  if (row.mode==='company_sales') {
    must(sourceRole,'请选择销售来源角色')
    const value = item => {
      const mode = must(item.mode,'请选择外部公司计算方式')
      return {mode,value:mode==='rate' ? periodRate(item.value) : minor(must(item.value,'请填写计提金额')),
        ...(row.classMember==='new' && item.recommendationRate != null ? {recommendationRate:periodRate(item.recommendationRate)} : {})}
    }
    component.companySales = {internalCompanies:row.companySales.internalCompanies,categories:row.companySales.categories.map(category=>({
      label:must(category.label?.trim(),'请填写类别名称'),
      companies:category.companies,
      ...value(category),
      ...(category.overrides.length ? {overrides:category.overrides.map(override=>({
        companyId:must(override.companyId,'请选择采用单独标准的公司'), ...value(override),
      }))} : {}),
    }))}
    if (component.companySales.categories.some(category=>!category.companies.length)) throw new Error('请选择每个类别适用的公司')
    if (component.companySales.categories.some(category=>category.recommendationRate>0 ||
      category.overrides?.some(override=>override.recommendationRate>0))) {
      if (!row.companySales.recommendationRecipients?.length) throw new Error('请选择可直接收取推荐分润的公司')
      component.companySales.recommendationRecipients = row.companySales.recommendationRecipients
    }
    return component
  }
  if (row.mode === 'period_rate') {
    const b = row.periodBenefit
    component.value = b.useTiers ? 0 : periodRate(row.amount)
    component.periodBenefit = {
      label:must(b.label?.trim(),'请填写分项名称'),
      ...(b.recipient==='pool' ? {pool:practicePoolBody(b.pool)} : b.recipient==='unit'
        ? {unitId:must(b.unitId,'请选择收款组织')} : {personId:must(b.personId,'请选择收款人员')}),
      ...(b.salesCompanies?.length ? {salesCompanies:b.salesCompanies} : {}),
      ...(b.salesPeople?.length ? {salesPeople:b.salesPeople} : {}),
      ...(b.recipient==='person' && b.appointmentUnitId ? {
        appointmentUnitId:b.appointmentUnitId,appointmentKind:must(b.appointmentKind,'请选择任职方式'),
      } : {}),
      ...(b.useTiers ? {tierOnWholeClass:b.tierOnWholeClass || false,tiers:b.tiers.map(t=>({
        from:minor(must(t.from,'请填写净实收档位下限')),
        ...(t.through != null ? {through:minor(t.through)} : {}),
        value:periodRate(t.value),
      })).sort((a,b)=>a.from-b.from)} : {}),
    }
    if (b.salesPeople?.length) must(sourceRole,'请选择销售人员的来源角色')
    return component
  }
  if (row.mode !== 'tiered') {
    component.value = minor(must(row.amount,'请填写每个组件的比例或金额'))
    if (classRule.value && row.type==='sales' && row.mode==='fixed' && row.classMember==='retrain' && row.retrainSplits?.length) {
      must(sourceRole,'请选择销售来源角色')
      component.retrainSplits = row.retrainSplits.map(split=>({
        companyId:must(split.companyId,'请选择拆分销售公司'),pool:practicePoolBody(split.pool),
        personalMinor:minor(must(split.personal,'请填写销售份额')),
        ...(split.servicePersonId ? {servicePersonId:split.servicePersonId,serviceMinor:minor(must(split.service,'请填写服务份额'))} : {}),
      }))
      if (component.retrainSplits.some(split=>split.personalMinor+(split.serviceMinor || 0)>component.value)) {
        throw new Error('销售份额与服务份额合计不能超过固定提成。')
      }
    }
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
  if (kind === 'product') return {code:must(f.code),name:must(f.name),periodMode:f.periodMode,
    ...(f.periodMode==='calendar' ? {cycleStartDay:must(f.cycleStartDay)} : f.cycleStartDay!=null ? {cycleStartDay:f.cycleStartDay} : {})}
  if (kind === 'mapping') return {productId:must(f.productId),source:f.source,domain:must(f.domain),sourceKey:must(f.sourceKey)}
  if (kind === 'period') {
    const productId = must(f.productId)
    if (calendarPeriod.value) {
      const settlementMonth = must(f.settlementMonth)
      return {productId, settlementMonth, name:f.name?.trim() || settlementMonth+(classRefundMonth.value ? ' 退款账期' : ' 账期')}
    }
    return {productId,name:must(f.name),ordinal:must(f.ordinal),startsOn:must(f.startsOn),endsOn:must(f.endsOn),
      ...(f.sourcePeriodKey?.trim() ? {sourcePeriodKey:f.sourcePeriodKey.trim()} : {}), ...(classRule.value && f.reportMonth ? {reportMonth:f.reportMonth} : {})}
  }
  if (kind === 'reportMonth') return {periodId:f.periodId,month:f.reportMonth || ''}
  if (kind === 'rule') return {productId:must(f.productId),label:must(f.label),
    ...(f.scope === 'period' ? {periodId:must(f.periodId,'请选择适用期次。')} : {}),
    ...(f.scope === 'ordinal' ? {fromOrdinal:must(f.fromOrdinal,'请填写适用的起始期数。')} : {}),
    ...(f.effectiveFrom ? {effectiveFrom:new Date(f.effectiveFrom+'+08:00').toISOString()} : {}),
    components:f.components.map(ruleComponentBody),
  }
  if (kind === 'person') return {name:must(f.name)}
  if (kind === 'identity') return {personId:must(f.personId),source:f.source,externalId:must(f.externalId),validFrom:new Date(must(f.validFrom)+'T00:00:00+08:00').toISOString()}
  return {source:f.source,stream:f.stream,orderId:must(f.orderId)}
}
async function save() {
  if (saving.value) return
  const issuedFor = sessionRequest
  const kind = drawer.value
  const paths = {product:'/products',mapping:'/mappings',period:'/periods',reportMonth:'/period-report-month',rule:'/rules',person:'/people',identity:'/identities',pull:'/orders/pull'}
  formError.value = ''
  saving.value = true
  try {
    const payload = bodyFor(kind,draft.value)
    const result = await api(paths[kind],{method:'POST',body:JSON.stringify(payload)})
    if (issuedFor!==sessionRequest) return
    notice.value = kind === 'pull' ? '同步完成，读取 '+result.facts+' 条业务记录，新增 '+result.newVersions+' 个版本。'
      : kind === 'rule' ? '规则 v'+result.version+' 已保存。' : '保存成功。'
    drawer.value = ''
    rulePreview.value = undefined
    if (kind === 'pull') {
      if (current.value === 'settlement') await workbench.value?.checkRefresh()
      if (current.value === 'orders') {
        filter.value = payload.orderId
        sourceFilter.value = payload.source
        resetOrders()
      }
    } else refresh()
  } catch (err) {
    if (issuedFor===sessionRequest) showFormError(err)
  } finally {
    if (issuedFor===sessionRequest) saving.value = false
  }
}
</script>

<template>
<a-config-provider :theme="theme" :locale="zhCN" :auto-insert-space-in-button="false">
  <main v-if="!session || sessionError || sessionChecking" class="workspace-content">
    <a-alert v-if="sessionError" :message="sessionError" type="error" show-icon>
      <template #action><a-button @click="loadSession">重试</a-button></template>
    </a-alert>
    <a-spin v-else tip="正在打开工作台"/>
  </main>
  <FinanceLogin v-else-if="!session.user && (!session.development || loginRequested)" :api="api"
    :development="session.development" @signed-in="loadSession" @cancel="cancelLogin"/>
  <main v-else-if="!visibleNavigation.length" class="workspace-content">
    <a-alert message="当前账号尚未配置工作台或产品规则权限" type="warning" show-icon>
      <template #action><a-button :loading="sessionBusy" @click="signOut">退出登录</a-button></template>
    </a-alert>
  </main>
  <div v-else-if="active" class="layout">
    <header class="topbar">
      <div class="path" aria-label="当前位置"><BankOutlined aria-hidden="true"/><span>财务平台</span><RightOutlined aria-hidden="true"/><b>{{ active.title }}</b></div>
      <div class="topbar-actions">
        <span v-if="session.user" class="finance-account-name">{{ session.user.name }}</span>
        <a-button v-if="session.user" type="text" size="small" :loading="sessionBusy" @click="signOut">退出登录</a-button>
        <a-button v-else type="text" size="small" @click="loginRequested=true">登录</a-button>
      </div>
    </header>
    <div class="workspace-bar">
      <div class="brand"><span class="brand-icon"><BankOutlined aria-hidden="true"/></span><b>财务工作台</b></div>
      <div class="workspace-bar-actions">
        <div v-if="usesWorkspaceMonth" class="workspace-month">
          <label for="workspace-month">结算月</label>
          <a-input id="workspace-month" v-model:value="workspaceMonth" type="month"/>
        </div>
        <a-button v-if="current!=='settlement'" :loading="loading" @click="refreshWorkspace"><template #icon><ReloadOutlined aria-hidden="true"/></template>刷新数据</a-button>
        <nav class="workspace-areas" aria-label="工作区">
          <a v-for="workspace in visibleWorkspaces" :key="workspace.key" :href="'#'+workspace.target"
            :aria-current="activeWorkspace?.key===workspace.key ? 'location' : undefined">{{ workspace.title }}</a>
        </nav>
      </div>
    </div>
    <div class="workspace-body" :class="{ 'with-scope':showScope }">
      <aside v-if="showScope" class="scope-panel" aria-label="筛选范围">
        <h2>范围</h2>
        <WorkspaceOrganizationScope v-if="usesOrganizationScope" :selected="unitScope" :nodes="scopeTree"
          :loading="loading && !scopeUnits" @select="selectOrganization" />
        <template v-if="usesProductScope">
          <label for="scope-product-search" class="desktop-scope scope-title">产品范围</label>
          <div class="desktop-scope">
            <a-input id="scope-product-search" v-model:value="scopeSearch" placeholder="搜索产品名称或编码" allow-clear/>
            <div class="scope-options" aria-label="产品范围">
              <button type="button" :aria-pressed="!productScope" @click="selectProduct()">全部产品</button>
              <button v-for="p in visibleProducts" :key="p.id" type="button" :aria-pressed="p.id===productScope" @click="selectProduct(p.id)">{{ p.name }}</button>
            </div>
          </div>
          <div class="mobile-scope">
            <label for="scope-product">产品范围</label>
            <a-select id="scope-product" :value="productScope" placeholder="全部产品" :options="scopeProductOptions" allow-clear @change="selectProduct"/>
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
        <section class="workspace-page-head">
          <div class="workspace-page-title">
            <h1>{{ active.title }}</h1>
            <a v-if="periodFromMonthly" class="workspace-return" href="#monthly"><LeftOutlined aria-hidden="true"/>返回月度汇总</a>
            <div v-if="actions && canOpenAction" class="topbar-actions">
              <a v-if="orderSyncReturnPeriod" class="workspace-return" :href="'#settlement?periodId='+orderSyncReturnPeriod+'&view=review'"><LeftOutlined aria-hidden="true"/>返回结算核对</a>
              <a-button type="primary" :disabled="!configurationReady" @click="open(actions)"><template #icon><component :is="current==='orders'?CloudSyncOutlined:PlusOutlined" aria-hidden="true"/></template>{{ titles[actions] }}</a-button>
            </div>
          </div>
          <nav v-if="workspacePages.length>1" class="workspace-nav" :aria-label="activeWorkspace?.title+'导航'">
            <a v-for="entry in workspacePages" :key="entry.key" :href="'#'+entry.key" :aria-current="current===entry.key ? 'page' : undefined">{{ entry.title }}</a>
          </nav>
        </section>
        <div class="workspace-page-body">
        <template v-if="current==='settlement'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
          <DraftWorkbench ref="workbench" v-model:period-id="workbenchPeriod" :periods="calculationPeriods" :component-options="options('component')" :member-options="classMembers" :report-dimensions="options('draftReportDimension')" :source-streams="[...options('dcaiOrderStream'), ...options('dczdOrderStream')]"
            :recovery-options="options('correctionRecovery')" :settlement-products="session.user?.settlementProducts ?? []"
            :api="api" :format-money="formatMoney" :format-time="formatTime" :unit-id="unitScope" :requested-period="requestedPeriod"
            :periods-loading="loading" :month="workspaceMonth" :requested-review="requestedReview"
            @show-order="orderDetails={versionId:$event}" @show-issue-order="orderDetails={versionId:$event,allowPull:true}"
            @show-applied-rule="showAppliedRule" />
        </template>
        <template v-else-if="current==='monthly'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <MonthlyReport ref="monthlyReport" :month="workspaceMonth" v-model:view="monthlyView" v-model:dimension="monthlyDimension"
            :api="api" :format-money="formatMoney" :product-id="productScope" :unit-id="unitScope" :unit-name="selectedUnit?.name"
            :resume="monthlyResume"
            :views="options('monthlyReportView')" :dimensions="options('draftReportDimension')"/>
        </template>
        <template v-else-if="current==='organization'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <OrganizationDirectory ref="organization" :api="api" :option-label="optionLabel" :format-time="formatTime"
            :can-write="session.user ? session.user.organizationWrite : session.development" :appointment-kinds="options('appointmentKind')"
            :catalog-roles="options('catalogAppointmentRole')" :catalog-scopes="options('catalogAppointmentScope')"/>
        </template>
        <template v-else>
        <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <div v-if="current==='orders' && data.sources.length" class="source-status-strip">
          <div v-for="entry in data.sources" :key="entry.source">
            <b>{{ sourceLabel(entry.source) }}</b><a-tag :color="entry.configured ? 'green' : 'default'">{{ entry.configured ? '已接入' : '未接入' }}</a-tag>
            <span v-if="entry.lastUpdate">最近入库 {{ formatTime(entry.lastUpdate) }}</span>
          </div>
        </div>
        <OrderSyncPanel v-if="current==='orders' && configurationReady && syncStreams.length"
          ref="orderSync" :api="api" :streams="syncStreams" :format-time="formatTime" :target="orderSyncTarget" @updated="refresh()"/>
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
                <a v-if="column.key==='name' && current==='periods'" :href="'#settlement?periodId='+record.id">{{ text }}</a>
                <template v-else-if="column.key==='name'||column.key==='label'">
                  <strong class="primary-cell">{{ text }}</strong>
                </template>
                <template v-else-if="column.key==='periodMode'"><a-tag :color="text==='class'?'green':'blue'">{{ optionLabel('periodMode',text) }}</a-tag></template>
                <template v-else-if="column.key==='mappingCount'">{{ data.mappings.filter(x=>x.productId===record.id).length }}</template>
                <template v-else-if="column.key==='productAction'">
                  <div class="product-links">
                    <a :href="'#periods?productId='+record.id" :aria-label="record.name+' 的核算期次'">核算期次</a>
                    <a v-if="canReadRules(record.id)" :href="'#rules?productId='+record.id" :aria-label="record.name+' 的产品规则'">产品规则</a>
                  </div>
                </template>
                <template v-else-if="column.key==='reportMonth'">
                  <a-button v-if="record.reportMonthEditable" type="link" size="small" :title="'设置 '+record.name+' 的报表月份'" @click="open('reportMonth',record)">{{ text }}</a-button>
                  <span v-else>{{ text }}</span>
                </template>
                <template v-else-if="column.key==='ruleVersion'">
                  <a-tag v-if="record.practiceRefundMonth" color="blue">按结算时规则</a-tag>
                  <a-tag v-else-if="record.classStartOutsidePeriod" color="orange" :title="formatTime(record.classStartsAt)">开班时间超出本期</a-tag>
                  <a-tag v-else-if="text" color="green">v{{ text }} · {{ record.ruleLabel }}</a-tag>
                  <a-tag v-else color="orange">{{ record.ruleMatchingAvailable ? '按订单匹配' : record.periodMode==='class' && !record.classStartsAt ? '待签到名单' : '待配规则' }}</a-tag>
                </template>
                <template v-else-if="column.key==='version'"><a-tag color="green">v{{ text }}</a-tag></template>
                <template v-else-if="column.key==='effectiveFrom'">
                  {{ text ? formatTime(text) : '范围默认' }}
                  <small v-if="record.effectiveTo" class="order-version">截止 {{ formatTime(record.effectiveTo) }}</small>
                </template>
                <template v-else-if="column.key==='scope'">
                  <template v-if="record.catalogPending">
                    {{ record.scopeLabel || catalogScopeLabels[record.scopeType] || '未细分范围' }}
                  </template>
                  <template v-else>{{ record.periodName || (record.fromOrdinal ? '从第 '+record.fromOrdinal+' 期起' : '产品默认') }}</template>
                </template>
                <template v-else-if="column.key==='components' && record.catalogPending">
                  <a-tag :color="catalogRuleBadge(record).color">{{ catalogRuleBadge(record).text }}</a-tag>
                </template>
                <template v-else-if="column.key==='ruleAction'">
                  <a-button v-if="record.catalogPending && record.rateConfigAvailable && record.projectMatches && (!session.user || session.user.rules.some(grant=>grant.productId===record.productId))"
                    type="link" size="small" @click="rateRule=record">分润配置</a-button>
                  <template v-else-if="!record.catalogPending">
                    <a-button type="link" size="small" @click="ruleDetails=record">查看规则</a-button>
                    <a-button v-if="canPublishRule(record.productId)" :disabled="!configurationReady"
                      type="link" size="small" @click="open('rule',record)">复制配置</a-button>
                  </template>
                </template>
                <template v-else-if="column.key==='components'">
                  <div v-for="(c,index) in text" :key="index">
                    <a-tag>{{ c.periodBenefit?.label || optionLabel('component',c.type) }}{{ c.classMember ? ' · '+optionLabel('classMember',c.classMember) : '' }}
                      {{ c.mode==='company_sales' ? c.companySales.categories.length+' 个类别' : c.mode==='period_rate' ? c.periodBenefit.tiers?.length ? '班次阶梯' : (c.value/10000)+'%' : c.mode==='tiered' ? tierLabel(c.tiers) : c.mode==='rate' ? (c.value/100)+'%' : formatMoney(c.value) }}</a-tag>
                    <small v-if="c.companySales?.categories.some(category=>category.recommendationRate>0 || category.overrides?.some(override=>override.recommendationRate>0))"
                      class="order-version">含公司推荐分润</small>
                    <small v-if="c.periodBenefit?.pool || c.retrainSplits?.length" class="order-version">含资金池归集</small>
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
                <template v-else-if="column.key==='orderNo'"><button class="order-link" type="button" @click="orderDetails={order:record,versionId:record.id,allowPull:true}" :title="'原订单 ID '+record.orderId">{{ text || record.orderId }}</button><small class="order-version">版本 {{ record.version }}</small></template>
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
        </div>
      </main>
    </div>
  </div>
  <a-drawer v-if="active && !sessionError && !sessionChecking && !loginRequested" :open="Boolean(drawer)"
    :title="rulePreview ? '预览分润规则' : drawer==='rule' && draft.copiedFrom ? '复制分润规则' : titles[drawer]"
    :width="drawer==='rule' ? 'min(620px, 100vw)' : 'min(510px, 100vw)'"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="close">
    <div v-if="formError" ref="formFeedback" tabindex="-1">
      <a-alert class="feedback" type="error" :message="formError" show-icon/>
    </div>
    <div v-if="rulePreview" ref="previewPane" class="rule-draft-preview" tabindex="-1" aria-label="发布前预览">
      <RuleVersionDetails :rule="rulePreview" embedded :recipients="classRuleDirectory"
        :option-label="optionLabel" :people="data.people" :opening-label="savedOpeningLabel"
        :format-time="formatTime" :format-money="formatMoney"/>
    </div>
    <a-form v-show="!rulePreview" id="finance-edit-form" :model="draft" layout="vertical" @finish="save">
      <template v-if="drawer==='product'">
        <a-form-item label="产品名称" required><a-input v-model:value="draft.name" aria-label="产品名称" :maxlength="80"/></a-form-item>
        <a-form-item label="产品编码" required><a-input v-model:value="draft.code" aria-label="产品编码" :maxlength="48"/></a-form-item>
        <a-form-item label="核算方式" html-for="product-period-mode"><a-select id="product-period-mode" v-model:value="draft.periodMode" :options="options('periodMode')"/></a-form-item>
        <a-form-item v-if="draft.periodMode==='calendar'" label="每月开始日" extra="填 1 表示自然月。填 25 表示上月 25 日至本月 24 日。" required><a-input-number v-model:value="draft.cycleStartDay" aria-label="每月开始日" :min="1" :max="28" :precision="0"/></a-form-item>
        <a-form-item v-if="draft.periodMode==='class'" label="退款账期每月开始日" :extra="firstOption('practiceRefundCalendar') ? '留空使用每月 '+firstOption('practiceRefundCalendar')+' 日开始。' : ''">
          <a-input-number v-model:value="draft.cycleStartDay" aria-label="退款账期每月开始日" :min="1" :max="28" :precision="0"/>
        </a-form-item>
      </template>
      <a-form-item v-if="['mapping','period','rule'].includes(drawer)" label="所属产品" required><a-select v-model:value="draft.productId" aria-label="所属产品" placeholder="选择产品" :options="drawer==='rule' ? ruleProductOptions : productOptions" :disabled="Boolean(draft.copiedFrom)"/></a-form-item>
      <a-form-item v-if="['mapping','pull','identity'].includes(drawer)" :label="drawer==='pull' ? '订单来源' : '业务来源'"><a-select v-model:value="draft.source" :aria-label="drawer==='pull' ? '订单来源' : '业务来源'" :options="sources" :disabled="drawer==='pull' && draft.lockedOrder"/></a-form-item>
      <template v-if="drawer==='mapping'">
        <a-form-item label="来源业务域" required><a-input v-model:value="draft.domain" aria-label="来源业务域" placeholder="填写业务接口返回的业务域"/></a-form-item>
        <a-form-item label="源产品标识" required><a-input v-model:value="draft.sourceKey" aria-label="源产品标识"/></a-form-item>
      </template>
      <template v-if="drawer==='period'">
        <a-form-item v-if="classRule" label="核算类型" html-for="practice-period-kind">
          <a-select id="practice-period-kind" v-model:value="draft.practicePeriodKind" :options="options('practicePeriodKind')"/>
        </a-form-item>
        <template v-if="calendarPeriod">
          <a-form-item label="结算月份" required><a-input v-model:value="draft.settlementMonth" aria-label="结算月份" type="month"/></a-form-item>
          <p v-if="calendarDates" class="period-window-preview" role="status">
            <span>核算日期</span><strong>{{ calendarDates }}</strong>
          </p>
          <a-form-item label="账期名称"><a-input v-model:value="draft.name" aria-label="账期名称" placeholder="默认使用结算月份"/></a-form-item>
        </template>
        <template v-else>
          <a-form-item label="期次名称" required><a-input v-model:value="draft.name" aria-label="期次名称"/></a-form-item>
          <a-form-item label="期数" required><a-input-number v-model:value="draft.ordinal" aria-label="期数" :min="1" :precision="0"/></a-form-item>
          <div class="field-grid"><a-form-item label="开始日期" required><a-input v-model:value="draft.startsOn" aria-label="开始日期" type="date"/></a-form-item><a-form-item label="结束日期" required><a-input v-model:value="draft.endsOn" aria-label="结束日期" type="date"/></a-form-item></div>
          <a-form-item label="来源期次标识" extra="填入子系统已确认签到名单的期次标识。">
            <a-input v-model:value="draft.sourcePeriodKey" aria-label="来源期次标识" placeholder="PRACTICE:1:1:123" :maxlength="80"/>
          </a-form-item>
          <a-form-item v-if="classRule" label="报表月份" extra="留空按班次结束日期归月。">
            <a-input v-model:value="draft.reportMonth" aria-label="报表月份" type="month"/>
          </a-form-item>
        </template>
      </template>
      <template v-if="drawer==='reportMonth'">
        <p>{{ draft.periodName }}</p>
        <a-form-item label="报表月份" extra="留空按班次结束日期归月。">
          <a-input v-model:value="draft.reportMonth" aria-label="报表月份" type="month"/>
        </a-form-item>
      </template>
      <template v-if="drawer==='rule'">
        <a-alert v-if="draft.copiedFrom" class="feedback" type="info" :message="'已带入 v'+draft.copiedFrom+' 的组件配置，请核对本次适用范围和生效时间。'" show-icon/>
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
          <a-input-number v-if="!['tiered','period_rate','company_sales'].includes(c.mode)" v-model:value="c.amount" :aria-label="'组件参数 '+(i+1)" :min="0" :max="c.mode==='rate'?100:1000000000" :precision="2"/>
          <a-button v-if="draft.components.length>1" type="link" danger @click="draft.components.splice(i,1)">移除</a-button>
          </div>
          <a-form-item v-if="classRule" :html-for="'rule-member-'+i" :label="'报名类型 '+(i+1)">
            <a-select :id="'rule-member-'+i" v-model:value="c.classMember" :options="availableClassMembers(c)" @change="resetAmountMode(c)"/>
          </a-form-item>
          <a-form-item v-if="['sales','referral'].includes(c.type)" :html-for="'rule-source-role-'+i" label="按订单中的谁计提" required>
            <a-select :id="'rule-source-role-'+i" v-model:value="c.sourceRole" :options="componentSourceRoles(c.sourceRole)"
              placeholder="选择订单角色" show-search option-filter-prop="label"/>
          </a-form-item>
          <PracticeTierEditor v-if="c.mode==='tiered'" :config="c.tierConfig" :metrics="options('tierMetric')" :start-modes="options('tierStartMode')" :amounts="baseModes" :people="classPeopleOptions" :openings="practiceOpeningOptions" :companies="options('practiceSalesCompany')"/>
          <PracticePeriodBenefitEditor v-if="c.mode==='period_rate'" :component="c" :index="i" :people="classPeopleOptions" :units="unitOptions"
            :companies="options('practiceSalesCompany')" :recipient-types="options('practicePeriodRecipient')"
            :appointment-kinds="options('appointmentKind')" :source-roles="options('practicePeriodSourceRole')"
            :pools="practicePoolOptions" :create-pool="createPracticePool"/>
          <PracticeRetrainSplitEditor v-if="classRule && c.type==='sales' && c.mode==='fixed' && c.classMember==='retrain'"
            :component="c" :index="i" :people="classPeopleOptions" :units="unitOptions" :companies="options('practiceSalesCompany')"
            :pools="practicePoolOptions" :create-pool="createPracticePool"/>
          <PracticeCompanySalesEditor v-if="c.mode==='company_sales'" :component="c" :index="i" :companies="options('practiceSalesCompany')"
            :recipients="companyRecipientOptions" :amounts="baseModes" :source-roles="options('practicePeriodSourceRole')"/>
          <a-form-item v-if="selectedProduct && (!classRule || !['rate','period_rate','company_sales'].includes(c.mode))" :label="'分项 '+(i+1)+' 的退款处理'"
            :extra="classRule ? (c.retrainSplits?.length ? '仅适用于未设置公司拆分的固定提成。' : '适用于确认名单前的固定金额分项或固定金额档位。') : '退款按计提时使用的规则版本处理。'">
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
        <a-alert v-if="!(draft.lockedOrder ? draft.sourceConfigured : data.sources.find(x=>x.source===draft.source)?.configured)"
          class="feedback" type="info" message="此来源尚未配置接口，请先在本机服务配置连接。" show-icon/>
        <a-form-item v-if="draft.source==='DCAI'" label="订单类型"><a-select v-model:value="draft.stream" aria-label="订单类型" :options="options('dcaiOrderStream')"/></a-form-item>
        <a-form-item label="原订单 ID" required><a-input v-model:value="draft.orderId" aria-label="原订单 ID" :disabled="draft.lockedOrder"/></a-form-item>
      </template>
    </a-form>
    <template #footer>
      <div class="workspace-edit-footer">
        <a-button v-if="rulePreview" :disabled="saving" @click="returnToRuleEditor">返回编辑</a-button>
        <template v-else>
          <a-button :disabled="saving" @click="close">取消</a-button>
          <a-button v-if="drawer==='rule'" id="rule-preview-button" :disabled="saving" @click="previewRule">预览规则</a-button>
        </template>
        <a-button html-type="submit" form="finance-edit-form" type="primary" :loading="saving">{{ drawer==='rule'?'保存并生效':drawer==='pull'?'开始拉取':'保存' }}</a-button>
      </div>
    </template>
  </a-drawer>
  <OrderDetails v-if="orderDetails" :selection="orderDetails" :api="api" :format-money="formatMoney" :format-time="formatTime" :source-label="sourceLabel"
    :labels="labels" :role-label="orderRoleLabel" :can-pull="Boolean(orderDetails.allowPull && configurationReady)"
    @close="orderDetails=undefined" @pull="pullFromDetails"/>
  <a-drawer v-if="appliedRule?.loading || appliedRule?.error" open title="计提使用的规则" @close="appliedRule=undefined">
    <a-alert v-if="appliedRule.error" :message="appliedRule.error" type="error" show-icon/>
    <a-spin v-else tip="正在读取规则"/>
  </a-drawer>
  <CatalogRuleRates v-if="(rateRule || appliedRule?.catalog) && active && !sessionError && !sessionChecking && !loginRequested"
    :rule="rateRule" :applied="appliedRule?.catalog ? appliedRule : undefined" :api="api" :format-time="formatTime"
    @close="rateRule=undefined; appliedRule=undefined" @published="ratePublished"/>
  <RuleVersionDetails v-if="viewedRule && active && !sessionError && !sessionChecking && !loginRequested"
    :rule="viewedRule" :can-copy="Boolean(ruleDetails && configurationReady && canPublishRule(viewedRule.productId))"
    :title="appliedRule?.rule ? '计提使用的规则' : undefined"
    :recipients="appliedRule?.directory"
    :class-product="data.products.find(product => product.id===viewedRule.productId)?.periodMode==='class'"
    :api="api" :people="data.people" :option-label="optionLabel" :opening-label="savedOpeningLabel"
    :format-time="formatTime" :format-money="formatMoney" @close="ruleDetails=undefined; appliedRule=undefined" @copy="copyRuleDetails"/>
</a-config-provider>
</template>

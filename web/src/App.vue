<script setup>
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ApartmentOutlined, BankOutlined, CloudSyncOutlined, DownOutlined, IdcardOutlined, LeftOutlined, PlusOutlined, ReloadOutlined, RightOutlined, ShopOutlined,
} from '@ant-design/icons-vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import PeriodTasks from './PeriodTasks.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import PracticePersonalScopeFields from './PracticePersonalScopeFields.vue'
import FinanceLogin from './FinanceLogin.vue'
import WorkspaceOrganizationScope from './WorkspaceOrganizationScope.vue'
import RuleImpactPreview from './RuleImpactPreview.vue'
import { organizationTree } from './organization-tree'
import { teamPoolKey } from './team-pool-key'
import { catalogAppointmentLevels, checkPracticeCompanyArrangementScopes, copiedRuleComponents, hasPracticePaymentScope, practiceLegacyArrangementLabel,
  practiceCompanyStandardIndicators, practiceCountStartBody, practiceCumulativeAmountMetrics, practicePeriodScopeText, practiceRetrainServiceScopeText, practiceRetrainShareText, practiceRetrainSplitBody,
  practiceRecipientIsPerson, practiceSalesScopeText, practiceStandardRateSummary, ruleComponentLabel, tierFormUsesHistory } from './rule-component-form'

const DraftWorkbench = defineAsyncComponent(() => import('./DraftWorkbench.vue'))
const MonthlyReport = defineAsyncComponent(() => import('./MonthlyReport.vue'))
const OrganizationDirectory = defineAsyncComponent(() => import('./OrganizationDirectory.vue'))
const OrganizationPersonDetail = defineAsyncComponent(() => import('./OrganizationPersonDetail.vue'))
const OrderDetails = defineAsyncComponent(() => import('./OrderDetails.vue'))
const OrderSyncPanel = defineAsyncComponent(() => import('./OrderSyncPanel.vue'))
const OrderSyncSettings = defineAsyncComponent(() => import('./OrderSyncSettings.vue'))
const RuleVersionDetails = defineAsyncComponent(() => import('./RuleVersionDetails.vue'))
const CatalogRuleRates = defineAsyncComponent(() => import('./CatalogRuleRates.vue'))
const CatalogPeriodRule = defineAsyncComponent(() => import('./CatalogPeriodRule.vue'))
const CatalogScenarioCreate = defineAsyncComponent(() => import('./CatalogScenarioCreate.vue'))
const PracticeRuleSettings = defineAsyncComponent(() => import('./PracticeRuleSettings.vue'))
const PracticePeriodBenefitEditor = defineAsyncComponent(() => import('./PracticePeriodBenefitEditor.vue'))
const PracticeRetrainSplitEditor = defineAsyncComponent(() => import('./PracticeRetrainSplitEditor.vue'))
const PracticeRetrainServiceEditor = defineAsyncComponent(() => import('./PracticeRetrainServiceEditor.vue'))
const PracticeCompanySalesEditor = defineAsyncComponent(() => import('./PracticeCompanySalesEditor.vue'))
const PracticeTierEditor = defineAsyncComponent(() => import('./PracticeTierEditor.vue'))
const PracticeRosterSelect = defineAsyncComponent(() => import('./PracticeRosterSelect.vue'))
const DepartmentRetention = defineAsyncComponent(() => import('./DepartmentRetention.vue'))
const TeamPools = defineAsyncComponent(() => import('./TeamPools.vue'))
const PoolDirectory = defineAsyncComponent(() => import('./PoolDirectory.vue'))
const navigation = [
  {key:'tasks', area:'work', title:'待办'},
  {key:'settlement', area:'work', title:'分润试算'},
  {key:'monthly', area:'work', title:'月度汇总'},
  {key:'pools', area:'work', title:'团队池'},
  {key:'retention', area:'work', title:'部门留存金'},
  {key:'orders', area:'work', title:'来源订单'},
  {key:'products', area:'config', title:'产品目录'},
  {key:'periods', area:'config', title:'核算期次'},
  {key:'rules', area:'config', title:'产品规则'},
  {key:'pool-config', area:'config', title:'团队池配置'},
  {key:'sync-config', area:'config', title:'数据同步配置'},
  {key:'organization', area:'data', title:'组织与人员'},
  {key:'people', area:'data', title:'人员映射'},
]
const workspaces = [{key:'work',title:'工作台'}, {key:'config',title:'配置'}, {key:'data',title:'系统与数据'}]
const organizationPrimaryPages = [
  {page:'organization',tab:'organization',label:'组织任职',icon:ApartmentOutlined},
  {page:'people',tab:'main',label:'人员档案',icon:IdcardOutlined},
  {page:'organization',tab:'companies',label:'销售公司',icon:ShopOutlined},
]
const organizationPages = [
  ...organizationPrimaryPages,
  {page:'organization',tab:'sync',label:'下发记录'},
  {page:'organization',tab:'duties',label:'业务职责'},
  {page:'organization',tab:'directors',label:'总监归属'},
  {page:'organization',tab:'catalog',label:'分润岗位'},
  {page:'people',tab:'identity',label:'来源账号'},
  {page:'organization',tab:'health',label:'AI 组织冲突'},
]
function isActiveOrganizationPage(entry) {
  return current.value===entry.page && (entry.page==='people' ? tab.value : organizationPanel.value)===entry.tab
}
const OrganizationPeopleDesk = defineAsyncComponent(() => import('./OrganizationPeopleDesk.vue'))
const peopleDesk = ref()
const organizationPanel = ref('organization')
const companyRouteId = ref()
const personPage = ref()
const personDetail = ref()
let personListScroll = 0
function organizationHref(page, tab) {
  if (tab==='sync') return '#organization?tab=sync&view=records'
  return '#'+page+(['main','organization'].includes(tab) ? '' : '?tab='+tab)
}
function personLink(id, section, companyId) {
  const base = organizationHref(current.value, current.value==='people' ? tab.value : organizationPanel.value)
  const params = new URLSearchParams(base.split('?')[1])
  params.set('personId',id)
  if (section) params.set('section',section)
  const company = companyId || personPage.value?.companyId
  if (organizationPanel.value==='companies' && current.value==='organization' && company) params.set('companyId',company)
  return '#'+current.value+'?'+params
}
function clearCompanyRoute() {
  if (companyRouteId.value) location.hash = organizationHref('organization','companies')
}
function navigateOrganizationPanel(panel) {
  location.hash = organizationHref('organization',panel)
}
const lastWorkspacePages = ref({})
const session = ref()
const sessionError = ref('')
const loginRequested = ref(false)
const sessionBusy = ref(false)
const sessionChecking = ref(false)
const wholeWorkspace = computed(() => session.value?.user ? session.value.user.workspace : session.value?.development)
const visibleNavigation = computed(() => wholeWorkspace.value ? navigation : session.value?.user?.rules.length
  ? navigation.filter(entry => ['rules','pool-config'].includes(entry.key)) : [])
const getRoute = key => {
  if (visibleNavigation.value.some(entry => entry.key === key)) return key
  return visibleNavigation.value[0]?.key
}
const current = ref()
const requestedPeriod = ref()
const requestedView = ref()
const requestedRulePeriod = ref()
const requestedPracticeSettings = ref()
const ruleReturn = ref()
const reviewRootPeriod = ref()
const configurationReturn = ref()
const orderSyncTarget = ref()
const orderReturn = ref()
const draftViews = {detail:'分润明细',report:'业绩与分润',issues:'待核',pools:'团队池分配',review:'结算核对',matching:'订单规则匹配'}
const workbenchPeriod = ref()
const workbenchView = ref('detail')
const workbenchQuery = ref({})
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
const workProductScope = ref()
const configProductScope = ref()
const ruleBusinessScope = ref()
const activeRuleComponent = ref(0)
const unitScope = ref()
const usesOrganizationScope = computed(() => ['orders','settlement','monthly','retention'].includes(current.value))
const scopeSearch = ref('')
const configProductPages = new Set(['products','periods','rules','pool-config'])
const activeProductScope = computed(() => configProductPages.has(current.value) ? configProductScope : workProductScope)
const productScope = computed({
  get: () => activeProductScope.value.value,
  set: value => { activeProductScope.value.value = value },
})
const productScopedPages = new Set(['products','periods','rules','tasks','settlement','monthly','pools'])
const usesProductScope = computed(() => productScopedPages.has(current.value))
const periodWorkflowPages = new Set(['tasks','settlement','pools','rules'])
const monthPeriodPages = new Set(['tasks','settlement','pools'])
let periodContextID
const ruleBusinessOptions = computed(() => {
  const products = data.value.products.filter(product=>!productScope.value || product.id===productScope.value)
  return products.flatMap(product=>{
    const prefix = productScope.value ? '' : product.name+' · '
    const choices = (product.businessSkus || []).map(sku=>({
      value:product.id+':sku:'+sku.code,label:sku.label.startsWith(product.name) ? sku.label : prefix+sku.label,code:sku.code,
    }))
    const productRules = data.value.catalogRules.filter(rule=>rule.productId===product.id)
    if (productRules.some(rule=>rule.mixedArrangements)) {
      choices.push({value:product.id+':mixed',label:prefix+'统一规则'})
    }
    if (productRules.some(rule=>!rule.mixedArrangements && rule.scopeType==='CROSS_SCENARIO_AGGREGATE')) {
      choices.push({value:product.id+':aggregate',label:prefix+'跨 SKU 汇总'})
    }
    if (product.periodMode==='class') {
      for (const member of options('classMember').filter(member=>member.value)) {
        choices.push({value:product.id+':member:'+member.value,label:prefix+member.label})
      }
    }
    return choices
  })
})
watch(productScope,()=>{
  ruleBusinessScope.value=undefined
  loadingRule.value=undefined
})
const showScope = computed(() => usesOrganizationScope.value || (usesProductScope.value && current.value!=='periods'))
const organization = ref()
const monthlyReport = ref()
const retention = ref()
const poolDirectory = ref()
const poolConfigProduct = ref()
const teamPoolPeriod = ref()
const requestedPoolPeriod = ref()
const poolContext = ref(new URLSearchParams())
const retentionResume = ref()
const periodTasks = ref()
const tasksMode = ref('pending')
const tasksReturn = ref()
const workspaceMonth = ref(new Intl.DateTimeFormat('sv-SE', {year:'numeric',month:'2-digit',timeZone:'Asia/Shanghai'}).format(new Date()))
const usesWorkspaceMonth = computed(() => ['tasks','settlement','monthly','pools','retention'].includes(current.value))
const monthlyView = ref()
const monthlyDimension = ref()
const periodOrigin = ref()
const periodOrigins = {monthly:'月度汇总',tasks:'待办',retention:'部门留存金',pools:'团队池',periods:'核算期次',person:'个人业绩与分润'}
const personReportOrigin = ref()
const monthlyReturn = ref()
const monthlyResume = ref()
const orderSync = ref()
const orderSyncSettings = ref()
const orderDetails = ref()
const rateRule = ref()
const periodRule = ref()
const catalogScenarioCreation = ref()
const ruleDetails = ref()
const loadingRule = ref()
const ruleRetirement = ref()
const retiringRule = ref(false)
const retirementError = ref('')
const appliedRule = ref()
const rulePreview = ref()
const ruleImpactBusy = ref(false)
const previewPane = ref()
const viewedRule = computed(() => ruleDetails.value || appliedRule.value?.rule)
const emptyData = () => ({products:[], mappings:[], periods:[], rules:[], orders:[], people:[], identities:[], sources:[], practiceCountOpenings:[], catalogRules:[]})
const data = ref(emptyData())
const loading = ref(false)
const saving = ref(false)
const globalError = ref('')
const notice = ref('')
const pulledOrderLink = ref()
const formError = ref('')
const formFeedback = ref()
const drawer = ref('')
const tab = ref('main')
const draft = ref({})
const filter = ref('')
const orderQuery = ref('')
const periodQuery = ref('')
const nextPeriodCursor = ref()
const identityAccount = ref()
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
const classMembers = computed(() => options('classMember').map(option=>option.value ? option : {...option,label:'新报和复训'}))
const practiceActivityTypes = computed(() => options('practiceActivityType').map(option=>({...option,value:Number(option.value)})))
function componentSourceRoles(saved) {
  const configured = options('orderAttributionRole')
  return saved && !configured.some(option => option.value === saved)
    ? [...configured, {value:saved,label:saved}] : configured
}
const ruleAmountModes = computed(() => options('amountMode').map(option => ({...option,
  label:option.value==='rate' ? '比例' : option.value==='fixed' ? '固定金额' : option.label,
})))
const baseModes = computed(() => ruleAmountModes.value.filter(option => !option.scope?.componentTypes))
function matchesOption(option, periodMode, type, member) {
  const scope = option.scope || {}
  return (!scope.periodModes || scope.periodModes.includes(periodMode)) &&
    (!scope.componentTypes || scope.componentTypes.includes(type)) &&
    (!scope.classMembers || scope.classMembers.includes(member))
}
const productOptions = computed(() => data.value.products.map(p => ({value:p.id,label:p.name})))
const periodProductOptions = computed(() => [{value:0,label:'全部产品'}, ...productOptions.value])
const canReadRules = productId => session.value?.user
  ? session.value.user.rules.some(rule=>rule.productId===productId) : session.value?.development
const poolProducts = computed(() => data.value.products.filter(product=>['class','calendar'].includes(product.periodMode) && canReadRules(product.id)))
const scopedProducts = computed(() => data.value.products.filter(p => current.value !== 'rules' || canReadRules(p.id)))
const scopeProductOptions = computed(() => scopedProducts.value.map(p=>({value:p.id,label:p.name})))
const canPublishRule = productId => session.value?.user
  ? session.value.user.rules.some(rule => rule.productId===productId && rule.publish) : session.value?.development
const practiceRuleTabs = [
  {key:'',label:'分润规则'}, {key:'count-starts',label:'累计起点'},
]
const practiceRulesProduct = computed(() => current.value==='rules' && !requestedRulePeriod.value && canReadRules(productScope.value)
  ? data.value.products.find(product=>product.id===productScope.value && product.periodMode==='class') : undefined)
const practiceSettingsView = computed(() => practiceRulesProduct.value ? requestedPracticeSettings.value : undefined)
function practiceRulesLink(tab) {
  const params = new URLSearchParams({productId:practiceRulesProduct.value.id})
  if (tab) params.set('practiceConfig',tab)
  return '#rules?'+params
}
function classRuleSetupLink(period, fromWorkbench = false) {
  if (period?.periodMode==='class' && period.classStartsAt && !period.classStartOutsidePeriod
      && !period.ruleVersion && canPublishRule(period.productId)) {
    const params = new URLSearchParams({productId:period.productId,periodId:period.id})
    if (fromWorkbench) params.set('from','settlement')
    if (fromWorkbench && periodOrigin.value) params.set('returnFrom',periodOrigin.value)
    if (fromWorkbench && periodOrigin.value==='person') params.set('fromPerson',personReportOrigin.value)
    if (fromWorkbench && periodOrigin.value==='pools') copyPoolContext(poolContext.value,params)
    if (fromWorkbench && reviewRootPeriod.value) params.set('reviewRootPeriodId',reviewRootPeriod.value)
    return '#rules?'+params
  }
}
const ruleTargetPeriod = computed(() => {
  if (current.value!=='rules' || !requestedRulePeriod.value || !canPublishRule(productScope.value)) return
  return data.value.periods.find(period=>period.id===requestedRulePeriod.value
    && period.productId===productScope.value && period.periodMode==='class' && !period.practiceRefundMonth)
})
const ruleProductOptions = computed(() => data.value.products.filter(p=>(!p.catalogPending || p.periodMode==='class') && canPublishRule(p.id))
  .map(p=>({value:p.id,label:p.name})))
const editableCatalogProducts = computed(() => current.value==='rules' ? data.value.products
  .filter(p=>(!productScope.value || p.id===productScope.value) && canPublishRule(p.id)) : [])
const catalogScenarioProducts = computed(() => editableCatalogProducts.value
  .filter(p=>p.code!=='DCAI_VIP' && p.businessSkus?.some(sku=>sku.paymentRule)).map(p=>({value:p.id,label:p.name})))
const catalogPeriodProducts = computed(() => editableCatalogProducts.value
  .filter(p=>p.code!=='DCAI_VIP' && p.catalogPeriodRule).map(p=>({value:p.id,label:p.name})))
const mixedCatalogProducts = computed(() => editableCatalogProducts.value
  .filter(p=>p.code==='DCAI_VIP' && p.catalogPeriodRule).map(p=>({value:p.id,label:p.name})))
const canCreateMixedRule = computed(() => mixedCatalogProducts.value.some(product=>
  !data.value.catalogRules.some(rule=>rule.productId===product.value && rule.mixedArrangements)))
const visibleProducts = computed(() => {
  const term = scopeSearch.value.trim().toLowerCase()
  return term ? scopedProducts.value.filter(p => (p.name+' '+p.code).toLowerCase().includes(term)) : scopedProducts.value
})
const peopleOptions = computed(() => data.value.people.map(p => ({value:p.id,label:p.name})))
const identifiedPersonOption = person => ({value:person.id,label:person.name+' · '+person.id})
const identityPeopleOptions = computed(() => data.value.people.map(identifiedPersonOption))
const classRuleDirectory = ref()
const registrationCompanies = ref()
const registrationCompaniesLoading = ref(false)
const registrationCompaniesError = ref('')
const classPeopleOptions = computed(() => classRuleDirectory.value?.people.map(identifiedPersonOption) || identityPeopleOptions.value)
const unitOptions = computed(() => classRuleDirectory.value?.units.map(unit=>({value:unit.id,label:unit.scopeName || unit.name})) || [])
const salesUnitOptions = computed(() => classRuleDirectory.value?.units.filter(unit=>['COMPANY','DEPARTMENT','TEAM'].includes(unit.kind))
  .map(unit=>({value:unit.id,kind:unit.kind,label:(unit.scopeName || unit.name)+' · '+optionLabel('organizationKind',unit.kind)})) || [])
const practiceScopeNames = computed(() => {
  const names = new Map((classRuleDirectory.value?.units || []).map(unit=>[unit.id,unit.scopeName || unit.name]))
  const personNames = new Map(classPeopleOptions.value.map(person=>[person.value,person.label]))
  const company = id => optionLabel('practiceSalesCompany',id) || '未选公司'
  return {
    unit:id=>names.get(id) || '组织名称待核',
    person:id=>personNames.get(id) || '人员 '+id,
    company,
    category:id=>optionLabel('practiceSalesCategory',id) || id,
    registrationCompany:id=>registrationCompanies.value?.find(option=>option.value===id)?.label || company(id),
  }
})
function latestComponentRuleIDs(sameEffectiveTime) {
  const latest = new Map()
  for (const rule of data.value.rules) {
    const scope = JSON.stringify([rule.productId,rule.periodId,rule.fromOrdinal,rule.activityType || 0,sameEffectiveTime ? rule.effectiveFrom : undefined])
    if (!latest.has(scope) || latest.get(scope).version < rule.version) latest.set(scope,rule)
  }
  return new Set([...latest.values()].map(rule=>rule.id))
}
const ruleEditVersionIDs = computed(() => latestComponentRuleIDs(true))
const ruleRetirementIDs = computed(() => latestComponentRuleIDs(false))
const practicePoolOptions = computed(() => classRuleDirectory.value?.pools?.map(pool=>({value:pool.id,label:pool.name+' · '+pool.unitName})) || [])
const companyRecipientOptions = computed(() => classRuleDirectory.value?.units.filter(unit=>unit.kind==='COMPANY')
  .map(unit=>({value:unit.id,label:unit.name})) || [])
const periodOptions = computed(() => data.value.periods.filter(p => p.productId === draft.value.productId && !p.practiceRefundMonth)
  .map(p => ({value:p.id,label:p.name})))
const previewPeriods = computed(() => {
  const rule = rulePreview.value
  if (!rule) return []
  const effective = rule.effectiveFrom ? Date.parse(rule.effectiveFrom) : -Infinity
  return data.value.periods.filter(period=>period.productId===rule.productId
    && !period.settled && !period.practiceRefundMonth && (!rule.periodId || period.id===rule.periodId)
    && (!rule.fromOrdinal || period.ordinal>=rule.fromOrdinal)
    && (!rule.activityType || Number(period.sourcePeriodKey?.split(':')[1])===rule.activityType)
    && (period.periodMode!=='class' || !period.classStartOutsidePeriod && Date.parse(period.classStartsAt)>=effective))
})
const selectedProduct = computed(() => data.value.products.find(p => p.id === draft.value.productId))
const calculationPeriods = computed(() => data.value.periods.filter(p =>
  (!productScope.value || p.productId === productScope.value) && (!workspaceMonth.value || p.reportMonth === workspaceMonth.value)))
const selectedWorkbenchPeriod = computed(() => calculationPeriods.value.find(period=>period.id===workbenchPeriod.value))
const issueRuleLink = computed(() => {
  const period = selectedWorkbenchPeriod.value
  if (period && canReadRules(period.productId)) return workbenchPageLink('rules',{productId:period.productId})
})
watch(() => data.value.periods.find(period => period.id === (requestedPeriod.value || requestedPoolPeriod.value))?.reportMonth, month => {
  if (month) workspaceMonth.value = month
})
watch(() => data.value.periods.find(period=>period.id===requestedPoolPeriod.value)?.productId, id => {
  if (current.value==='pools' && id && productScope.value && productScope.value!==id) productScope.value = undefined
})
const classRule = computed(() => selectedProduct.value?.periodMode === 'class')
const healthPracticeRule = computed(() => classRule.value && selectedProduct.value.code==='DCZD_PRACTICE')
const allowsPracticeFallback = row => healthPracticeRule.value &&
  row.type==='sales' && row.classMember==='new'
const allowsPracticeOpening = row => !row.tierConfig?.cumulativeMetric && !hasPracticePaymentScope(row.saleScope || {})
const practiceRetrainSales = computed(() => classRule.value
  ? draft.value.components?.find(part=>part.type==='sales' && ['fixed','rate'].includes(part.mode) && part.classMember==='retrain' && part.retrainSplits?.length) : undefined)
const retrainSplits = computed(() => practiceRetrainSales.value?.retrainSplits || [])
const recordedServiceShares = computed(() => retrainSplits.value.filter(split=>split.serviceEnabled))
const serviceCardKey = companyId => 'retrain-service-'+companyId
function removeRetrainService(split) {
  split.serviceEnabled = false
  split.servicePersonId = undefined
  split.service = undefined
  split.serviceScopeMode = 'all'
  activeRuleComponent.value = undefined
}
const periodBenefitMode = computed(() => options('amountMode').find(option=>option.value==='period_rate'))
const isPeriodBenefit = type => classRule.value && ['manager','delivery','service'].includes(type)
const companySalesMode = computed(() => options('amountMode').find(option=>option.value==='company_sales'))
const componentMode = type => isPeriodBenefit(type) ? 'period_rate' : type==='company_sales' ? 'company_sales' : undefined
const amountModes = row => {
  const expected = componentMode(row.type)
  return ruleAmountModes.value.filter(option =>
    matchesOption(option, selectedProduct.value?.periodMode, row.type, row.classMember) &&
    (expected ? option.value===expected : !['period_rate','company_sales'].includes(option.value)))
    .map(option=>option.value==='tiered' && row.retrainSplits?.length
      ? {...option,disabled:true,label:'阶梯（当前使用复训分账）'} : option)
}
function updateAmountMode(row, mode) {
  if (row.mode !== mode && (mode === 'rate' || mode === 'fixed')) {
    row.amount = undefined
    if (classRule.value && mode==='fixed' && ['sales','referral'].includes(row.type) &&
        options('refundPolicy').some(option=>option.value==='proportional')) row.refundPolicy ||= 'proportional'
  }
  row.mode = mode
  if (mode==='period_rate' && !row.periodBenefit) {
    row.periodBenefit = {label:'',pool:{},tiers:[{from:0}],method:'rate'}
  }
  if (mode==='company_sales' && !row.companySales) {
    row.sourceRole = row.sourceRole || firstOption('practicePeriodSourceRole')
    row.companySales = {label:'',payee:{kind:'sales_company',pool:{}},categories:[]}
  }
  if (mode === 'tiered' && !row.tierConfig) {
    row.tierConfig = {
      metric:'count',
      forceCount:true,
      startMode:firstOption('tierStartMode'),
      count:[{mode:baseModes.value[0]?.value}],
      performance:[{from:0,mode:baseModes.value[0]?.value}],
      personCountStarts:[],
      categories:[],
      companies:[],
      people:[],
      onlyOverrides:options('practiceSalesCategory').length > 0,
    }
  }
}
function resetAmountMode(row) {
  const modes = amountModes(row)
  if (!modes.some(option => option.value === row.mode)) updateAmountMode(row,modes[0]?.value)
}
function changeClassMember(row, member) {
  row.classMember = member
  if (row.tierConfig && member!=='new') {
    row.tierConfig.includeRetrain = false
    if (row.tierConfig.forceFallback && !row.tierConfig.hasLegacyMetrics) {
      row.tierConfig.forceFallback = false
      row.tierConfig.forceCount = true
    }
  }
  resetAmountMode(row)
}
function tierIncludesRetrain(row) {
  return row.mode==='tiered' && Boolean(row.tierConfig?.includeRetrain ?? row.tiers?.includeRetrain)
}
const componentTypes = computed(() => options('component').filter(option=>
  matchesOption(option,selectedProduct.value?.periodMode) &&
  (!isPeriodBenefit(option.value) || !!periodBenefitMode.value?.scope?.componentTypes?.includes(option.value)) &&
  (option.value!=='company_sales' || !!companySalesMode.value)))
const recipientChoices = computed(() => {
  if (drawer.value!=='rule' || !selectedProduct.value) return []
  const choices = [
    {type:'sales',label:'订单上的成交销售'},
    {type:'referral',label:'订单上的推荐人'},
    ...(classRule.value ? [
      {type:'company_sales',label:'公司销售提成'},
      {type:'service',label:'服务提成'},
      {type:'delivery',label:'交付提成'},
      ...(selectedProduct.value?.directorRulesAvailable ? [{key:'director_rate',type:'manager',label:'按班期归属的总监提成'}] : []),
      {key:'referral_company',type:'manager',recipient:'referral_company',label:'推荐公司提成'},
      {type:'manager',label:'独立收款安排'},
    ] : []),
  ]
  return choices.filter(choice=>componentTypes.value.some(type=>type.value===choice.type)).map(choice=>({
    ...choice,key:choice.key || choice.type,disabled:!classRule.value && draft.value.components.some(row=>row.type===choice.type),
  }))
})
function addRuleComponent(key) {
  if (key==='director_rate') {
    draft.value.components.push({type:'manager',mode:key,classMember:'',amount:undefined})
    activeRuleComponent.value=draft.value.components.length-1
    return
  }
  const {type,recipient,label} = recipientChoices.value.find(choice=>choice.key===key)
  const members = classRule.value ? classMembers.value : []
  const member = type==='company_sales' ? members.find(option=>!draft.value.components.some(
    part=>part.type===type && part.classMember===option.value)) || members[0] : members[0]
  const row = {type,classMember:member?.value,
    mode:baseModes.value[0]?.value}
  if (classRule.value && ['sales','referral'].includes(type)) row.label = optionLabel('component',type)
  resetAmountMode(row)
  if (row.periodBenefit && ['service','delivery'].includes(type)) row.periodBenefit.label=optionLabel('component',type)
  if (row.periodBenefit && recipient) Object.assign(row.periodBenefit,{recipient,label})
  draft.value.components.push(row)
  activeRuleComponent.value = draft.value.components.length-1
}
function ruleRecipientSummary(row) {
  if (row.mode==='director_rate') return '按生效归属收款'+(row.amount==null ? '' : ' · '+row.amount+'%')
  if (row.periodBenefit) {
    const benefit = row.periodBenefit
    const payee = benefit.recipient==='pool' ? practicePoolOptions.value.find(option=>option.value===benefit.pool?.poolId)?.label || '团队池'
      : benefit.recipient==='unit' ? unitOptions.value.find(option=>option.value===benefit.unitId)?.label || '指定组织'
      : benefit.recipient==='person' ? classPeopleOptions.value.find(option=>option.value===benefit.personId)?.label || '指定个人'
      : benefit.recipient==='order_person' ? orderRoleLabel(benefit.orderRecipientRole) || '订单上的人员'
      : benefit.recipient==='order_appointment' ? [catalogAppointmentLevels.find(option=>option.value===benefit.orderAppointmentLevel)?.label,
        optionLabel('appointmentKind',benefit.orderAppointmentKind)].filter(Boolean).join(' · ') || '待选择任职'
      : optionLabel('practicePeriodRecipient',benefit.recipient) || '选择收款对象'
    const amount = benefit.method==='tiers' ? '阶梯' : benefit.method==='rate'
      ? (benefit.standardRates ? practiceStandardRateSummary(benefit.standardRates,value=>value+'%') : row.amount == null ? '' : row.amount+'%')
      : (benefit.fixedAmount == null ? '' : formatMoney(minor(benefit.fixedAmount))+
        (benefit.method==='fixed' ? ' / 报名' : benefit.method==='order_fixed' ? ' / 笔'
          : benefit.recipient==='order_appointment' && benefit.method==='class_fixed' ? ' / 组织 / 班' : ''))
    return [payee,amount].filter(Boolean).join(' · ')
  }
  if (row.mode==='company_sales') {
    const payee = row.companySales?.payee
    let who = payee?.kind==='sales_company' ? '销售所属公司' : '选择收款对象'
    if (payee?.kind==='order_person') who = optionLabel('orderAttributionRole',payee.role) || '订单上的人员'
    if (payee?.kind==='person') who = classPeopleOptions.value.find(option=>option.value===payee.personId)?.label || '指定个人'
    if (payee?.kind==='company') who = companyRecipientOptions.value.find(option=>option.value===payee.companyId)?.label || '指定公司'
    if (payee?.kind==='pool') who = practicePoolOptions.value.find(option=>option.value===payee.pool?.poolId)?.label || '团队池'
    return [who,practiceRecipientIsPerson(payee?.kind) && optionLabel('organizationDuty',row.recipientDuty)].filter(Boolean).join(' · ')
  }
  const person = row.salesRecipientKind==='person' ? (row.salesPersonId ? practiceScopeNames.value.person(row.salesPersonId) : '待选择收款人')
    : row.sourceRole ? optionLabel('orderAttributionRole',row.sourceRole) : '选择订单上的收款人'
  const who = [person,optionLabel('organizationDuty',row.recipientDuty)].filter(Boolean).join(' · ')
  if (row===practiceRetrainSales.value && row.retrainSplits?.length) return who+' · 按公司分账'
  if (row.mode==='tiered') {
    const metric = practiceCumulativeAmountMetrics.find(option=>option.value===row.tierConfig?.cumulativeMetric)?.label
    return who+' · '+(metric || (row.tierConfig?.forceCount ? '累计新报人数' : '按阶梯'))
  }
  if (row.amount == null) return who
  return who+' · '+(row.mode==='fixed' ? formatMoney(Math.round(row.amount*100)) : row.amount+'%')
}
function ruleSalesScope(row, limit) {
  return classRule.value ? practiceSalesScopeText(row,practiceScopeNames.value,limit) : undefined
}
function companyMatchIndicators(sales) {
  const configured = practiceCompanyStandardIndicators(sales,value=>value+'%',value=>formatMoney(minor(value)))
  return configured.length ? configured : ['待配置计提标准']
}
const syncStreams = computed(() => data.value.sources
  .filter(entry => entry.configured && (!sourceFilter.value || sourceFilter.value===entry.source))
  .flatMap(entry => options(entry.source.toLowerCase()+'OrderStream')))
const sourceLabel = key => optionLabel('source',key)?.split(' · ')[0]
const periodPullProducts = computed(() => data.value.products.filter(product=>product.practicePeriodPull &&
  (!productScope.value || product.id===productScope.value)).map(product=>({label:product.name,value:product.id})))
const titles = {product:'新增产品',mapping:'添加来源映射',periodRoster:'更正签到关联',periodPull:'同步来源期次',
  reportMonth:'设置报表月份',refundCutoff:'设置退款截止时间',rule:'配置分润规则',person:'新增人员',identity:'登记归属',pull:'重新拉取订单'}
const actions = computed(() => {
  if (current.value === 'products') return tab.value === 'mapping' ? 'mapping' : 'product'
  if (current.value === 'people') return tab.value === 'identity' ? 'identity' : 'person'
  return {periods:'periodPull',rules:'rule',orders:'pull'}[current.value]
})
const formatTime = value => value ? new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'short',timeZone:'Asia/Shanghai'}).format(new Date(value)) : '—'
const refundCutoffInput = new Intl.DateTimeFormat('sv-SE',{dateStyle:'short',timeStyle:'medium',timeZone:'Asia/Shanghai'})
const identityTimeFormat = new Intl.DateTimeFormat('zh-CN',{dateStyle:'short',timeStyle:'medium',timeZone:'Asia/Shanghai'})
const formatIdentityTime = value => value ? identityTimeFormat.format(new Date(value)) : '—'
const formatMoney = minor => minor == null ? '—' : new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(minor/100)
const practiceOpeningOptions = computed(() => {
  const productId = drawer.value==='rule' ? draft.value.productId : practiceRulesProduct.value?.id
  return data.value.practiceCountOpenings.filter(row=>row.productId===productId)
    .map(row=>({value:row.id,label:openingLabel(row)}))
})
function openingLabel(row) {
  return '冻结结算 '+row.sourceSettlementId.slice(0,8)+' · 截至 '+formatTime(row.throughAt)+' · '+row.personCount+' 人'
}
function savedOpeningLabel(id) {
  const opening = data.value.practiceCountOpenings.find(row=>row.id===id)
  return opening ? openingLabel(opening) : '冻结期初 '+id
}

const labels = {ORDER_PAID:'付款',ORDER_REFUNDED:'退款',REFUND_SUCCEEDED:'退款',REFUND_ADJUSTED:'退款更正',ORDER_COMPLETED:'已完成',DELIVERY_COMPLETED:'已交付'}
const catalogScopeLabels = {BUSINESS_SCENARIO:'业务场景',CROSS_SCENARIO_AGGREGATE:'跨场景汇总'}
const canOpenAction = computed(() => current.value==='periods'
  ? periodPullProducts.value.length > 0
  : current.value!=='rules' ||
  ruleProductOptions.value.some(product => !productScope.value || product.value===productScope.value))
function catalogRuleBadge(row) {
  const now = Date.now()
  if (!row.inLatestSnapshot) return { text:'历史版本', color:'default' }
  if (!row.projectMatches) return { text:'其他核算项目', color:'default' }
  if (row.sourceStatus!=='PUBLISHED') return { text:'已停用', color:'default' }
  if (row.retiredFrom && Date.parse(row.retiredFrom)<=now) return { text:'已停用', color:'default' }
  if (row.effectiveTo && new Date(row.effectiveTo).getTime()<=now) return { text:'已截止', color:'default' }
  if (row.paused) return { text:'暂停执行', color:'orange' }
  if (new Date(row.effectiveFrom).getTime()>now || row.resumeFrom && new Date(row.resumeFrom).getTime()>now) {
    return { text:'待生效', color:'blue' }
  }
  return row.draftSupported ? { text:'可试算', color:'green' } : { text:'未开放试算', color:'blue' }
}
function openRuleRetirement(rule) {
  retirementError.value = ''
  const local = new Date(Date.now()+8*3600000)
  let effectiveFrom = local.toISOString().slice(0,16)
  if (rule.aggregateCycle) {
    const day = rule.aggregateCycle==='MONTHLY_25_TO_24' ? 25 : 1
    const offset = day > local.getUTCDate() ? 0 : 1
    effectiveFrom = new Date(Date.UTC(local.getUTCFullYear(),local.getUTCMonth()+offset,day)).toISOString().slice(0,10)
  }
  ruleRetirement.value = {rule,effectiveFrom}
}
async function saveRuleRetirement() {
  if (retiringRule.value || !ruleRetirement.value?.effectiveFrom) return
  const {rule,effectiveFrom} = ruleRetirement.value
  retirementError.value = ''
  retiringRule.value = true
  try {
    await api('/rules/retire',{method:'POST',body:JSON.stringify({
      productId:rule.productId,
      ...(rule.catalogPending ? {catalogVersionId:rule.id} : {ruleId:rule.id}),
      effectiveFrom:new Date(effectiveFrom+(rule.aggregateCycle ? 'T00:00:00+08:00' : '+08:00')).toISOString(),
    })})
    ruleRetirement.value = undefined
    notice.value = '已登记规则停用时间。'
    refresh()
  } catch (err) {
    retirementError.value = err.message
  } finally {
    retiringRule.value = false
  }
}
function ratePublished(result, targetSku) {
  notice.value = result.id === (rateRule.value || periodRule.value)?.id ? '规则无变化。' : '规则 v'+result.version+' 已发布。'
  if (targetSku && rateRule.value) ruleBusinessScope.value = rateRule.value.productId+':sku:'+targetSku
  rateRule.value = undefined
  periodRule.value = undefined
  refresh()
}
function openCatalogScenarioCreation() {
  const [id,kind,...sku] = (ruleBusinessScope.value || '').split(':')
  const matchingProduct = catalogScenarioProducts.value.find(option=>option.value===Number(id))
  catalogScenarioCreation.value = {productId:matchingProduct?.value || productScope.value,
    sku:matchingProduct && kind==='sku' ? sku.join(':') : undefined}
}
function catalogScenarioPublished(result, product, sku) {
  catalogScenarioCreation.value = undefined
  ruleBusinessScope.value = product+':sku:'+sku
  notice.value = '规则 v'+result.version+' 已发布。'
  refresh()
}
function openCatalogPeriodCreation() {
  const [id,kind,...sku] = (ruleBusinessScope.value || '').split(':')
  const selected = catalogPeriodProducts.value.find(option=>option.value===Number(id))
  periodRule.value = {create:true,productId:selected?.value || productScope.value,sku:selected && kind==='sku' ? sku.join(':') : undefined}
}
function openMixedCatalogRule() {
  const productId = mixedCatalogProducts.value[0]?.value
  if (productId) periodRule.value = {create:true,productId,mixedArrangements:true}
}
function copyCatalogPeriodRule(source) {
  periodRule.value = {create:true,productId:source.productId,copiedFrom:source}
}
function showCatalogPeriodIssueVersion({ruleId,itemKey,month}) {
  periodRule.value = {...periodRule.value,id:ruleId,focusItemKey:itemKey,reviewMonth:month}
}
function periodRulePublished(result, product) {
  if (product) ruleBusinessScope.value = product+(periodRule.value?.mixedArrangements ? ':mixed' : ':aggregate')
  ratePublished(result)
}
function periodRuleResumed(result) {
  periodRule.value = undefined
  notice.value = '整期规则从 '+formatTime(result.resumeFrom)+' 恢复执行。'
  refresh()
}
const mainRows = computed(() => {
  const key = tab.value === 'mapping' ? 'mappings' : tab.value === 'identity' ? 'identities' : current.value
  const result = key === 'rules' ? [...data.value.rules,...data.value.catalogRules] : data.value[key]
  let rows = usesProductScope.value && productScope.value
    ? result.filter(row => (key === 'products' ? row.id : row.productId) === productScope.value)
    : result
  if (key==='rules' && ruleBusinessScope.value) {
    const [product,kind,...scope] = ruleBusinessScope.value.split(':')
    const businessKey = scope.join(':')
    rows = rows.filter(row=>row.productId===Number(product) && (
      kind==='sku' && row.scopeType==='BUSINESS_SCENARIO' && row.scopeId===businessKey ||
      kind==='mixed' && row.mixedArrangements ||
      kind==='aggregate' && !row.mixedArrangements && row.scopeType==='CROSS_SCENARIO_AGGREGATE' ||
      kind==='member' && row.classMembers?.includes(businessKey)
    ))
  }
  if (key==='identities' && identityAccount.value) {
    const {source,externalId} = identityAccount.value
    rows = rows.filter(row=>row.source===source && row.externalId===externalId)
  }
  if (['orders','periods'].includes(current.value) || !filter.value.trim()) return rows
  const q = filter.value.trim().toLowerCase()
  return rows.filter(row => Object.values(row).some(value => typeof value === 'string' && value.toLowerCase().includes(q))
    || key==='people' && String(row.id)===q)
})
const columns = computed(() => {
  const values = {
    product:[['产品名称','name'],['产品编码','code'],['核算方式','periodMode'],['业务 SKU / 报名类型','businessSkus',290],['操作','productAction',160]],
    mapping:[['产品名称','productName'],['业务来源','source'],['业务域','domain'],['来源标识','sourceKey']],
    periodPull:[['期次名称','name',160],['所属产品','productName',180],['期数','ordinal',84],['开始日期','startsOn',118],['结束日期','endsOn',118],
      ['退款截止时间','refundCutoff',158],['报表月份','reportMonth',115],['来源期次','sourcePeriodKey',148],['签到名单','practiceRoster',150],['适用规则','ruleVersion',200]],
    rule:[['规则名称','label',210],['所属产品','productName',128],['版本','version',68],
      ['适用订单','scope',188],['生效时间','effectiveFrom',165],['状态','components',112],['操作','ruleAction',240]],
    pull:[['原订单','orderNo',250],['订单来源','source',112],['业务状态','kind',104],['来源金额','amountMinor',116],['归属产品','productName',142],['业务时间','occurredAt',136],['手机号','contactPhones',195]],
    person:[['人员名称','name'],['人员 ID','id']],
    identity:[['归属人员','personName',128],['业务来源','source',112],['来源人员 ID','externalId',164],
      ['生效时间','validFrom',158],['结束时间','validTo',158],['操作','identityAction',192]],
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
function orderListParams() {
  const params = new URLSearchParams()
  if (sourceFilter.value) params.set('source',sourceFilter.value)
  if (orderQuery.value) params.set('orderId',orderQuery.value)
  if (olderThan.value) params.set('beforeId',olderThan.value)
  if (unitScope.value) params.set('unitId',unitScope.value)
  return params
}
const orderExportHref = computed(() => {
  const params = orderListParams()
  params.set('format','csv')
  return '/api/v1/orders?' + params
})
async function refresh(reloadOptions = false, periodsOnly = false) {
  if (!current.value) return
  const version = ++requestVersion
  loading.value = true
  globalError.value = ''
  const sections = {tasks:['products','periods'], settlement:['products','periods'], monthly:['products'], pools:['products','periods'], retention:[], 'sync-config':[], 'pool-config':['products'], products: ['products','mappings','sources'], periods:['products','periods'], rules:['products','periods','rules','people','practiceCountOpenings','catalogRules'], orders:['orders','sources'], organization:[], people:tab.value==='identity'?['people','identities']:[]}
  if (periodContextID && periodWorkflowPages.has(current.value)) {
    const target = periodContextID
    try {
      const [period] = await api('/periods?periodId='+target)
      if (version!==requestVersion) return
      if (period) {
        if (current.value==='rules') productScope.value = period.productId
        else if (productScope.value && productScope.value!==period.productId) productScope.value = undefined
        if (monthPeriodPages.has(current.value)) workspaceMonth.value = period.reportMonth
      }
    } catch (err) {
      if (version!==requestVersion) return
      globalError.value = err.message
    }
    periodContextID = undefined
  }
  const keys = periodsOnly ? ['periods'] : sections[current.value]
  const requests = Object.fromEntries(keys.map(key => [key, key === 'orders' ? '/orders?' + orderListParams() : '/'+key]))
  const periodPage = current.value==='periods'
  if (periodPage) {
    const search = new URLSearchParams()
    if (productScope.value) search.set('productId',productScope.value)
    if (olderThan.value) search.set('beforeId',olderThan.value)
    if (periodQuery.value) search.set('q',periodQuery.value)
    requests.periods = '/periods/page?'+search
  } else if (requests.periods && periodWorkflowPages.has(current.value)) {
    const search = new URLSearchParams()
    if (productScope.value) search.set('productId',productScope.value)
    if (monthPeriodPages.has(current.value) && workspaceMonth.value) search.set('month',workspaceMonth.value)
    requests.periods = '/periods?'+search
  }
  if (requests.identities && identityAccount.value) {
    const {source,externalId} = identityAccount.value
    requests.identities += '?'+new URLSearchParams({source,externalId})
  }
  if (requests.catalogRules) requests.catalogRules = '/rule-catalog'
  if (requests.rules) requests.rules = '/rules?view=summary'
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
      else if (key==='periods' && periodPage) {
        data.value.periods = result.value.items
        nextPeriodCursor.value = result.value.nextBeforeId
      } else {
        data.value[key] = result.value
        if (key === 'products' && productScope.value && !result.value.some(product=>product.id===productScope.value)) {
          productScope.value = undefined
        }
      }
    } else {
      if (key==='orders') data.value.orders = []
      if (key==='periods' && periodPage) {
        data.value.periods = []
        nextPeriodCursor.value = undefined
      }
      globalError.value ||= result.reason.message
    }
  })
  loading.value = false
}
function showIdentityHistory({source,externalId}) {
  identityAccount.value = {source,externalId,previousSearch:filter.value}
  filter.value = ''
  refresh()
}
function showAllIdentities() {
  filter.value = identityAccount.value?.previousSearch || ''
  identityAccount.value = undefined
  refresh()
}
function changeListTab() {
  filter.value = ''
  if (identityAccount.value) {
    identityAccount.value = undefined
    refresh()
  }
}
function refreshWorkspace() {
  refresh(true)
  periodTasks.value?.refresh()
  if (personPage.value) personDetail.value?.refresh()
  else if (current.value==='people' && peopleDesk.value) peopleDesk.value.refresh()
  else organization.value?.refresh()
  monthlyReport.value?.refresh()
  retention.value?.refresh()
  poolDirectory.value?.refresh()
  orderSync.value?.reload()
  orderSyncSettings.value?.reload()
}
function settlementLink(periodId, view, origin, rootPeriodId, fromPerson = personReportOrigin.value) {
  const params = new URLSearchParams({periodId})
  if (view) params.set('view',view)
  if (origin) params.set('from',origin)
  if (origin==='person' && fromPerson) params.set('fromPerson',fromPerson)
  if (origin==='pools') copyPoolContext(poolContext.value,params)
  if (rootPeriodId && rootPeriodId!==periodId) params.set('reviewRootPeriodId',rootPeriodId)
  return '#settlement?'+params
}
function workbenchPageLink(page, values) {
  const params = new URLSearchParams(values)
  if (current.value==='settlement' && workbenchPeriod.value) {
    params.set('returnPeriodId',workbenchPeriod.value)
    params.set('returnView',workbenchView.value)
    if (periodOrigin.value) params.set('returnFrom',periodOrigin.value)
    if (periodOrigin.value==='person') params.set('fromPerson',personReportOrigin.value)
    if (periodOrigin.value==='pools') copyPoolContext(poolContext.value,params)
    if (reviewRootPeriod.value) params.set('reviewRootPeriodId',reviewRootPeriod.value)
  }
  return '#'+page+'?'+params
}
function orderRecordsLink(order) {
  const source = order.source || ''
  if (current.value==='orders' && sourceFilter.value===source && orderQuery.value===order.orderId) return
  return workbenchPageLink('orders',source ? {source,orderId:order.orderId} : {orderId:order.orderId})
}
function relatedPeriodLink(periodId) {
  if (periodId) return settlementLink(periodId,'review',periodOrigin.value,reviewRootPeriod.value || workbenchPeriod.value)
  return workbenchPageLink('periods',selectedWorkbenchPeriod.value ? {productId:selectedWorkbenchPeriod.value.productId} : {})
}
const poolContextFields = ['pool','poolMonth','retentionMonth','retentionUnit','retentionAccount','retentionPage']
function copyPoolContext(from,to) {
  for (const field of poolContextFields) {
    const value = from.get(field)
    if (value && value.length<=500) to.set(field,value)
  }
}
function poolPageHref(id, context) {
  const params = new URLSearchParams({periodId:id})
  copyPoolContext(context,params)
  return '#pools?'+params
}
function retentionPoolLink(row, accountId, page) {
  const context = new URLSearchParams({pool:teamPoolKey(row),poolMonth:row.month,
    retentionMonth:workspaceMonth.value,retentionAccount:accountId,retentionPage:page})
  if (unitScope.value) context.set('retentionUnit',unitScope.value)
  return poolPageHref(row.periodId,context)
}
const retentionPoolReturn = computed(() => {
  const month = routeMonth(poolContext.value.get('retentionMonth'))
  if (current.value!=='pools' || !month) return
  const params = new URLSearchParams({month})
  for (const [from,to] of [['retentionUnit','unitId'],['retentionAccount','accountId'],['retentionPage','page']]) {
    const value = poolContext.value.get(from)
    if (value) params.set(to,value)
  }
  return '#retention?'+params
})
const periodOriginLink = computed(() => {
  if (periodOrigin.value==='person') return personReportOrigin.value
  if (periodOrigin.value==='pools') return poolPageHref(requestedPeriod.value,poolContext.value)
  if (periodOrigin.value==='periods' && selectedWorkbenchPeriod.value) {
    return '#periods?productId='+selectedWorkbenchPeriod.value.productId
  }
  return '#'+periodOrigin.value
})
function routeMonth(value) {
  return /^\d{4}-(0[1-9]|1[0-2])$/.test(value || '') ? value : undefined
}
function routeUnit(value) {
  return /^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i.test(value || '') ? value : undefined
}
function routeId(value) {
  const id = Number(value)
  return Number.isSafeInteger(id) && id > 0 ? id : undefined
}
function personReportFilters(params) {
  const month = params.get('month')
  const reportView = params.get('reportView')
  return {
    month:routeMonth(month),
    view:['current','settled'].includes(reportView) ? reportView : undefined,
  }
}
function personalReportHref(base, id, settings) {
  const [key,query] = base.split('?')
  const params = new URLSearchParams(query)
  params.set('personId',id)
  params.set('section','finance')
  if (settings.month) params.set('month',settings.month)
  if (settings.view) params.set('reportView',settings.view)
  return key+'?'+params
}
function readPersonReportOrigin(value) {
  const [route,query] = (value || '').split('?',2)
  const page = route?.slice(1)
  if (route!=='#organization' && route!=='#people') return
  const params = new URLSearchParams(query)
  const id = routeId(params.get('personId'))
  if (!id) return
  const tab = organizationPages.find(entry=>entry.page===page && entry.tab===params.get('tab'))?.tab
  const companyId = tab==='companies' ? routeUnit(params.get('companyId')) : undefined
  const href = organizationHref(page,tab || (page==='people' ? 'main' : 'organization'))
  return personalReportHref(href+(companyId ? '&companyId='+companyId : ''),id,personReportFilters(params))
}
function rememberPersonReport(settings) {
  if (!personPage.value) return
  const href = personalReportHref(personPage.value.back,personPage.value.id,settings)
  if (location.hash!==href) history.replaceState(history.state,'',href)
}
function personReportPeriodLink(periodId, month, view) {
  const {back,id} = personPage.value
  return settlementLink(periodId,undefined,'person',undefined,personalReportHref(back,id,{month,view}))
}
function changeRoute() {
  const previous = current.value
  const previousOrigin = periodOrigin.value
  const previousReviewRoot = reviewRootPeriod.value
  let [key, query] = location.hash.slice(1).split('?', 2)
  const params = new URLSearchParams(query)
  if (key==='organization' && params.get('tab')==='sync' && params.get('view')!=='records') {
    key = 'people'
    params.delete('tab')
    params.delete('view')
    history.replaceState(history.state,'','#people'+(params.size ? '?'+params : ''))
  }
  current.value = getRoute(key)
  if (active.value) lastWorkspacePages.value[active.value.area] = active.value.area==='data' ? 'organization' : current.value
  const previousPerson = personPage.value
  const isOrganizationPage = current.value===key && (key==='organization' || key==='people')
  const requestedTab = organizationPages.find(entry=>entry.page===key && entry.tab===params.get('tab'))?.tab
  const companyId = requestedTab==='companies' ? routeUnit(params.get('companyId')) : undefined
  companyRouteId.value = companyId
  const personId = isOrganizationPage ? routeId(params.get('personId')) : undefined
  const personBack = organizationHref(key,requestedTab || (key==='people' ? 'main' : 'organization'))
  personPage.value = personId ? {id:personId,section:params.get('section'),
    back:personBack+(companyId ? '&companyId='+companyId : ''), companyId,
    backLabel:companyId ? '返回公司成员' : undefined,
    reportFilters:personReportFilters(params)} : undefined
  if (key==='organization' && isOrganizationPage) {
    organizationPanel.value = requestedTab || 'organization'
  }
  if (personPage.value && !previousPerson) personListScroll = previous===current.value ? window.scrollY : 0
  if (personPage.value?.id!==previousPerson?.id) {
    const returnScroll = !personPage.value && previous===current.value ? personListScroll : 0
    nextTick(() => window.scrollTo(0,returnScroll))
  }
  // Keep the list and its filters mounted while navigating within a person profile.
  if (previous===current.value && isOrganizationPage &&
      (key==='organization' || (previousPerson || personPage.value) && tab.value===(requestedTab || 'main'))) return
  const productID = routeId(params.get('productId'))
  if (current.value === key && configProductPages.has(key) && productID) {
    productScope.value = productID
  }
  const periodID = routeId(params.get('periodId'))
  const isPoolPage = current.value==='pools' && key==='pools'
  const context = new URLSearchParams()
  copyPoolContext(params,context)
  poolContext.value = context
  requestedPoolPeriod.value = isPoolPage ? periodID : undefined
  if (requestedPoolPeriod.value) {
    const month = routeMonth(context.get('poolMonth'))
    if (month) workspaceMonth.value = month
    const product = data.value.periods.find(row=>row.id===periodID)?.productId
    if (product && productScope.value && productScope.value!==product) productScope.value = undefined
  }
  retentionResume.value = undefined
  if (current.value==='retention' && key==='retention' && routeMonth(params.get('month'))) {
    workspaceMonth.value = params.get('month')
    unitScope.value = routeUnit(params.get('unitId'))
    retentionResume.value = {accountId:routeUnit(params.get('accountId')),page:Math.min(routeId(params.get('page')) || 1,100000)}
  }
  requestedRulePeriod.value = key==='rules' && current.value===key ? periodID : undefined
  const practiceConfig = params.get('practiceConfig')
  requestedPracticeSettings.value = key==='rules' && current.value===key && practiceRuleTabs.some(tab=>tab.key===practiceConfig)
    ? practiceConfig : undefined
  requestedPeriod.value = key==='settlement' && current.value===key ? periodID : undefined
  const rootPeriodId = routeId(params.get('reviewRootPeriodId'))
  reviewRootPeriod.value = requestedPeriod.value && rootPeriodId!==requestedPeriod.value ? rootPeriodId : undefined
  const view = params.get('view')
  requestedView.value = requestedPeriod.value && Object.hasOwn(draftViews,view) ? view : undefined
  const origin = params.get('from')
  personReportOrigin.value = readPersonReportOrigin(params.get('fromPerson'))
  periodOrigin.value = requestedPeriod.value && Object.hasOwn(periodOrigins,origin) &&
    (origin!=='person' || personReportOrigin.value) ? origin : undefined
  monthlyResume.value = undefined
  if (periodOrigin.value==='monthly' && previous === 'monthly') {
    monthlyReturn.value = {month:workspaceMonth.value,view:monthlyView.value,dimension:monthlyDimension.value,
      product:productScope.value,unit:unitScope.value,browse:monthlyReport.value?.browseState()}
  }
  if (current.value === 'monthly' && previous === 'settlement' && previousOrigin==='monthly' && monthlyReturn.value) {
    const saved = monthlyReturn.value
    workspaceMonth.value = saved.month
    monthlyView.value = saved.view
    monthlyDimension.value = saved.dimension
    productScope.value = saved.product
    unitScope.value = saved.unit
    monthlyResume.value = saved.browse
  }
  if (current.value==='monthly') monthlyReturn.value = undefined
  if (periodOrigin.value==='tasks' && previous==='tasks') {
    tasksReturn.value = {month:workspaceMonth.value,product:productScope.value}
  }
  if (current.value==='tasks') {
    if (previous==='settlement' && previousOrigin==='tasks' && tasksReturn.value) {
      workspaceMonth.value = tasksReturn.value.month
      productScope.value = tasksReturn.value.product
    }
    tasksReturn.value = undefined
  }
  const returnFrom = params.get('returnFrom')
  const candidateOrigin = Object.hasOwn(periodOrigins,returnFrom) ? returnFrom : previous==='settlement' ? previousOrigin : undefined
  const returnOrigin = candidateOrigin==='person' && !personReportOrigin.value ? undefined : candidateOrigin
  const returnRootPeriod = rootPeriodId || (previous==='settlement' ? previousReviewRoot : undefined)
  ruleReturn.value = wholeWorkspace.value && requestedRulePeriod.value && params.get('from')==='settlement'
    ? settlementLink(requestedRulePeriod.value,undefined,returnOrigin,returnRootPeriod) : undefined
  const returnPeriod = routeId(params.get('returnPeriodId'))
  const isOrders = key === 'orders' && current.value === key
  const stream = params.get('stream')
  const fromDate = params.get('fromDate')
  orderSyncTarget.value = isOrders && stream ? {stream,fromDate} : undefined
  const returnView = params.get('returnView')
  const targetView = Object.hasOwn(draftViews,returnView) ? returnView : 'review'
  const returnLink = returnPeriod ? settlementLink(returnPeriod,targetView,returnOrigin,returnRootPeriod) : undefined
  orderReturn.value = isOrders && returnLink ? {href:returnLink,label:'返回'+draftViews[targetView]} : undefined
  configurationReturn.value = wholeWorkspace.value && current.value===key && ['rules','periods'].includes(key) && returnLink
    ? {href:returnLink,label:'返回本期'+draftViews[targetView]} : undefined
  orderDetails.value = undefined
  rateRule.value = undefined
  periodRule.value = undefined
  catalogScenarioCreation.value = undefined
  ruleDetails.value = undefined
  loadingRule.value = undefined
  ruleRetirement.value = undefined
  appliedRule.value = undefined
  rulePreview.value = undefined
  if (requestedPeriod.value && productScope.value && data.value.periods.find(period => period.id === requestedPeriod.value)?.productId !== productScope.value) {
    productScope.value = undefined
  }
  if (current.value==='rules' && session.value?.user && !session.value.user.rules.some(rule=>rule.productId===productScope.value)) {
    productScope.value=undefined
  }
  tab.value = key==='people' && isOrganizationPage ? requestedTab || 'main' : 'main'
  ruleBusinessScope.value = undefined
  identityAccount.value = undefined
  orderQuery.value = isOrders ? (params.get('orderId') || '').trim() : ''
  filter.value = orderQuery.value
  periodQuery.value = ''
  nextPeriodCursor.value = undefined
  sourceFilter.value = isOrders ? params.get('source') || '' : ''
  olderThan.value = ''
  pageStack.value = []
  notice.value = ''
  periodContextID = requestedPeriod.value || requestedPoolPeriod.value || requestedRulePeriod.value
  pulledOrderLink.value = undefined
  if (reviewRootPeriod.value || previousReviewRoot || configurationReturn.value || orderReturn.value) {
    nextTick(() => window.scrollTo(0,0))
  }
  refresh(current.value==='rules' && previous!=='rules')
}
function clearWorkspace() {
  ++requestVersion
  data.value = emptyData()
  workProductScope.value = undefined
  configProductScope.value = undefined
  unitScope.value = undefined
  scopeUnits.value = undefined
  lastWorkspacePages.value = {}
  current.value = undefined
  personPage.value = undefined
  organizationPanel.value = 'organization'
  requestedPeriod.value = undefined
  requestedView.value = undefined
  requestedRulePeriod.value = undefined
  requestedPracticeSettings.value = undefined
  ruleReturn.value = undefined
  reviewRootPeriod.value = undefined
  configurationReturn.value = undefined
  orderSyncTarget.value = undefined
  orderReturn.value = undefined
  workbenchPeriod.value = undefined
  workbenchView.value = 'detail'
  workbenchQuery.value = {}
  periodOrigin.value = undefined
  personReportOrigin.value = undefined
  requestedPoolPeriod.value = undefined
  teamPoolPeriod.value = undefined
  poolConfigProduct.value = undefined
  poolContext.value = new URLSearchParams()
  retentionResume.value = undefined
  tasksMode.value = 'pending'
  tasksReturn.value = undefined
  monthlyReturn.value = undefined
  monthlyResume.value = undefined
  orderDetails.value = undefined
  drawer.value = ''
  draft.value = {}
  rateRule.value = undefined
  periodRule.value = undefined
  catalogScenarioCreation.value = undefined
  ruleDetails.value = undefined
  loadingRule.value = undefined
  ruleRetirement.value = undefined
  appliedRule.value = undefined
  rulePreview.value = undefined
  saving.value = false
  formError.value = ''
  globalError.value = ''
  notice.value = ''
  pulledOrderLink.value = undefined
  filter.value = ''
  orderQuery.value = ''
  identityAccount.value = undefined
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
  if (drawer.value === 'rule' && next !== previous && !draft.value.copiedFrom && !draft.value.lockedPeriod) {
    draft.value.periodId = undefined
    draft.value.effectiveFrom = undefined
    draft.value.components = []
    activeRuleComponent.value = undefined
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
watch(() => {
  const saved = viewedRule.value
  const rule = saved || (drawer.value==='rule' && classRule.value ? draft.value : undefined)
  if (!rule?.components?.some(part=>part.periodBenefit?.registrationScope)) return
  const params = new URLSearchParams({productId:rule.productId})
  if (rule.periodId && (saved || rule.scope==='period')) {
    params.set('periodId',rule.periodId)
  } else if (rule.effectiveFrom) {
    const value = saved || rule.editBaseId ? rule.effectiveFrom : rule.effectiveFrom+'+08:00'
    const time = Date.parse(value)
    if (!Number.isFinite(time)) return
    params.set('effectiveFrom',new Date(time).toISOString())
  } else return
  return '/practice-registration-companies?'+params
}, async (path, _, onCleanup) => {
  registrationCompanies.value = undefined
  registrationCompaniesError.value = ''
  registrationCompaniesLoading.value = Boolean(path)
  if (!path) return
  let current = true
  onCleanup(() => { current = false })
  try {
    const companies = await api(path)
    if (current) registrationCompanies.value = companies
  } catch (err) {
    if (current) registrationCompaniesError.value = err.message
  } finally {
    if (current) registrationCompaniesLoading.value = false
  }
})
function resetOrders() {
  olderThan.value = ''
  pageStack.value = []
  const [key, search] = location.hash.slice(1).split('?', 2)
  if (key === 'orders') {
    const params = new URLSearchParams(search)
    if (orderQuery.value) params.set('orderId',orderQuery.value)
    else params.delete('orderId')
    if (sourceFilter.value) params.set('source',sourceFilter.value)
    else params.delete('source')
    const query = params.toString()
    history.replaceState(history.state,'','#orders'+(query ? '?'+query : ''))
  }
  refresh()
}
function searchOrders(value) {
  orderQuery.value = value.trim()
  filter.value = orderQuery.value
  resetOrders()
}
function selectOrganization(unit) {
  if (unitScope.value === unit) return
  unitScope.value = unit
  if (current.value === 'orders') resetOrders()
}
function selectProduct(product) {
  if (productScope.value===product) return
  productScope.value = product
  if (current.value==='periods') resetPeriods()
  else if (periodWorkflowPages.has(current.value)) refresh(false,true)
  const [key, search] = location.hash.slice(1).split('?', 2)
  if (key !== current.value || !configProductPages.has(key)) return
  const params = new URLSearchParams(search)
  if (product) params.set('productId', product)
  else params.delete('productId')
  if (key==='rules') {
    requestedRulePeriod.value = undefined
    params.delete('periodId')
  }
  const query = params.toString()
  history.replaceState(history.state, '', '#'+key+(query ? '?'+query : ''))
}
function selectWorkspaceMonth(month) {
  if (workspaceMonth.value===month) return
  workspaceMonth.value = month
  if (monthPeriodPages.has(current.value)) refresh(false,true)
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
function resetPeriods() {
  olderThan.value = ''
  pageStack.value = []
  nextPeriodCursor.value = undefined
  refresh(false,true)
}
function searchList(value) {
  if (current.value==='orders') searchOrders(value)
  if (current.value==='periods') {
    periodQuery.value = (value || '').trim()
    resetPeriods()
  }
}
function pagePeriods(forward) {
  if (forward) {
    pageStack.value.push(olderThan.value)
    olderThan.value = String(nextPeriodCursor.value)
  } else olderThan.value = pageStack.value.pop() || ''
  refresh(false,true)
}

function open(kind, row, edit = false) {
  if (!configurationReady.value) return
  formError.value = ''
  notice.value = ''
  pulledOrderLink.value = undefined
  rulePreview.value = undefined
  drawer.value = kind
  activeRuleComponent.value = 0
  draft.value = {
    periodMode:firstOption('periodMode'),source:row?.source || firstOption('source'),
    orderId:row?.orderId || '',scope:firstOption('ruleScope'),
    productId:['mapping','rule'].includes(kind) ? productScope.value : undefined,
  }
  if (kind === 'pull') {
    draft.value.source = row?.source || sourceFilter.value ||
      data.value.sources.find(source=>source.configured)?.source || firstOption('source')
    const stream = row?.domain?.includes('HEALTH') ? 'HEALTH_IDENTITY_ORDERS' : row?.domain==='VIP' ? 'VIP_ORDERS' : undefined
    draft.value.stream = options('dcaiOrderStream').find(option=>option.value===stream)?.value || firstOption('dcaiOrderStream')
    draft.value.lockedOrder = Boolean(row?.orderId)
    draft.value.sourceConfigured = row?.sourceConfigured
  }
  if (kind === 'reportMonth') draft.value = {periodId:row.id, periodName:row.name, reportMonth:row.reportMonthOverride || ''}
  if (kind === 'periodPull') draft.value = {productId:periodPullProducts.value.length===1 ? periodPullProducts.value[0].value : undefined}
  if (kind === 'refundCutoff') draft.value = {periodId:row.id, periodName:row.name,
    cutoffAt:row.refundCutoffAt ? refundCutoffInput.format(new Date(row.refundCutoffAt)).replace(' ','T') : ''}
  if (kind === 'periodRoster') {
    draft.value = {periodId:row.id, periodName:row.name, sourcePeriodKey:row.sourcePeriodKey,
      expectedSourcePeriodKey:row.sourcePeriodKey, startsOn:row.startsOn, endsOn:row.endsOn,
      expectedStartsOn:row.startsOn, expectedEndsOn:row.endsOn, reportMonthOverride:row.reportMonthOverride}
  }
  if (kind === 'identity' && row) {
    draft.value = {source:row.source,externalId:row.externalId,previousOwner:row}
  }
  if (kind === 'rule') {
    const target = ruleTargetPeriod.value
    if (row) {
      activeRuleComponent.value = undefined
      draft.value = {productId:row.productId, label:row.label, copiedFrom:row.version,
        scope:row.periodId ? 'period' : row.fromOrdinal ? 'ordinal' : 'default',
        activityType:row.activityType || 0,
        components:copiedRuleComponents(row.components,optionLabel),
      }
    } else draft.value.components = []
    if (edit) {
      draft.value.editBaseId = row.id
      draft.value.periodId = row.periodId
      draft.value.fromOrdinal = row.fromOrdinal
      draft.value.effectiveFrom = row.effectiveFrom
    } else if (target) {
      draft.value.scope = 'period'
      draft.value.periodId = target.id
      draft.value.fromOrdinal = target.ordinal
      draft.value.lockedPeriod = true
      draft.value.label = target.name+'分润规则'
    }
  }
}
function pullFromDetails(order) {
  orderDetails.value = undefined
  const references = order.refundsPayment
  const originalOrder = references?.length===1 ? references[0].orderId : ''
  open('pull', originalOrder ? {...order,orderId:originalOrder} : order)
}
async function loadRuleVersion(row, action) {
  loadingRule.value = {id:row.id,action}
  const request = loadingRule.value
  globalError.value = ''
  try {
    const rule = await api('/rules?'+new URLSearchParams({view:'detail',id:row.id}))
    if (loadingRule.value!==request) return
    if (action==='view') ruleDetails.value = rule
    else open('rule',rule,action==='edit')
  } catch (err) {
    if (loadingRule.value===request) globalError.value = err.message
  } finally {
    if (loadingRule.value===request) loadingRule.value = undefined
  }
}
function copyRuleDetails() {
  const rule = ruleDetails.value
  ruleDetails.value = undefined
  open('rule', rule)
}
function editRetrainServiceDetails(companyId) {
  const rule = ruleDetails.value
  ruleDetails.value = undefined
  open('rule',rule,true)
  activeRuleComponent.value = serviceCardKey(companyId)
}
async function showAppliedRule(selection) {
  appliedRule.value = {loading:true}
  const request = appliedRule.value
  try {
    const result = await api('/drafts/rule?'+new URLSearchParams(selection))
    if (appliedRule.value === request) appliedRule.value = {...result,fromIssue:selection.section==='issues'}
  } catch (err) {
    if (appliedRule.value === request) appliedRule.value = {error:err.message}
  }
}
function openAggregateDraft(result) {
  periodRule.value = undefined
  location.hash = settlementLink(result.periodId,'detail')
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
function compareComponentRule(periodId, signal, matchedOrders) {
  const {productName,periodName,...rule} = rulePreview.value
  return api('/rule-impact',{method:'POST',body:JSON.stringify({periodId,rule,matchedOrders}),signal})
}
function must(value, msg = '请补齐必填内容') {
  if (value === undefined || value === null || String(value).trim() === '') throw new Error(msg)
  return value
}
const minor = value => Math.round(Number(value)*100)
const periodRate = value => Math.round(Number(must(value,'请填写班次计提比例'))*10000)
function practicePoolBody(pool) {
  return {poolId:must(pool.poolId,'请选择团队池'),ownerId:pool.ownerId || undefined}
}
function initialPracticeCountStart() {
  const {scope,periodId,fromOrdinal,effectiveFrom,editBaseId,productId,activityType} = draft.value
  if (scope==='ordinal' && fromOrdinal>0) return {countFromOrdinal:fromOrdinal}
  if (scope==='period') {
    const period = data.value.periods.find(period=>period.id===periodId)
    if (period?.ordinal>0) return {countFromOrdinal:period.ordinal}
    if (period?.classStartsAt) return {countFromAt:period.classStartsAt}
  }
  if (effectiveFrom) return {countFromAt:editBaseId ? effectiveFrom : new Date(effectiveFrom+'+08:00').toISOString()}
  const ordinals = data.value.periods.filter(period=>period.productId===productId && period.ordinal>0 &&
    !period.practiceRefundMonth && (!activityType || Number(period.sourcePeriodKey?.split(':')[1])===activityType))
    .map(period=>period.ordinal)
  return {countFromOrdinal:ordinals.length ? Math.min(...ordinals) : 1}
}
function ruleComponentBody(row) {
  const signupBasis = classRule.value && row.practiceSignupBasis
  const refundPolicy = signupBasis && row.classMember==='retrain' ? undefined : row.refundPolicy
  if (row.mode==='director_rate') {
    return {type:'manager',mode:'director_rate',value:periodRate(must(row.amount,'请填写总监提成比例')),
      ...(row.classMember ? {classMember:row.classMember} : {}),
      ...(signupBasis ? {practiceSignupBasis:true} : {})}
  }
  const benefit = row.mode==='period_rate' ? row.periodBenefit : undefined
  const needsSalesRole = row.mode==='company_sales' || benefit && (benefit.recipient==='order_appointment' ||
    !benefit.registrationScope && (benefit.ownSales || benefit.salesPeople?.length || benefit.salesUnits?.length) ||
    benefit.method==='rate' && benefit.standardRates?.some(standard=>standard.salesUnits?.length))
  const sourceRole = row.sourceRole?.trim().toUpperCase() || (needsSalesRole ? 'DIRECT_REFERRER' : undefined)
  if (['sales','referral'].includes(row.type)) must(sourceRole,'请选择销售或推荐分项的订单角色。')
  const component = {type:must(row.type),mode:must(row.mode,'请选择计提方式'),value:0,
    ...(sourceRole ? {sourceRole} : {}), ...(refundPolicy && !['period_rate','company_sales'].includes(row.mode) ? {refundPolicy} : {}),
    ...(signupBasis ? {practiceSignupBasis:true} : {}),
    ...(row.classMember ? {classMember:row.classMember} : {})}
  if (classRule.value && ['sales','referral'].includes(row.type)) {
    component.label = must(row.label?.trim(),'请填写收款安排名称')
    if (row.salesRecipientKind==='person') {
      component.salesPersonId = must(row.salesPersonId,'请选择收款人')
    }
    if (row.recipientDuty) component.recipientDuty = row.recipientDuty
    if (row.salesCompanies?.length) component.salesCompanies = row.salesCompanies
    const {kind,companyExceptions,personExceptions,...scope} = row.saleScope || {}
    if (scope.ownSales || hasPracticePaymentScope(scope) || kind==='company' && component.salesCompanies) {
      component.saleScope = {...scope,...(kind ? {kind} : {})}
      if (kind==='category' && ['rate','fixed'].includes(row.mode)) {
        const standard = entry => ({mode:entry.mode,value:minor(must(entry.amount,'请填写特例的计提金额或比例。'))})
        if (companyExceptions?.length) component.saleScope.companyExceptions = companyExceptions.map(entry=>
          ({companyId:must(entry.companyId,'请选择有特例的销售公司。'),...standard(entry)}))
        if (personExceptions?.length) component.saleScope.personExceptions = personExceptions.map(entry=>
          ({personId:must(entry.personId,'请选择有特例的销售人员。'),...standard(entry)}))
      }
    }
    if ((row.restrictSaleScope || kind) && !component.salesCompanies && !component.saleScope) {
      throw new Error('请选择「'+component.label+'」的适用公司、类别或组织。')
    }
  }
  if (row.mode==='company_sales') {
    const sales = row.companySales
    must(sales.label?.trim(),'请填写收款安排名称')
    const scopeMode = sales.scopeMode || (sales.ownSales ? 'own' : sales.general ? 'all' : 'team')
    if (scopeMode==='team' && !sales.categories.length && sales.defaultRate == null) {
      throw new Error('请给「'+sales.label.trim()+'」配置销售组织或销售类别的计提标准。')
    }
    const payee = {kind:must(sales.payee?.kind,'请选择收款对象')}
    if (payee.kind==='order_person') payee.role=must(sales.payee.role,'请选择从订单读取的收款人')
    if (payee.kind==='person') {
      payee.personId=must(sales.payee.personId,'请选择收款人员')
    }
    if (practiceRecipientIsPerson(payee.kind) && row.recipientDuty) component.recipientDuty = row.recipientDuty
    if (payee.kind==='company') payee.companyId=must(sales.payee.companyId,'请选择收款公司')
    if (payee.kind==='pool') payee.pool=practicePoolBody(sales.payee.pool)
    const value = item => {
      const mode = must(item.mode,'请选择外部公司计算方式')
      return {mode,value:mode==='rate' ? periodRate(item.value) : minor(must(item.value,'请填写计提金额')),
        ...(mode==='fixed' && item.refundPolicy ? {refundPolicy:item.refundPolicy} : {}),
        ...(row.classMember==='new' && item.recommendationRate != null ? {recommendationRate:periodRate(item.recommendationRate)} : {})}
    }
    component.companySales = {label:sales.label.trim(),payee,
      ...(sales.defaultRate == null ? {} : {defaultRate:periodRate(sales.defaultRate)}),
      ...(scopeMode==='team' ? {} : {general:value(sales.general)}),
      ...(scopeMode==='own' ? {ownSales:true} : {}),
      internalCompanies:sales.internalCompanies,categories:sales.categories.map(category=>{
      const byCategory = category.scope==='category'
      const companies = byCategory ? [] : category.companies
      if (byCategory ? !category.salesCategoryIds.length : !companies.length && !category.salesUnits?.length) {
        throw new Error(byCategory ? '请选择销售类别' : '请选择指定组织')
      }
      return {
        companies,
        ...(category.label ? {label:category.label} : {}),
        ...(!byCategory && category.salesUnits?.length ? {salesUnits:category.salesUnits} : {}),
        ...(byCategory ? {salesCategoryIds:category.salesCategoryIds} : {}),
        ...value(category),
        ...(category.overrides.length ? {overrides:category.overrides.map(override=>{
          if (!override.companyIds?.length && !override.salesUnits?.length) throw new Error('请选择采用单独标准的销售组织')
          return {companyIds:override.companyIds,salesUnits:override.salesUnits,...value(override)}
        })} : {}),
      }
    })}
    if (component.companySales.categories.some(category=>category.recommendationRate>0 ||
      category.overrides?.some(override=>override.recommendationRate>0))) {
      if (!row.companySales.recommendationRecipients?.length) throw new Error('请选择可直接收取推荐分润的公司')
      component.companySales.recommendationRecipients = row.companySales.recommendationRecipients
    }
    return component
  }
  if (row.mode === 'period_rate') {
    const b = row.periodBenefit
    const registration = b.registrationScope
    const label = must(b.label?.trim(),'请填写收款安排名称')
    must(b.recipient,'请选择收款对象')
    if (!registration && b.restrictPayments && !hasPracticePaymentScope(b)) {
      throw new Error('请为「'+label+'」选择销售类别、团队、小组、销售公司或销售人员。')
    }
    const hasStandardRates = b.method==='rate' && Array.isArray(b.standardRates)
    if (hasStandardRates && !b.standardRates.length) throw new Error('请为「'+label+'」添加比例标准。')
    component.value = b.method==='rate' && !hasStandardRates ? periodRate(row.amount) : 0
    component.periodBenefit = {
      label,
      ...(b.labelIsCustom && practiceLegacyArrangementLabel(label,optionLabel)!==label ? {labelIsCustom:true} : {}),
      ...(b.recipient==='pool' ? {pool:practicePoolBody(b.pool)} : {}),
      ...(b.recipient==='unit' ? {unitId:must(b.unitId,'请选择收款组织')} : {}),
      ...(b.recipient==='person' ? {personId:must(b.personId,'请选择收款人员')} : {}),
      ...(b.recipient==='order_person' ? {orderRecipientRole:must(b.orderRecipientRole,'请选择从订单读取的收款人')} : {}),
      ...(b.recipient==='order_appointment' ? {
        orderAppointmentLevel:must(b.orderAppointmentLevel,'请选择从哪个组织取收款人'),
        orderAppointmentKind:must(b.orderAppointmentKind,'请选择收款岗位'),
      } : {}),
      ...(b.recipient==='sales_company' ? {salesCompanyRecipient:true} : {}),
      ...(b.recipient==='referral_company' ? {referralCompanyRecipient:true,
        ...(b.referralCompanyIds?.length ? {referralCompanyIds:b.referralCompanyIds} : {})} : {}),
      ...(b.ownSales && b.recipient==='person' ? {ownSales:true} : {}),
      ...(!registration && b.salesCategoryIds?.length ? {salesCategoryIds:b.salesCategoryIds} : {}),
      ...(!registration && b.salesCompanies?.length ? {salesCompanies:b.salesCompanies} : {}),
      ...(!registration && b.salesPeople?.length ? {salesPeople:b.salesPeople} : {}),
      ...(!registration && b.salesUnits?.length ? {salesUnits:b.salesUnits} : {}),
      ...(!registration && b.joinSalesOrganizations ? {joinSalesOrganizations:true} : {}),
      ...(b.recipient==='person' && b.appointmentUnitId ? {
        appointmentUnitId:b.appointmentUnitId,appointmentKind:must(b.appointmentKind,'请选择任职方式'),
      } : {}),
      ...(['fixed','class_fixed','order_fixed'].includes(b.method) ? {fixedMinor:minor(must(b.fixedAmount,'请填写固定金额'))} : {}),
      ...(b.method==='class_fixed' ? {fixedPerClass:true} : {}),
      ...(b.method==='order_fixed' ? {fixedPerOrder:true} : {}),
      ...(hasStandardRates ? {standardRates:b.standardRates.map(standard=>{
        const value = periodRate(standard.value)
        if (standard.scope==='company') {
          if (!standard.salesUnits?.length && !standard.salesCompanies?.length) throw new Error('请选择使用特例比例的销售组织。')
          return {salesUnits:standard.salesUnits,salesCompanies:standard.salesCompanies,value}
        }
        if (standard.scope==='category') {
          if (!standard.salesCategoryIds?.length) throw new Error('请选择使用默认比例的销售类别。')
          return {salesCategoryIds:standard.salesCategoryIds,value}
        }
        if (standard.scope==='external') return {externalCompanyDefault:true,value}
        return {value}
      })} : {}),
      ...(b.method==='tiers' ? {tierOnWholeClass:b.tierOnWholeClass || false,tiers:b.tiers.map(t=>({
        from:minor(must(t.from,'请填写净实收档位下限')),
        ...(t.through != null ? {through:minor(t.through)} : {}),
        value:periodRate(t.value),
      })).sort((a,b)=>a.from-b.from)} : {}),
    }
    if (hasStandardRates) {
      const standards = component.periodBenefit.standardRates
      const companies = standards.flatMap(standard=>standard.salesCompanies || [])
      const categories = standards.flatMap(standard=>standard.salesCategoryIds || [])
      const units = standards.flatMap(standard=>standard.salesUnits || [])
      if (companies.length!==new Set(companies).size || units.length!==new Set(units).size || categories.length!==new Set(categories).size) {
        throw new Error('「'+label+'」的同一销售组织或销售类别只需设置一条比例。')
      }
    }
    if (registration) {
      const sellerId = registration.sellerId?.trim()
      const sellerCompanyId = registration.sellerCompanyId
      const companies = registration.companies || []
      if ((sellerId || sellerCompanyId) && (!/^[1-9]\d*$/.test(sellerId || '') || !sellerCompanyId)) {
        throw new Error('请同时填写本人销售账号和销售公司。')
      }
      if (!sellerId && !companies.length) throw new Error('请选择本人和所属公司，或添加下属公司。')
      if (companies.includes(sellerCompanyId)) throw new Error('本人销售公司不能同时选作下属公司。')
      component.periodBenefit.registrationScope = {
        ...(sellerId ? {sellerId,sellerCompanyId} : {}),
        ...(companies.length ? {companies} : {}),
        ...(registration.exclusive ? {exclusive:true} : {}),
      }
      if (b.recipient!=='order_appointment' && !component.periodBenefit.standardRates?.some(standard=>standard.salesUnits?.length)) delete component.sourceRole
    }
    return component
  }
  if (row.mode !== 'tiered') {
    component.value = minor(must(row.amount,'请填写每个组件的比例或金额'))
    if (classRule.value && row.type==='sales' && ['fixed','rate'].includes(row.mode) && row.classMember==='retrain' && row.retrainSplits?.length) {
      must(sourceRole,'请选择销售来源角色')
      component.retrainSplits = row.retrainSplits.map(split=>practiceRetrainSplitBody(split,row.mode==='fixed' ? component.value : undefined))
      if (component.salesCompanies && component.retrainSplits.some(split=>!component.salesCompanies.includes(split.companyId))) {
        throw new Error('拆分公司须在适用销售公司范围内。')
      }
    }
    return component
  }
  const form = row.tierConfig
  const useFallback = allowsPracticeFallback(row) && form.forceFallback
  const amountMetricLabel = practiceCumulativeAmountMetrics.find(option=>option.value===form.cumulativeMetric)?.label
  must(form.metric,'请选择阶梯指标')
  const serialize = (rows, convert) => rows.map(tier => ({
    from:convert(must(tier.from,'请填写各档位下限')),
    ...(tier.through != null ? {through:convert(tier.through)} : {}),
    mode:must(tier.mode,'请选择各档位计提方式'),
    value:minor(must(tier.value,'请填写各档位计提值')),
    ...(form.includeRetrain ? {retrain:{
      mode:must(tier.retrain?.mode,'请选择各档位复训计提方式'),
      value:minor(must(tier.retrain?.value,'请填写各档位复训计提值')),
    }} : {}),
  })).sort((a,b) => a.from-b.from)
  const scheduleBody = (schedule, metric) => {
    if (form.cumulativeMetric) metric = 'performance'
    else if (useFallback) metric = 'fallback'
    else if (form.forceCount) metric = 'count'
    if (metric!=='performance' && !schedule.count.length) throw new Error('请添加至少一档累计人数标准。')
    if (metric!=='count' && !schedule.performance.length) throw new Error('请添加至少一档'+(amountMetricLabel || '本期实付')+'标准。')
    return {
      ...(metric!=='performance' ? {count:serialize(schedule.count,Number)} : {}),
      ...(metric!=='count' ? {performance:serialize(schedule.performance,minor)} : {}),
    }
  }
  component.tiers = form.onlyOverrides ? {} : scheduleBody(form,form.metric)
  if (form.includeRetrain) component.tiers.includeRetrain = true
  if (form.cumulativeMetric) component.tiers.cumulativeMetric = form.cumulativeMetric
  if (tierFormUsesHistory(form)) {
    const useOpening = form.startMode==='opening'
    const openingAllowed = allowsPracticeOpening(row)
    if (useOpening && !openingAllowed && useFallback) {
      throw new Error('请选择订单来源销售公司，或清空冻结期初。')
    }
    if (useOpening && !openingAllowed && draft.value.editBaseId) {
      throw new Error('请先在「累计起点」把该规则的冻结期初改为起始期数或开班时间。')
    }
    const configured = form.openingId || form.countFromOrdinal || form.countFromAt
    Object.assign(component.tiers,configured && (!useOpening || openingAllowed)
      ? practiceCountStartBody(form) : initialPracticeCountStart())
    if (form.personCountStarts.length) component.tiers.personCountStarts = form.personCountStarts.map(item => ({
      personId:must(item.personId,'请选择设置累计起点的人员'),
      ...practiceCountStartBody(item),
    }))
  }
  if (form.onlyOverrides && !form.categories.length && !form.companies.length && !form.people.length) throw new Error('请添加销售类别、公司或个人档位')
  if (form.categories.length) component.tiers.categorySchedules = form.categories.map(category => {
    if (!category.salesCategoryIds?.length) throw new Error('请选择销售类别')
    return {salesCategoryIds:category.salesCategoryIds,...scheduleBody(category,must(category.metric,'请选择销售类别阶梯指标'))}
  })
  if (form.companies.length) component.tiers.companySchedules = form.companies.map(company => {
    const ids = company.companyIds || []
    if (!ids.length) throw new Error('请选择销售公司')
    return {
      ...(ids.length===1 ? {companyId:ids[0]} : {companyIds:ids}),
      ...scheduleBody(company,must(company.metric,'请选择公司阶梯指标')),
    }
  })
  if (form.people.length) component.tiers.personSchedules = form.people.map(person => ({
    personId:must(person.personId,'请选择个人档位对应的销售人员'),
    ...scheduleBody(person,must(person.metric,'请选择个人阶梯指标')),
  }))
  return component
}
function bodyFor(kind,f) {
  if (kind === 'product') return {code:must(f.code),name:must(f.name),periodMode:f.periodMode,
    ...(f.periodMode==='calendar' ? {cycleStartDay:must(f.cycleStartDay)} : f.cycleStartDay!=null ? {cycleStartDay:f.cycleStartDay} : {})}
  if (kind === 'mapping') return {productId:must(f.productId),source:f.source,domain:must(f.domain),sourceKey:must(f.sourceKey)}
  if (kind === 'reportMonth') return {periodId:f.periodId,month:f.reportMonth || ''}
  if (kind === 'refundCutoff') return {periodId:f.periodId,cutoffAt:f.cutoffAt ? new Date(f.cutoffAt+'+08:00').toISOString() : null}
  if (kind === 'periodRoster') return {periodId:f.periodId,sourcePeriodKey:f.sourcePeriodKey?.trim() || '',
    expectedSourcePeriodKey:f.expectedSourcePeriodKey || '', startsOn:must(f.startsOn), endsOn:must(f.endsOn),
    expectedStartsOn:f.expectedStartsOn,expectedEndsOn:f.expectedEndsOn}
  if (kind === 'rule') {
    if (!f.components.length) throw new Error('请添加至少一项收款安排')
    const components = f.components.map(ruleComponentBody)
    checkPracticeCompanyArrangementScopes(components)
    return {productId:must(f.productId),label:must(f.label),
      ...(f.scope === 'period' ? {periodId:must(f.periodId,'请选择适用期次。')} : {}),
      ...(f.scope === 'ordinal' ? {fromOrdinal:must(f.fromOrdinal,'请填写适用的起始期数。')} : {}),
      ...(classRule.value && f.scope!=='period' && f.activityType ? {activityType:f.activityType} : {}),
      ...(f.editBaseId ? {editBaseId:f.editBaseId, effectiveFrom:f.effectiveFrom} :
        f.effectiveFrom ? {effectiveFrom:new Date(f.effectiveFrom+'+08:00').toISOString()} : {}),
      components,
    }
  }
  if (kind === 'person') return {name:must(f.name)}
  if (kind === 'identity') {
    const validFrom = new Date(must(f.validFrom)+'T'+(f.validTime || '00:00')+'+08:00').toISOString()
    return {personId:must(f.personId),source:f.source,externalId:must(f.externalId),validFrom}
  }
  return {source:f.source,stream:f.stream,orderId:must(f.orderId)}
}
function orderPullNotice(result) {
  const synced = '同步完成，读取 '+result.facts+' 条业务记录，新增 '+result.newVersions+' 个版本。'
  const check = result.refundCheck
  if (!check) return synced
  if (check.status==='PROCESSING') return synced+' 子系统退款处理中。'
  if (check.status==='NEEDS_REVIEW') return synced+' 退款详情或原付款关联待核对。'
  if (check.status==='NO_SUCCEEDED_REFUND') return synced+' 子系统本次未返回成功退款。'
  if (check.status==='FULL' || check.status==='PARTIAL') {
    const status = check.status==='FULL' ? '已全额退款' : '已部分退款'
    return synced+' '+status+'，成功退款 '+formatMoney(check.refundedMinor)+'，原实收 '+formatMoney(check.paidMinor)+'。'
  }
  return synced
}
async function save() {
  if (saving.value) return
  const issuedFor = sessionRequest
  const kind = drawer.value
  const paths = {product:'/products',mapping:'/mappings',periodRoster:'/period-practice-roster',periodPull:'/periods/pull',
    reportMonth:'/period-report-month',refundCutoff:'/period-refund-cutoff',rule:'/rules',person:'/people',identity:'/identities',pull:'/orders/pull'}
  formError.value = ''
  saving.value = true
  try {
    const payload = kind==='periodPull' ? {productId:draft.value.productId} : bodyFor(kind,draft.value)
    if (kind==='periodPull' && !payload.productId) throw new Error('请选择要同步期次的产品。')
    const result = await api(paths[kind],{method:'POST',body:JSON.stringify(payload)})
    if (issuedFor!==sessionRequest) return
    notice.value = kind === 'pull' ? orderPullNotice(result)
      : kind === 'periodPull' ? periodPullNotice(result)
      : kind === 'rule' ? '规则 v'+result.version+' 已保存。'
      : kind === 'periodRoster' ? result.changed ? '签到名单关联已更新，待重新试算。' : '签到名单关联无变化。'
      : kind === 'identity' ? '来源账号归属已保存。' : '保存成功。'
    drawer.value = ''
    rulePreview.value = undefined
    if (kind === 'pull') {
      if (current.value === 'settlement') {
        pulledOrderLink.value = orderRecordsLink(payload)
        await workbench.value?.checkRefresh()
      }
      if (current.value === 'orders') {
        sourceFilter.value = payload.source
        searchOrders(payload.orderId)
      }
    } else refresh()
  } catch (err) {
    if (issuedFor===sessionRequest) showFormError(err)
  } finally {
    if (issuedFor===sessionRequest) saving.value = false
  }
}
function periodPullNotice(result) {
  const conflicts = result.conflicts || []
  const counts = `新增 ${result.added} 期，已登记 ${result.existing} 期。`
  const skipped = result.skipped ? `来源缺少期数或日期 ${result.skipped} 期。` : ''
  const issues = conflicts.length ? `需核对 ${conflicts.length} 期（${conflicts.slice(0,3).join('、')}${conflicts.length>3?'等':''}）。` : ''
  return counts+skipped+issues
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
      <div class="path" aria-label="当前位置"><BankOutlined aria-hidden="true"/><span>财务平台</span><RightOutlined aria-hidden="true"/><b>{{ personPage ? '人员详情' : active.area==='data' ? '组织与人员' : active.title }}</b></div>
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
          <a-input id="workspace-month" :value="workspaceMonth" @update:value="selectWorkspaceMonth" type="month"/>
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
        <template v-else-if="current==='orders'">
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
        <section v-show="!personPage" class="workspace-page-head" :class="{'organization-page-head':active.area==='data'}">
          <div class="workspace-page-title">
            <h1>{{ active.area==='data' ? '组织与人员' : active.title }}</h1>
            <a v-if="reviewRootPeriod" class="workspace-return" :href="settlementLink(reviewRootPeriod,'review',periodOrigin)"><LeftOutlined aria-hidden="true"/>返回原期核对</a>
            <a v-else-if="periodOrigin" class="workspace-return" :href="periodOriginLink"><LeftOutlined aria-hidden="true"/>返回{{ periodOrigins[periodOrigin] }}</a>
            <a v-if="retentionPoolReturn" class="workspace-return" :href="retentionPoolReturn"><LeftOutlined aria-hidden="true"/>返回部门留存金</a>
            <a v-if="configurationReturn" class="workspace-return" :href="configurationReturn.href"><LeftOutlined aria-hidden="true"/>{{ configurationReturn.label }}</a>
            <a v-else-if="ruleTargetPeriod" class="workspace-return" :href="ruleReturn || '#periods?productId='+ruleTargetPeriod.productId"><LeftOutlined aria-hidden="true"/>{{ ruleReturn ? '返回本期试算' : '返回核算期次' }}</a>
            <div v-if="!practiceSettingsView && actions && (canOpenAction || catalogScenarioProducts.length || catalogPeriodProducts.length || canCreateMixedRule) && !identityAccount" class="topbar-actions">
              <a v-if="orderReturn" class="workspace-return" :href="orderReturn.href"><LeftOutlined aria-hidden="true"/>{{ orderReturn.label }}</a>
              <a-button v-if="canOpenAction && actions!=='person'" type="primary" :disabled="!configurationReady" @click="open(actions)"><template #icon><component :is="['pull','periodPull'].includes(actions) ? CloudSyncOutlined : PlusOutlined" aria-hidden="true"/></template>{{ ruleTargetPeriod ? '配置本期规则' : titles[actions] }}</a-button>
              <a-button v-if="canCreateMixedRule" :type="canOpenAction ? 'default' : 'primary'" :disabled="!configurationReady" @click="openMixedCatalogRule">新增产品规则</a-button>
              <a-button v-if="catalogScenarioProducts.length" :type="canOpenAction ? 'default' : 'primary'" :disabled="!configurationReady" @click="openCatalogScenarioCreation">新增逐单规则</a-button>
              <a-button v-if="catalogPeriodProducts.length" :type="canOpenAction || catalogScenarioProducts.length ? 'default' : 'primary'" :disabled="!configurationReady" @click="openCatalogPeriodCreation">新增整期规则</a-button>
            </div>
          </div>
          <nav v-if="active.area==='data'" class="workspace-nav organization-nav" aria-label="组织与人员导航">
            <a v-for="entry in organizationPrimaryPages" :key="entry.tab" :href="organizationHref(entry.page,entry.tab)"
              :aria-current="isActiveOrganizationPage(entry) ? 'page' : undefined">
              <component :is="entry.icon" aria-hidden="true"/>{{entry.label}}
            </a>
          </nav>
          <nav v-else-if="workspacePages.length>1" class="workspace-nav" :aria-label="activeWorkspace?.title+'导航'">
            <a v-for="entry in workspacePages" :key="entry.key" :href="'#'+entry.key" :aria-current="current===entry.key ? 'page' : undefined">{{ entry.title }}</a>
          </nav>
          <nav v-if="practiceRulesProduct" class="workspace-nav practice-rules-nav" aria-label="实战班配置">
            <a v-for="entry in practiceRuleTabs" :key="entry.key" :href="practiceRulesLink(entry.key)"
              :aria-current="(practiceSettingsView || '')===entry.key ? 'page' : undefined">{{ entry.label }}</a>
          </nav>
        </section>
        <OrganizationPersonDetail v-if="personPage" :key="personPage.id" ref="personDetail"
          :person-id="personPage.id" :section="personPage.section" :back="personPage.back" :back-label="personPage.backLabel" :person-link="personLink" :appointment-kinds="options('appointmentKind')"
          :catalog-roles="options('catalogAppointmentRole')" :catalog-scopes="options('catalogAppointmentScope')"
          :api="api" :option-label="optionLabel" :format-time="formatTime" :format-identity-time="formatIdentityTime" :format-money="formatMoney" :month="workspaceMonth"
          :report-views="options('monthlyReportView')" :report-dimensions="options('draftReportDimension')"
          :report-filters="personPage.reportFilters" :period-link="personReportPeriodLink" @report-filters="rememberPersonReport"
          :can-write="session.user ? session.user.organizationWrite : session.development"/>
        <div v-show="!personPage" class="workspace-page-body">
        <KeepAlive>
          <OrganizationDirectory v-if="current==='organization'" ref="organization" :api="api" :option-label="optionLabel" :format-time="formatTime"
            :panel="organizationPanel" :person-link="personLink" :viewing-person="!!personPage" :requested-company-id="companyRouteId"
            @update:panel="navigateOrganizationPanel" @position-changed="refresh(true)" @close-company="clearCompanyRoute"
            :can-write="session.user ? session.user.organizationWrite : session.development" :appointment-kinds="options('appointmentKind')"
            :catalog-roles="options('catalogAppointmentRole')" :catalog-scopes="options('catalogAppointmentScope')"/>
        </KeepAlive>
        <template v-if="current==='tasks'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <PeriodTasks ref="periodTasks" v-model="tasksMode" :api="api" :month="workspaceMonth" :product-id="productScope"
            :periods="calculationPeriods" :periods-loading="loading" :period-link="settlementLink"/>
        </template>
        <template v-else-if="current==='settlement'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''">
            <template v-if="pulledOrderLink" #action><a :href="pulledOrderLink">查看最新来源订单</a></template>
          </a-alert>
          <DraftWorkbench ref="workbench" v-model:period-id="workbenchPeriod" v-model:view="workbenchView" v-model:detail-query="workbenchQuery"
            :periods="calculationPeriods" :component-options="options('component')" :member-options="classMembers" :report-dimensions="options('draftReportDimension')" :source-streams="[...options('dcaiOrderStream'), ...options('dczdOrderStream')]"
            :recovery-options="options('correctionRecovery')" :settlement-products="session.user?.settlementProducts ?? []"
            :local-settlement="session.development && !session.user"
            :api="api" :format-money="formatMoney" :format-time="formatTime" :unit-id="unitScope" :requested-period="requestedPeriod"
            :rule-setup-link="classRuleSetupLink(selectedWorkbenchPeriod, true)" :issue-rule-link="issueRuleLink"
            :related-order-link="orderRecordsLink" :related-period-link="relatedPeriodLink"
            :periods-loading="loading" :refresh-workspace="refresh" :month="workspaceMonth" :requested-view="requestedView" :roster-edit-ready="configurationReady"
            @show-order="orderDetails={versionId:$event,allowPull:true}" @show-issue-order="orderDetails={versionId:$event,allowPull:true}"
            @show-roster="open('periodRoster',$event)"
            @show-applied-rule="showAppliedRule" />
        </template>
        <template v-else-if="current==='monthly'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <MonthlyReport ref="monthlyReport" :month="workspaceMonth" v-model:view="monthlyView" v-model:dimension="monthlyDimension"
            :api="api" :format-money="formatMoney" :product-id="productScope" :unit-id="unitScope" :unit-name="selectedUnit?.name"
            :resume="monthlyResume" :units="scopeUnits"
            :views="options('monthlyReportView')" :dimensions="options('draftReportDimension')" @select-unit="selectOrganization"/>
        </template>
        <template v-else-if="current==='pools'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <TeamPools v-model="teamPoolPeriod" :periods="calculationPeriods" :requested-period="requestedPoolPeriod"
            :requested-pool="poolContext.get('pool')" :loading="loading" :api="api" :format-money="formatMoney" :format-time="formatTime" :product-id="productScope"
            :can-configure="poolProducts.length>0" :period-link="id=>settlementLink(id,'detail','pools')"/>
        </template>
        <OrderSyncSettings v-else-if="current==='sync-config'" ref="orderSyncSettings" :api="api" :format-time="formatTime"/>
        <template v-else-if="current==='pool-config'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <PoolDirectory ref="poolDirectory" v-model="poolConfigProduct" :products="poolProducts" :api="api" :can-publish="canPublishRule"/>
        </template>
        <template v-else-if="current==='retention'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <DepartmentRetention ref="retention" :month="workspaceMonth" :unit-id="unitScope" :api="api"
            :units="scopeUnits" :format-money="formatMoney" :format-time="formatTime" :resume="retentionResume" :pool-link="retentionPoolLink"/>
        </template>
        <template v-else-if="current==='organization'">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>

        </template>
        <OrganizationPeopleDesk v-else-if="current==='people' && tab!=='identity'" ref="peopleDesk" :api="api"
          :person-link="personLink" :option-label="optionLabel" :can-write="session.user ? session.user.organizationWrite : session.development"/>
        <template v-else-if="practiceSettingsView">
          <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
          <PracticeRuleSettings :key="practiceRulesProduct.id"
            :product-id="practiceRulesProduct.id" :can-edit="canPublishRule(practiceRulesProduct.id)" :api="api"
            :start-modes="options('tierStartMode')" :openings="practiceOpeningOptions"
            :option-label="optionLabel" :format-time="formatTime" @saved="refresh()"/>
        </template>
        <template v-else>
        <a-alert v-if="globalError" class="feedback" type="error" :message="globalError" show-icon/>
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <a-alert v-if="ruleTargetPeriod" class="feedback" type="info" :message="'为'+ruleTargetPeriod.name+'配置规则'" show-icon/>
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
            <a-tabs v-if="current==='products'" v-model:active-key="tab" @change="changeListTab">
              <a-tab-pane key="main" tab="产品列表"/>
              <a-tab-pane key="mapping" tab="来源映射"/>
            </a-tabs>
            <h2 v-else>{{ {periods:'期次列表',rules:'规则列表',orders:'订单业务记录'}[current] }}</h2>
            <div class="search" :class="{'period-filters':current==='periods'}">
              <template v-if="current==='periods'">
                <label for="period-product-filter" class="sr-only">按产品筛选</label>
                <a-select id="period-product-filter" class="period-product-filter" :value="productScope ?? 0"
                  :options="periodProductOptions" show-search option-filter-prop="label" @change="selectProduct($event || undefined)"/>
              </template>
              <a-input-search v-model:value="filter" :placeholder="current==='orders'?'输入原订单 ID':current==='people'
                  ? (tab==='identity'?'搜索归属人员或来源账号':'搜索姓名或人员 ID') : current==='periods' ? '按期次、产品或日期搜索' : '在当前列表中搜索'"
                :aria-label="current==='orders'?'原订单 ID':'搜索列表'" :maxlength="current==='orders'?128:undefined"
                allow-clear @search="searchList"/>
              <a-button v-if="current==='orders'" :href="orderExportHref" :disabled="loading || !mainRows.length" download="finance-orders.csv">
                导出当前页
              </a-button>
            </div>
          </div>
          <div v-if="current==='rules' && ruleBusinessOptions.length" class="rule-scope-bar">
            <span id="rule-business-label">适用订单</span>
            <div v-if="ruleBusinessOptions.length<=7" class="rule-scope-buttons" role="group" aria-labelledby="rule-business-label">
              <a-button :type="!ruleBusinessScope ? 'primary' : 'default'" :aria-pressed="!ruleBusinessScope"
                @click="ruleBusinessScope=undefined">全部</a-button>
              <a-button v-for="scope in ruleBusinessOptions" :key="scope.value"
                :type="ruleBusinessScope===scope.value ? 'primary' : 'default'" :aria-pressed="ruleBusinessScope===scope.value"
                :title="scope.code" @click="ruleBusinessScope=scope.value">{{ scope.label }}</a-button>
            </div>
            <a-select v-else v-model:value="ruleBusinessScope" :options="ruleBusinessOptions" aria-labelledby="rule-business-label"
              allow-clear show-search option-filter-prop="label" placeholder="全部业务 SKU 和报名类型"/>
          </div>
          <section v-if="current==='orders' && orderQuery" class="identity-scope" aria-label="当前订单查询">
            <div>
              <span>{{ sourceFilter ? sourceLabel(sourceFilter) : '全部来源' }}</span>
              <strong>{{ orderQuery }}</strong>
            </div>
            <a-button size="small" @click="searchOrders('')">清除订单查询</a-button>
          </section>
          <section v-if="identityAccount" class="identity-scope" aria-label="来源账号">
            <div>
              <span>{{ sourceLabel(identityAccount.source) }}</span>
              <strong>{{ identityAccount.externalId }}</strong>
            </div>
            <a-button size="small" @click="showAllIdentities">返回全部映射</a-button>
          </section>
          <div class="table-container">
            <a-table :columns="columns" :data-source="mainRows" :loading="loading" row-key="id"
              :pagination="['orders','periods'].includes(current)?false:{pageSize:12,showSizeChanger:false,hideOnSinglePage:current==='people'}"
              :scroll="{x:tableWidth}">
              <template #bodyCell="{ column, record, text }">
                <template v-if="column.key==='businessSkus'">
                  <div v-if="text?.length" class="product-business-skus">
                    <span v-for="sku in text" :key="sku.code" :title="sku.code">{{ sku.label }}</span>
                  </div>
                  <span v-else-if="record.periodMode==='class'">{{ options('classMember').filter(item=>item.value).map(item=>item.label).join(' · ') }}</span>
                  <span v-else>—</span>
                </template>
                <a v-else-if="column.key==='name' && current==='periods'" :href="settlementLink(record.id,undefined,'periods')">{{ text }}</a>
                <a v-else-if="current==='people' && column.key==='name'" :href="personLink(record.id)" class="person-name-link">{{ text }}</a>
                <a v-else-if="current==='people' && column.key==='personName'" :href="personLink(record.personId,'accounts')" class="person-name-link">{{ text }}</a>
                <template v-else-if="column.key==='name'||column.key==='label'">
                  <strong class="primary-cell">{{ text }}</strong>
                </template>
                <template v-else-if="column.key==='periodMode'"><a-tag :color="text==='class'?'green':'blue'">{{ optionLabel('periodMode',text) }}</a-tag></template>
                <template v-else-if="column.key==='mappingCount'">{{ data.mappings.filter(x=>x.productId===record.id).length }}</template>
                <a-space v-else-if="column.key==='identityAction'" :size="0">
                  <a-button v-if="!identityAccount" type="link" size="small" @click="showIdentityHistory(record)">查看账号历史</a-button>
                  <a-button v-if="!record.validTo && configurationReady" type="link" size="small" @click="open('identity',record)">变更归属</a-button>
                </a-space>
                <template v-else-if="column.key==='productAction'">
                  <div class="product-links">
                    <a :href="'#periods?productId='+record.id" :aria-label="record.name+' 的核算期次'">核算期次</a>
                    <a v-if="canReadRules(record.id)" :href="'#rules?productId='+record.id" :aria-label="record.name+' 的产品规则'">产品规则</a>
                  </div>
                </template>
                <template v-else-if="column.key==='refundCutoff'">
                  <a-button v-if="!record.settled" type="link" size="small" :aria-label="'设置 '+record.name+' 的退款截止时间'"
                    @click="open('refundCutoff',record)">{{ record.refundCutoffAt ? formatTime(record.refundCutoffAt) : '设置截止时间' }}</a-button>
                  <span v-else>{{ record.refundCutoffAt ? formatTime(record.refundCutoffAt) : '默认' }}</span>
                </template>
                <template v-else-if="column.key==='reportMonth'">
                  <a-button v-if="record.reportMonthEditable" type="link" size="small" :title="'设置 '+record.name+' 的报表月份'" @click="open('reportMonth',record)">{{ text }}</a-button>
                  <span v-else>{{ text }}</span>
                </template>
                <template v-else-if="column.key==='sourcePeriodKey'">
                  <span v-if="text" class="numeric">{{ text }}</span>
                  <a-tag v-else-if="record.periodMode==='class' && !record.practiceRefundMonth" color="orange">待关联来源期次</a-tag>
                  <span v-else>—</span>
                </template>
                <template v-else-if="column.key==='practiceRoster'">
                  <template v-if="record.periodMode==='class' && !record.practiceRefundMonth">
                    <span v-if="record.practiceRosterVersion">v{{ record.practiceRosterVersion }} · {{ record.practiceRosterAttendance }} 人</span>
                    <span v-else>{{ record.sourcePeriodKey ? '待拉取' : '未关联' }}</span>
                    <small v-if="record.classStartsAt" class="order-version">{{ formatTime(record.classStartsAt) }}</small>
                    <div v-if="record.practiceRosterEditable">
                      <a-button type="link" size="small" @click="open('periodRoster',record)">{{ record.sourcePeriodKey ? '更正签到关联' : '关联签到名单' }}</a-button>
                    </div>
                  </template>
                  <span v-else>—</span>
                </template>
                <template v-else-if="column.key==='ruleVersion'">
                  <a-tag v-if="record.practiceRefundMonth" color="blue">按结算时规则</a-tag>
                  <a-tag v-else-if="record.classStartOutsidePeriod" color="orange" :title="formatTime(record.classStartsAt)">开班时间超出本期</a-tag>
                  <a-tag v-else-if="text" color="green">v{{ text }} · {{ record.ruleLabel }}</a-tag>
                  <a v-else-if="classRuleSetupLink(record)" :href="classRuleSetupLink(record)">
                    待配规则
                  </a>
                  <a-tag v-else color="orange">{{ record.ruleMatchingAvailable ? '按订单匹配' : record.periodMode==='class' && !record.classStartsAt ? '待签到名单' : '待配规则' }}</a-tag>
                </template>
                <template v-else-if="column.key==='version'"><a-tag color="green">v{{ text }}</a-tag></template>
                <template v-else-if="column.key==='effectiveFrom'">
                  {{ text ? formatTime(text) : '范围默认' }}
                  <small v-if="record.effectiveTo" class="order-version">截止 {{ formatTime(record.effectiveTo) }}</small>
                  <small v-if="record.retiredFrom" class="order-version">停用时间 {{ formatTime(record.retiredFrom) }}</small>
                </template>
                <template v-else-if="column.key==='scope'">
                  <template v-if="record.catalogPending">
                    {{ record.mixedArrangements ? '统一规则' : record.scopeType==='CROSS_SCENARIO_AGGREGATE' ? '跨 SKU 汇总' : record.scopeLabel || catalogScopeLabels[record.scopeType] || '未细分范围' }}
                  </template>
                  <template v-else>
                    {{ record.periodName || (record.fromOrdinal ? '从第 '+record.fromOrdinal+' 期起' : '产品默认') }}
                    <small v-if="record.activityType" class="order-version">{{ optionLabel('practiceActivityType',String(record.activityType)) }}</small>
                  </template>
                </template>
                <template v-else-if="column.key==='components' && record.catalogPending">
                  <a-tag :color="catalogRuleBadge(record).color">{{ catalogRuleBadge(record).text }}</a-tag>
                </template>
                <template v-else-if="column.key==='ruleAction'">
                  <a-button v-if="record.catalogPending && record.rateConfigAvailable && record.projectMatches && (!session.user || session.user.rules.some(grant=>grant.productId===record.productId))"
                    type="link" size="small" @click="rateRule=record">分润配置</a-button>
                  <a-button v-else-if="record.periodConfigAvailable && canReadRules(record.productId)" type="link" size="small"
                    @click="periodRule=record">{{ record.mixedArrangements ? '分润配置' : '整期分润' }}</a-button>
                  <template v-else-if="!record.catalogPending">
                    <a-button type="link" size="small" :loading="loadingRule?.id===record.id && loadingRule.action==='view'"
                      @click="loadRuleVersion(record,'view')">查看规则</a-button>
                    <a-button v-if="canPublishRule(record.productId) && !record.retiredFrom && ruleEditVersionIDs.has(record.id)" :disabled="!configurationReady"
                      type="link" size="small" :loading="loadingRule?.id===record.id && loadingRule.action==='edit'"
                      @click="loadRuleVersion(record,'edit')">编辑规则</a-button>
                    <a-button v-if="canPublishRule(record.productId)" :disabled="!configurationReady"
                      type="link" size="small" :loading="loadingRule?.id===record.id && loadingRule.action==='copy'"
                      @click="loadRuleVersion(record,'copy')">复制配置</a-button>
                  </template>
                  <a-button v-if="canPublishRule(record.productId) && !record.retiredFrom &&
                    (record.catalogPending ? record.retirementAvailable : ruleRetirementIDs.has(record.id))"
                    type="link" danger size="small" @click="openRuleRetirement(record)">停用规则</a-button>
                </template>
                <template v-else-if="column.key==='components'">
                  {{ record.componentCount }} 项收款安排
                  <small v-if="record.classMembers?.length" class="order-version">
                    {{ record.classMembers.map(member=>optionLabel('classMember',member)).join(' · ') }}
                  </small>
                </template>
                <template v-else-if="column.key==='source'">{{ sourceLabel(text) }}</template>
                <template v-else-if="column.key==='orderNo'"><button class="order-link" type="button" @click="orderDetails={order:record,versionId:record.id,allowPull:true}" :title="'原订单 ID '+record.orderId">{{ text || record.orderId }}</button><small class="order-version">版本 {{ record.version }}</small></template>
                <template v-else-if="column.key==='contactPhones'">
                  <template v-if="record.display?.customerPhone || record.display?.referrerPhone || record.display?.sellerPhone">
                    <small v-if="record.display.customerPhone" class="order-version">客户 {{ record.display.customerPhone }}</small>
                    <small v-if="record.display.referrerPhone" class="order-version">推荐人 {{ record.display.referrerPhone }}</small>
                    <small v-if="record.display.sellerPhone" class="order-version">销售 {{ record.display.sellerPhone }}</small>
                  </template>
                  <span v-else>—</span>
                </template>
                <template v-else-if="column.key==='kind'"><a-tag>{{ labels[text] || text }}</a-tag></template>
                <template v-else-if="column.key==='productName'&&current==='orders'"><a-tag v-if="!text" color="orange">待映射</a-tag><span v-else>{{ text }}</span></template>
                <template v-else-if="column.key==='amountMinor'"><span class="numeric">{{ formatMoney(text) }}</span></template>
                <template v-else-if="column.key==='occurredAt'">{{ formatTime(text) }}</template>
                <template v-else-if="['validFrom','validTo'].includes(column.key)">{{ formatIdentityTime(text) }}</template>
              </template>
              <template #emptyText>
                <div class="empty">
                  <a-empty :image="undefined" :description="(current==='periods' ? periodQuery || productScope : current==='orders' ? orderQuery : filter) ? '未找到匹配记录' : '暂无记录'"/>
                  <a-button v-if="current!=='periods' && !filter && !orderQuery && !identityAccount && configurationReady && canOpenAction" @click="open(actions)">{{ titles[actions] }}</a-button>
                </div>
              </template>
            </a-table>
          </div>
          <div v-if="current==='periods' && (nextPeriodCursor || pageStack.length)" class="paging">
            <a-button :disabled="!pageStack.length || loading" @click="pagePeriods(false)">上一页</a-button>
            <a-button :disabled="!nextPeriodCursor || loading" @click="pagePeriods(true)">下一页</a-button>
          </div>
          <div v-if="current==='orders' && (mainRows.length>=50 || pageStack.length)" class="paging"><a-button :disabled="!pageStack.length||loading" @click="prevOrders">上一页</a-button><a-button :disabled="mainRows.length<50||loading" @click="nextOrders">下一页</a-button></div>
        </section>
        </template>
        </div>
      </main>
    </div>
  </div>
  <a-drawer v-if="active && !sessionError && !sessionChecking && !loginRequested" :open="Boolean(drawer)"
    :root-class-name="drawer==='rule' ? 'finance-rule-editor' : undefined"
    :title="rulePreview ? '预览分润规则' : drawer==='rule' && draft.editBaseId ? '编辑分润规则'
      : drawer==='rule' && draft.copiedFrom ? '复制分润规则'
      : drawer==='periodRoster' && !draft.expectedSourcePeriodKey ? '关联签到名单'
      : drawer==='identity' && draft.previousOwner ? '变更来源账号归属' : titles[drawer]"
    :width="drawer==='rule' ? 'min(1260px, 100vw)' : 'min(510px, 100vw)'"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="close">
    <div v-if="formError" ref="formFeedback" tabindex="-1">
      <a-alert class="feedback" type="error" :message="formError" show-icon/>
    </div>
    <div v-if="rulePreview" ref="previewPane" class="rule-draft-preview" tabindex="-1" aria-label="发布前预览">
      <RuleVersionDetails :rule="rulePreview" embedded :class-product="classRule" :recipients="classRuleDirectory"
        :registration-companies="registrationCompanies"
        :option-label="optionLabel" :people="data.people"
        :format-time="formatTime" :format-money="formatMoney"/>
      <RuleImpactPreview v-model:busy="ruleImpactBusy" :periods="previewPeriods" :period-id="rulePreview.periodId"
        :simulate="compareComponentRule" :format-money="formatMoney" :format-time="formatTime" show-matched-orders/>
    </div>
    <a-form v-show="!rulePreview" id="finance-edit-form" :model="draft" layout="vertical"
      :size="drawer==='rule' ? 'small' : 'middle'" @finish="save">
      <template v-if="drawer==='product'">
        <a-form-item label="产品名称" required><a-input v-model:value="draft.name" aria-label="产品名称" :maxlength="80"/></a-form-item>
        <a-form-item label="产品编码" required><a-input v-model:value="draft.code" aria-label="产品编码" :maxlength="48"/></a-form-item>
        <a-form-item label="核算方式" html-for="product-period-mode"><a-select id="product-period-mode" v-model:value="draft.periodMode" :options="options('periodMode')"/></a-form-item>
        <a-form-item v-if="draft.periodMode==='calendar'" label="每月开始日" extra="填 1 表示自然月。填 25 表示上月 25 日至本月 24 日。" required><a-input-number v-model:value="draft.cycleStartDay" aria-label="每月开始日" :min="1" :max="28" :precision="0"/></a-form-item>
        <a-form-item v-if="draft.periodMode==='class'" label="退款账期每月开始日" :extra="firstOption('practiceRefundCalendar') ? '留空使用每月 '+firstOption('practiceRefundCalendar')+' 日开始。' : ''">
          <a-input-number v-model:value="draft.cycleStartDay" aria-label="退款账期每月开始日" :min="1" :max="28" :precision="0"/>
        </a-form-item>
      </template>
      <a-form-item v-if="drawer==='mapping'" label="所属产品" required><a-select v-model:value="draft.productId" aria-label="所属产品" placeholder="选择产品" :options="productOptions"/></a-form-item>
      <a-form-item v-if="drawer==='periodPull'" label="所属产品" html-for="practice-period-pull-product" required>
        <a-select id="practice-period-pull-product" v-model:value="draft.productId" placeholder="选择产品" :options="periodPullProducts"/>
      </a-form-item>
      <a-form-item v-if="['mapping','pull','identity'].includes(drawer)" :label="drawer==='pull' ? '订单来源' : '业务来源'">
        <a-select v-model:value="draft.source" :aria-label="drawer==='pull' ? '订单来源' : '业务来源'"
          :options="sources" :disabled="drawer==='pull' && draft.lockedOrder || drawer==='identity' && Boolean(draft.previousOwner)"/>
      </a-form-item>
      <template v-if="drawer==='mapping'">
        <a-form-item label="来源业务域" required><a-input v-model:value="draft.domain" aria-label="来源业务域" placeholder="填写业务接口返回的业务域"/></a-form-item>
        <a-form-item label="源产品标识" required><a-input v-model:value="draft.sourceKey" aria-label="源产品标识"/></a-form-item>
      </template>
      <template v-if="drawer==='periodRoster'">
        <p>{{ draft.periodName }}</p>
        <a-form-item>
          <PracticeRosterSelect v-model:value="draft.sourcePeriodKey" :api="api" :format-time="formatTime" :period-id="draft.periodId"/>
        </a-form-item>
        <div class="field-grid">
          <a-form-item label="开始日期" required><a-input v-model:value="draft.startsOn" aria-label="开始日期" type="date"/></a-form-item>
          <a-form-item label="结束日期" required><a-input v-model:value="draft.endsOn" aria-label="结束日期" type="date"/></a-form-item>
        </div>
        <p class="workbench-scope-note">已同步名单的开班日期须在期次起止日期内。保存后重新试算。</p>
        <p v-if="!draft.reportMonthOverride && draft.endsOn && draft.endsOn.slice(0,7)!==draft.expectedEndsOn.slice(0,7)"
          class="workbench-scope-note">保存后报表月份将改为 {{ draft.endsOn.slice(0,7) }}。</p>
      </template>
      <template v-if="drawer==='refundCutoff'">
        <a-form-item label="核算期次"><span>{{ draft.periodName }}</span></a-form-item>
        <a-form-item label="退款截止时间（北京时间）" html-for="refund-cutoff-at"
          extra="截止前成功的退款可补进仍未确认的本期草稿。到达截止时间后成功的退款计入后续账期。留空使用默认时间。">
          <a-input id="refund-cutoff-at" v-model:value="draft.cutoffAt" type="datetime-local" step="1"/>
        </a-form-item>
      </template>
      <template v-if="drawer==='reportMonth'">
        <p>{{ draft.periodName }}</p>
        <a-form-item label="报表月份" extra="留空按班次结束日期归月。">
          <a-input v-model:value="draft.reportMonth" aria-label="报表月份" type="month"/>
        </a-form-item>
      </template>
      <template v-if="drawer==='rule'">
        <p v-if="draft.editBaseId" class="rule-formula">当前版本 v{{ draft.copiedFrom }} · 保存后生成新版本</p>
        <a-alert v-else-if="draft.copiedFrom" class="feedback" type="info"
          :message="'已复制 v'+draft.copiedFrom+' 的收款安排，请核对适用期次。'" show-icon/>
        <div class="rule-basics">
        <a-form-item label="所属产品" required><a-select v-model:value="draft.productId" aria-label="所属产品" placeholder="选择产品" :options="ruleProductOptions" :disabled="Boolean(draft.copiedFrom || draft.lockedPeriod)"/></a-form-item>
        <a-form-item label="规则名称" required><a-input v-model:value="draft.label" aria-label="规则名称"/></a-form-item>
        <a-form-item html-for="rule-scope" label="适用范围">
          <a-select id="rule-scope" v-model:value="draft.scope" :disabled="Boolean(draft.editBaseId)"
            :options="draft.lockedPeriod ? [{value:'period',label:'仅本期'},{value:'ordinal',label:'本期及以后',disabled:!draft.fromOrdinal}] : options('ruleScope')"/>
        </a-form-item>
        <a-form-item v-if="draft.scope==='period'" html-for="rule-period" label="指定期次" required><a-select id="rule-period" v-model:value="draft.periodId" :options="periodOptions" :disabled="Boolean(draft.lockedPeriod || draft.editBaseId)"/></a-form-item>
        <a-form-item v-if="draft.scope==='ordinal'" label="起始期数" :extra="selectedProduct?.directorRulesAvailable ? '每期只用一个版本。新规则生效后，覆盖范围内的旧版本自动过期。' : '本期及后续班期自动沿用，直到新规则生效。指定单期的配置优先。'" required><a-input-number v-model:value="draft.fromOrdinal" aria-label="起始期数" :min="1" :precision="0" :disabled="Boolean(draft.editBaseId || draft.lockedPeriod)"/></a-form-item>
        <a-form-item v-if="classRule && draft.scope!=='period'" html-for="rule-activity-type" label="开班方式">
          <a-select id="rule-activity-type" :value="draft.activityType || 0" :options="practiceActivityTypes"
            :disabled="Boolean(draft.editBaseId)" @update:value="draft.activityType=$event"/>
        </a-form-item>
        <a-form-item v-if="selectedProduct" label="生效时间" :extra="draft.editBaseId ? undefined : (classRule ? '按北京时间的开班时点匹配，留空为范围默认。' : '按北京时间的付款时点匹配，留空为范围默认。')">
          <span v-if="draft.editBaseId">{{ draft.effectiveFrom ? formatTime(draft.effectiveFrom) : '范围默认' }}</span>
          <a-input v-else v-model:value="draft.effectiveFrom" aria-label="规则生效时间" type="datetime-local" step="1"/>
        </a-form-item>
        </div>
        <div v-if="selectedProduct" class="rule-blocks" aria-label="收款安排">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">收款安排</h3>
            <a-space wrap :size="6">
              <a-dropdown v-if="recipientChoices.length" :trigger="['click']">
                <a-button size="small">添加收款安排 <DownOutlined aria-hidden="true"/></a-button>
                <template #overlay>
                  <a-menu @click="({key})=>addRuleComponent(key)">
                    <a-menu-item v-for="choice in recipientChoices" :key="choice.key" :disabled="choice.disabled">
                      {{ choice.label }}<small v-if="choice.disabled">（已配置）</small>
                    </a-menu-item>
                  </a-menu>
                </template>
              </a-dropdown>
            </a-space>
          </div>
          <section v-for="(c,i) in draft.components" :key="i" class="rule-block">
          <button type="button" class="rule-block-header" :aria-expanded="activeRuleComponent===i"
            :aria-controls="'practice-rule-part-'+i" @click="activeRuleComponent=activeRuleComponent===i ? undefined : i">
            <DownOutlined aria-hidden="true"/>
            <strong>{{ ruleComponentLabel(c,optionLabel) }}</strong>
            <span v-if="classRule" class="rule-block-badge">{{ tierIncludesRetrain(c) || !c.classMember ? '新报和复训' : optionLabel('classMember',c.classMember) }}</span>
            <span v-if="c.periodBenefit?.recipient" class="rule-block-badge">
              {{ optionLabel('practicePeriodRecipient',c.periodBenefit.recipient) }}
            </span>
            <span v-if="c.periodBenefit" class="rule-block-recipient" :title="practicePeriodScopeText(c.periodBenefit,practiceScopeNames,Infinity)">
              提成范围 · {{ practicePeriodScopeText(c.periodBenefit,practiceScopeNames) }}
            </span>
            <span v-else-if="c.companySales" class="practice-company-matches" :title="ruleSalesScope(c,Infinity)">
              <span v-for="criterion in companyMatchIndicators(c.companySales)" :key="criterion" class="practice-company-match">{{ criterion }}</span>
            </span>
            <span v-else-if="ruleSalesScope(c)" class="rule-block-recipient" :title="ruleSalesScope(c,Infinity)">
              提成范围 · {{ ruleSalesScope(c) }}
            </span>
            <span v-if="c.periodBenefit?.method==='tiers' && c.periodBenefit.tierOnWholeClass" class="rule-block-badge">全班净实收选档</span>
            <small>{{ ruleSalesScope(c) ? '收款 · ' : '' }}{{ ruleRecipientSummary(c) }}</small>
          </button>
          <div v-if="i===activeRuleComponent" :id="'practice-rule-part-'+i" class="rule-block-body component-editor">
          <template v-if="c.mode==='director_rate'">
            <a-form-item :html-for="'director-member-'+i" label="计提订单类型"><a-select :id="'director-member-'+i" v-model:value="c.classMember" :options="classMembers"/></a-form-item>
            <a-form-item :html-for="'director-rate-'+i" label="总监提成比例" extra="按签到报名净实收计提，收款人和下属公司取本班生效的总监归属。">
              <a-input-number :id="'director-rate-'+i" v-model:value="c.amount" :min="0" :max="100" :precision="4" addon-after="%"/>
            </a-form-item>
          </template>
          <RuleArrangementFields v-else-if="!['company_sales','period_rate'].includes(c.mode)"
            :name-id="classRule ? 'rule-sales-label-'+i : undefined" v-model:name="c.label"
            :order-type-id="classRule ? 'rule-member-'+i : undefined" :order-types="classRule ? classMembers : undefined"
            :order-type="c.classMember" @update:order-type="changeClassMember(c,$event)">
            <template v-if="classRule && ['sales','referral'].includes(c.type)">
              <a-form-item :html-for="'rule-payee-kind-'+i" label="收款对象">
                <a-select :id="'rule-payee-kind-'+i" :value="c.salesRecipientKind || 'order_person'"
                  :options="[{value:'order_person',label:'订单上的人员'},{value:'person',label:'指定个人'}]"
                  @update:value="c.salesRecipientKind=$event; if ($event!=='person' && c.saleScope?.ownSales) c.saleScope=undefined"/>
              </a-form-item>
              <a-form-item v-if="c.salesRecipientKind==='person'" :html-for="'rule-sales-person-'+i" label="指定收款人" required>
                <a-select :id="'rule-sales-person-'+i" v-model:value="c.salesPersonId" :options="classPeopleOptions"
                  show-search option-filter-prop="label" placeholder="选择收款人"/>
              </a-form-item>
            </template>
            <a-form-item v-if="['sales','referral'].includes(c.type)" :html-for="'rule-source-role-'+i"
              :label="c.salesRecipientKind==='person' ? '订单归属角色' : '从订单取收款人'" required>
              <a-select :id="'rule-source-role-'+i" v-model:value="c.sourceRole" :options="componentSourceRoles(c.sourceRole)"
                placeholder="选择订单角色" show-search option-filter-prop="label"/>
            </a-form-item>
            <template #calculation>
              <a-form-item :html-for="'rule-mode-'+i" label="计提方式">
                <a-select :id="'rule-mode-'+i" :value="c.mode" :options="amountModes(c)" @change="updateAmountMode(c,$event)"/>
              </a-form-item>
            </template>
            <template #standards>
              <RuleCommissionStandards v-if="['fixed','rate','tiered'].includes(c.mode)">
                <template v-if="classRule && ['sales','referral'].includes(c.type)" #scope>
                  <PracticePersonalScopeFields :row="c" :index="i"
                    :companies="options('practiceSalesCompany')" :categories="options('practiceSalesCategory')" :units="salesUnitOptions"
                    :allow-source-company="c.mode==='tiered' && allowsPracticeFallback(c) && Boolean(c.tierConfig?.forceFallback)"
                    :people="classPeopleOptions"/>
                </template>
                <RuleStandardValue v-if="c.mode==='fixed' || c.mode==='rate'" :id="'rule-amount-'+i" :method="c.mode" v-model="c.amount"
                  :max="c.mode==='rate' ? 100 : 1000000000"
                  :label="c===practiceRetrainSales && c.retrainSplits?.length ?
                    (c.mode==='rate' ? '复训总提成比例（%）' : '每份复训总提成（元）') : undefined"/>
                <PracticeTierEditor v-else :config="c.tierConfig" :metrics="options('tierMetric')"
                  :amounts="baseModes" :people="classPeopleOptions" :companies="options('practiceSalesCompany')"
                  :allow-fallback="allowsPracticeFallback(c)" :allow-opening="allowsPracticeOpening(c)" :openings="practiceOpeningOptions"
                  :sales-categories="options('practiceSalesCategory')" :show-legacy-retrain="c.classMember==='new' && c.tierConfig.hasRetrainPayout"/>
              </RuleCommissionStandards>
            </template>
            <template #advanced>
              <PracticeRetrainSplitEditor v-if="c===practiceRetrainSales && c.retrainSplits?.length" :component="c" :index="i"
                :people="classPeopleOptions" :units="unitOptions" :companies="options('practiceSalesCompany')"
                :pools="practicePoolOptions" :format-money="formatMoney"/>
            </template>
          </RuleArrangementFields>
          <PracticePeriodBenefitEditor v-if="c.mode==='period_rate'" :component="c" :index="i" :people="classPeopleOptions" :units="unitOptions"
            :allow-signup-fixed="healthPracticeRule"
            :registration-companies="registrationCompanies" :registration-loading="registrationCompaniesLoading" :registration-error="registrationCompaniesError"
            :sales-categories="options('practiceSalesCategory')"
            :companies="options('practiceSalesCompany')" :recipient-types="options('practicePeriodRecipient')"
            :referral-companies="companyRecipientOptions"
            :appointment-kinds="options('appointmentKind')" :sales-units="salesUnitOptions"
            :payee-roles="componentSourceRoles(c.periodBenefit.orderRecipientRole)"
            :pools="practicePoolOptions" :member-choices="classMembers"/>
          <PracticeCompanySalesEditor v-if="c.mode==='company_sales'" :component="c" :index="i" :companies="options('practiceSalesCompany')"
            :organizations="salesUnitOptions"
            :sales-categories="options('practiceSalesCategory')" :recipients="companyRecipientOptions" :amounts="baseModes"
            :payee-roles="componentSourceRoles(c.companySales.payee.role)"
            :people="classPeopleOptions" :units="unitOptions" :pools="practicePoolOptions"
            :member-choices="classMembers" @member-change="resetAmountMode(c)"/>
          <a-form-item v-if="classRule" :html-for="'practice-signup-basis-'+i" label="计提口径"
            :extra="c.practiceSignupBasis ? (c.mode==='tiered'
              ? '累计新报人数按同一人已确认的报名统计。比例分润按净实收计算，再按定金和尾款原实付金额拆分。'
              : '比例分润按净实收计算，再按定金和尾款原实付金额拆分。适用的固定分润在部分退款时全额保留。') : undefined">
            <a-select :id="'practice-signup-basis-'+i" :value="c.practiceSignupBasis ? 'signup' : 'net'"
              :options="[{value:'net',label:'按净实收拆分'},{value:'signup',label:'按报名计提'}]"
              @update:value="c.practiceSignupBasis=$event==='signup'"/>
          </a-form-item>
          <div v-if="draft.components.length>1" class="form-actions">
            <a-button type="link" size="small" danger class="rule-remove"
              @click="draft.components.splice(i,1); activeRuleComponent=Math.min(i,draft.components.length-1)">移除此安排</a-button>
          </div>
          </div>
          </section>
          <section v-for="(split,i) in recordedServiceShares" :key="split.companyId" class="rule-block retrain-service-arrangement">
            <button type="button" class="rule-block-header" :aria-expanded="activeRuleComponent===serviceCardKey(split.companyId)"
              :aria-controls="'retrain-service-form-'+i"
              @click="activeRuleComponent=activeRuleComponent===serviceCardKey(split.companyId) ? undefined : serviceCardKey(split.companyId)">
              <DownOutlined aria-hidden="true"/>
              <strong>{{ split.serviceLabel?.trim() || '服务提成' }}</strong>
              <span class="rule-block-badge">复训</span>
              <span class="rule-block-recipient" :title="practiceRetrainServiceScopeText(split,practiceScopeNames,Infinity)">
                提成范围 · {{ practiceRetrainServiceScopeText(split,practiceScopeNames) }}
              </span>
              <small v-if="split.servicePersonId">{{ practiceScopeNames.person(split.servicePersonId) }}
                <template v-if="split.service!=null"> · {{ practiceRetrainShareText(Math.round(split.service*100),
                  split.serviceBasis==='fixed' ? undefined : {basis:split.serviceBasis,value:Math.round(split.service*10000)},formatMoney) }}
                </template>
              </small>
            </button>
            <div v-if="activeRuleComponent===serviceCardKey(split.companyId)" :id="'retrain-service-form-'+i" class="rule-block-body">
              <PracticeRetrainServiceEditor :split="split" :index="i" :people="classPeopleOptions"
                :sales-units="salesUnitOptions"
                :total="practiceRetrainSales.mode==='fixed' ? practiceRetrainSales.amount : undefined"
                :format-money="formatMoney" @remove="removeRetrainService(split)"/>
            </div>
          </section>
        </div>
      </template>
      <a-form-item v-if="drawer==='person'" label="人员名称" required><a-input v-model:value="draft.name" aria-label="人员名称"/></a-form-item>
      <template v-if="drawer==='identity'">
        <a-form-item label="来源人员 ID" required>
          <a-input v-model:value="draft.externalId" aria-label="来源人员 ID" :disabled="Boolean(draft.previousOwner)"/>
        </a-form-item>
        <a-form-item v-if="draft.previousOwner" label="上一段归属">
          <strong>{{ draft.previousOwner.personName }}</strong>
          <small class="order-version">{{ formatIdentityTime(draft.previousOwner.validFrom) }} 起</small>
        </a-form-item>
        <a-form-item label="归属人员" html-for="identity-person" required>
          <a-select id="identity-person" v-model:value="draft.personId" placeholder="搜索姓名或人员 ID"
            :options="identityPeopleOptions" option-filter-prop="label" show-search/>
        </a-form-item>
        <div class="field-grid">
          <a-form-item label="生效日期" html-for="identity-date" required>
            <a-input id="identity-date" v-model:value="draft.validFrom" type="date"/>
          </a-form-item>
          <a-form-item label="生效时刻（北京时间）" html-for="identity-time" extra="留空按当天零点">
            <a-input id="identity-time" v-model:value="draft.validTime" type="time" step="1"/>
          </a-form-item>
        </div>
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
        <a-button html-type="submit" form="finance-edit-form" type="primary" :loading="saving" :disabled="ruleImpactBusy">{{ drawer==='rule'?'保存并生效':drawer==='pull'?'开始拉取':drawer==='periodPull'?'开始同步':'保存' }}</a-button>
      </div>
    </template>
  </a-drawer>
  <a-modal v-if="ruleRetirement" open title="停用规则" ok-text="确认停用" cancel-text="返回"
    :confirm-loading="retiringRule" :ok-button-props="{danger:true,disabled:!ruleRetirement.effectiveFrom}"
    @ok="saveRuleRetirement" @cancel="!retiringRule && (ruleRetirement=undefined)">
    <a-alert v-if="retirementError" class="feedback" :message="retirementError" type="error" show-icon/>
    <p><strong>{{ ruleRetirement.rule.label }}</strong></p>
    <p class="rule-formula">{{ ruleRetirement.rule.catalogPending
      ? '该规则包的所有版本从停用时间起退出选版。'
      : '同一适用范围内的已有版本从停用时间起退出选版。' }}已确认结算继续引用原规则。</p>
    <a-form layout="vertical">
      <a-form-item :label="ruleRetirement.rule.aggregateCycle ? '停用账期起点（北京时间）' : '停用时间（北京时间）'"
        html-for="retirement-effective">
        <a-input id="retirement-effective" v-model:value="ruleRetirement.effectiveFrom"
          :type="ruleRetirement.rule.aggregateCycle ? 'date' : 'datetime-local'" :disabled="retiringRule"/>
      </a-form-item>
    </a-form>
  </a-modal>
  <OrderDetails v-if="orderDetails" :selection="orderDetails" :api="api" :format-money="formatMoney" :format-time="formatTime" :source-label="sourceLabel"
    :labels="labels" :role-label="orderRoleLabel" :can-pull="Boolean(orderDetails.allowPull && configurationReady)" :records-link="orderRecordsLink"
    @close="orderDetails=undefined" @pull="pullFromDetails"/>
  <a-drawer v-if="appliedRule?.loading || appliedRule?.error" open title="计提使用的规则" @close="appliedRule=undefined">
    <a-alert v-if="appliedRule.error" :message="appliedRule.error" type="error" show-icon/>
    <a-spin v-else tip="正在读取规则"/>
  </a-drawer>
  <CatalogRuleRates v-if="(rateRule || appliedRule?.catalog) && active && !sessionError && !sessionChecking && !loginRequested"
    :rule="rateRule" :applied="appliedRule?.catalog ? appliedRule : undefined" :api="api" :format-time="formatTime"
    :format-money="formatMoney" :option-label="optionLabel" :periods="data.periods"
    :skus="data.products.find(product=>product.id===rateRule?.productId)?.businessSkus"
    @close="rateRule=undefined; appliedRule=undefined" @published="ratePublished"/>
  <CatalogPeriodRule v-if="(periodRule || appliedRule?.aggregate) && active && !sessionError && !sessionChecking && !loginRequested"
    :key="periodRule?.create ? 'create' : periodRule?.id || 'applied'"
    :rule="periodRule" :applied="appliedRule?.aggregate" :focus-item-key="appliedRule?.component"
    :products="periodRule?.mixedArrangements ? mixedCatalogProducts : catalogPeriodProducts" :api="api" :format-time="formatTime" :format-money="formatMoney" :option-label="optionLabel"
    :can-review-activity="wholeWorkspace && !appliedRule?.aggregate"
    @close="periodRule=undefined; appliedRule=undefined" @published="periodRulePublished" @open-draft="openAggregateDraft"
    @copy="copyCatalogPeriodRule" @open-version="showCatalogPeriodIssueVersion" @resumed="periodRuleResumed"
    @show-order="orderDetails={versionId:$event}"/>
  <CatalogScenarioCreate v-if="catalogScenarioCreation && active && !sessionError && !sessionChecking && !loginRequested"
    :products="catalogScenarioProducts" :initial-product="catalogScenarioCreation.productId" :initial-sku="catalogScenarioCreation.sku" :api="api"
    :periods="data.periods" :format-money="formatMoney"
    @close="catalogScenarioCreation=undefined" @published="catalogScenarioPublished"/>
  <RuleVersionDetails v-if="viewedRule && active && !sessionError && !sessionChecking && !loginRequested"
    :rule="viewedRule" :can-copy="Boolean(ruleDetails && configurationReady && canPublishRule(viewedRule.productId))"
    :can-edit-service="Boolean(ruleDetails && configurationReady && canPublishRule(viewedRule.productId) &&
      !viewedRule.retiredFrom && ruleEditVersionIDs.has(viewedRule.id))"
    :title="appliedRule?.rule ? (appliedRule.fromIssue ? '试算使用的规则' : '计提使用的规则') : undefined"
    :component="appliedRule?.rule ? appliedRule.component : undefined"
    :show-count-starts="Boolean(appliedRule?.rule)"
    :membership-type="appliedRule?.rule ? appliedRule.membershipType : undefined"
    :recipients="appliedRule?.directory" :registration-companies="registrationCompanies"
    :class-product="data.products.find(product => product.id===viewedRule.productId)?.periodMode==='class'"
    :api="api" :people="data.people" :option-label="optionLabel" :opening-label="savedOpeningLabel"
    :format-time="formatTime" :format-money="formatMoney" @close="ruleDetails=undefined; appliedRule=undefined" @copy="copyRuleDetails"
    @edit-service="editRetrainServiceDetails"/>
</a-config-provider>
</template>

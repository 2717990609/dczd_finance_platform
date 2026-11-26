const localTime = value => value ? new Date(Date.parse(value) + 8 * 60 * 60 * 1000).toISOString().slice(0, -1) : undefined

export function rateAsBasisPoints(value) {
  const match = /^(\d{1,3})(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const rate = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return rate <= 10000 ? rate : undefined
}

export const catalogFlatMethods = [
  {value:'rate',label:'比例'},
  {value:'fixed',label:'固定金额'},
]

export const catalogMembershipTypes = [
  {value:'new',label:'仅新报'},
  {value:'all',label:'全部会员订单'},
]

export const catalogAppointmentLevels = [
  {value:'COMPANY',label:'付款销售所属公司'},
  {value:'DEPARTMENT',label:'付款销售所在部门'},
  {value:'TEAM',label:'付款销售所在小组'},
]

export function moneyAsMinor(value) {
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const minor = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return Number.isSafeInteger(minor) ? minor : undefined
}

export const catalogRateScopeTitles = {
  company:'公司特例',
  category:'销售类别默认',
  inherited:'公司补充比例',
  external:'已保存的兜底比例',
  general:'通用比例',
}
export function commissionRatePriority(scopes) {
  const labels = {...catalogRateScopeTitles,company:'组织特例',general:'通用标准'}
  return Object.entries(labels).filter(([scope])=>scopes.includes(scope)).map(([scope,label])=>({scope,label}))
}
export const catalogPercent = rateBps => (rateBps/100).toLocaleString('zh-CN',{maximumFractionDigits:2})+'%'

export const practiceCompanyMatchLabels = {
  category:'按销售类别计提',
  companies:'按组织计提',
  default:'已保存的兜底比例',
}

export const practiceTierSelectionLabels = {
  false:'本安排的净实收',
  true:'全班净实收（新报＋复训）',
}

export function catalogRateConditionScope(row) {
  if (row.scope) return row.scope
  if (row.externalCompanyDefault) return 'external'
  if (row.companyFallback) return 'inherited'
  if (row.companyIds?.length) return 'company'
  return row.categoryIds?.length ? 'category' : 'general'
}

export function catalogRateOrderScopeLabel(rows, getRate = row => row.rateBps) {
  const limited = row => row.limitsRelationship ?? ((row.relationshipCode || row.relationship || 'ANY')!=='ANY')
  const positive = rows.filter(row=>getRate(row)>0)
  const scopes = new Set(positive.map(row=>{
    const scope = catalogRateConditionScope(row)
    return scope==='inherited' ? 'company' : scope
  }))
  if (scopes.size===1) {
    if (scopes.has('company')) return '按指定公司匹配'
    if (scopes.has('category')) return '按销售类别匹配'
    if (scopes.has('external')) return '其他外部公司'
    if (positive.every(limited)) return '按销售关系匹配'
    if (rows.every(row=>catalogRateConditionScope(row)==='general' && !limited(row))) return '本规则全部订单'
  }
  if (scopes.size>1 && !scopes.has('general')) {
    return [
      scopes.has('company') && '按指定公司',
      scopes.has('category') && '按销售类别',
      scopes.has('external') && '其他外部公司用默认比例',
    ].filter(Boolean).join('、')
  }
  return '按比例条件匹配'
}

export function catalogRateIndicators(rows, getRate = row => row.rateBps) {
  const byScope = new Map()
  for (const row of rows) {
    const scope = catalogRateConditionScope(row)
    if (!byScope.has(scope)) byScope.set(scope,[])
    const rate = getRate(row)
    if (rate!=null) byScope.get(scope).push(rate)
  }
  return Object.entries(catalogRateScopeTitles).filter(([scope])=>byScope.has(scope)).map(([scope,title])=>{
    const rates = byScope.get(scope)
    if (!rates.length) return title+' · 待填写'
    const low = Math.min(...rates), high = Math.max(...rates)
    return title+' · '+(low===high ? catalogPercent(low) : catalogPercent(low)+'–'+catalogPercent(high))
  })
}

export function practiceCompanyRatePriority({companies,units,categories,hasDefault,hasGeneral}) {
  return commissionRatePriority([
    (companies.length || units.length) && 'company',
    categories.length && 'category',
    hasDefault && 'external',
    hasGeneral && 'general',
  ])
}
export const catalogRecipientIsPerson = target => ['person','order_seller','referrer','appointment'].includes(target)
export const practiceRecipientIsPerson = target => ['person','order_person','order_appointment'].includes(target)

export function catalogOrderScope(item) {
  if (item.ownSales) return 'own'
  const {salesScopeUnitIds:unitIds=[],salesScopeCategoryIds:categoryIds=[]} = item
  return unitIds.length || categoryIds.length ? {unitIds,categoryIds} : null
}

export function emptyCatalogOrderScope(scope) {
  return scope && scope!=='own' && !scope.unitIds.length && !scope.categoryIds.length
}

export function catalogScopeOptions(options, selected, names, unknown) {
  const active = new Set(options.map(option=>option.value))
  return [...options,...(selected || []).filter(id=>!active.has(id)).map(id=>({value:id,label:names?.[id] || unknown}))]
}

export function checkPracticeCompanyArrangementScopes(components) {
  const arrangements = new Map()
  function register(member, scope, ids, arrangement) {
    for (const id of ids) {
      const key = JSON.stringify([member,scope,id])
      const previous = arrangements.get(key)
      if (previous && previous!==arrangement) {
        throw new Error('「'+previous.label+'」与「'+arrangement.label+'」的'+scope+'重复，请调整适用范围。')
      }
      arrangements.set(key,arrangement)
    }
  }
  for (const {classMember,companySales:arrangement} of components) {
    if (!arrangement) continue
    for (const member of classMember ? [classMember] : ['new','retrain']) {
      if (arrangement.defaultRate != null) {
        register(member,'既有兜底比例',['default'],arrangement)
      }
      if (arrangement.general) register(member,'通用标准',['general'],arrangement)
      for (const standard of arrangement.categories) {
        register(member,'销售类别',standard.salesCategoryIds || [],arrangement)
        register(member,'指定组织',standard.salesUnits || [],arrangement)
        register(member,'指定公司',standard.companies,arrangement)
        for (const override of standard.overrides || []) {
          register(member,'指定组织',override.salesUnits || [],arrangement)
          register(member,'指定公司',override.companyIds || [],arrangement)
        }
      }
    }
  }
}

export function catalogRuleImpactPeriods(periods, productId, effectiveFrom) {
  const effective = Date.parse(effectiveFrom)
  return (periods || []).filter(period=>period.productId===productId && period.periodMode==='calendar' &&
    !period.settled && !period.aggregatePackageId &&
    effective < Date.parse(period.endsOn+'T00:00:00+08:00') + 86400000)
}

export function newCatalogRateCondition(options, scope = options.categories.length ? 'category' : 'general') {
  return {scope, companyIds:[], categoryIds:[],
    relationship:options.relationships.find(option=>option.value==='ANY')?.value || options.relationships[0]?.value}
}

export function catalogRateConditionBody(row, scope = row.scope, salesPayee = false) {
  const rateBps = rateAsBasisPoints(row.rate)
  if (rateBps===undefined) throw new Error('请填写有效比例。')
  if (scope==='external') {
    return {company:'ANY',relationship:'ANY',rateBps,externalCompanyDefault:true,...(salesPayee ? {payee:'sales_company'} : {})}
  }
  if (!row.relationship) throw new Error('请选择销售关系。')
  if (scope==='category' && !row.categoryIds.length) throw new Error('请选择销售类别。')
  if (['company','inherited'].includes(scope) && !row.companyIds.length) throw new Error('请选择特例公司。')
  return {relationship:row.relationship,rateBps,
    ...(scope==='inherited' ? {companyFallback:true} : {}),
    ...(salesPayee && row.payee ? {payee:row.payee} : {}),
    ...(scope!=='category' && scope!=='general' && row.companyIds?.length ? {companyIds:row.companyIds} : {company:'ANY'}),
    ...(scope!=='general' && row.categoryIds?.length ? {categoryIds:row.categoryIds} : {})}
}

export const catalogSalesPayees = [
  {value:'seller',label:'销售本人'},
  {value:'sales_company',label:'销售所属公司'},
]

export const catalogOrderSalesPayees = {
  order_seller:'订单上的成交销售',
  order_sales_company:'销售所属公司',
}

export function catalogPayeeComplete(payee, roles) {
  switch (payee.target) {
    case 'seller':
    case 'order_seller':
    case 'order_sales_company':
    case 'referrer': return true
    case 'person': {
      const role = roles.find(option=>option.value===payee.role)
      return !!(payee.personId && (!role?.organization || role.personal || payee.appointmentUnitId))
    }
    case 'appointment': return roles.some(option=>option.value===payee.role && option.organization) &&
      catalogAppointmentLevels.some(option=>option.value===payee.appointmentLevel)
    case 'company': return !!payee.organization
    case 'pool': return !!((payee.poolId || payee.organization && (payee.role || payee.ownerPersonId)) && payee.memberRole)
    default: return false
  }
}

export function ruleComponentLabel(component, optionLabel) {
  if (component.mode==='director_rate') return '按班期归属的总监提成'
  if (component.label?.trim()) return component.label.trim()
  if (component.salesLabel?.trim()) return component.salesLabel.trim()
  if (component.companySales?.label != null) return component.companySales.label.trim() || '未命名收款安排'
  if (component.periodBenefit) {
    const label = component.periodBenefit.label?.trim()
    if (!label) return '未命名收款安排'
    return component.periodBenefit.labelIsCustom ? label : practiceLegacyArrangementLabel(label,optionLabel)
  }
  return optionLabel('component',component.type) || '分润分项'
}

export function practiceLegacyArrangementLabel(label, optionLabel) {
  return label==='SERVICE' ? '服务' : optionLabel('catalogAppointmentRole',label) || label
}

export const hasPracticePaymentScope = benefit =>
  !!(benefit.salesCategoryIds?.length || benefit.salesCompanies?.length || benefit.salesPeople?.length || benefit.salesUnits?.length)

export function practiceStandardRateScope(row) {
  return row.scope || (row.externalCompanyDefault ? 'external' :
    row.salesCompanies?.length || row.salesUnits?.length ? 'company' : row.salesCategoryIds?.length ? 'category' : 'general')
}

export function practiceStandardRateGroups(rows = []) {
  const indexed = rows.map((row,index)=>({row,index}))
  return Object.entries({company:'指定组织',category:'销售类别默认',external:'已保存的兜底比例',general:'通用比例'}).map(([scope,label])=>({
    scope,label,items:indexed.filter(({row})=>practiceStandardRateScope(row)===scope),
  }))
}

export function practiceStandardRateSummary(rows, format) {
  return practiceStandardRateGroups(rows).filter(group=>group.items.length).map(({label,items})=>{
    const values = items.map(({row})=>row.value).filter(Number.isFinite)
    if (!values.length) return label+' 待填写'
    const min = Math.min(...values), max = Math.max(...values)
    return label+' '+(min===max ? format(min) : format(min)+'–'+format(max))
  }).join(' · ') || '待填写比例'
}

export function practicePeriodScopeText(benefit, names, limit = 2) {
  const entries = []
  const list = (label, ids, name) => {
    if (!ids?.length) return
    const visible = ids.slice(0,limit).map(name).join('、')
    entries.push(label+' '+visible+(ids.length>limit ? ' 等 '+ids.length+' 个' : ''))
  }
  const registration = benefit.registrationScope
  if (registration) {
    entries.push('签到报名')
    if (registration.sellerId) {
      const company = registration.sellerCompanyId ? '（'+names.registrationCompany(registration.sellerCompanyId)+'）' : ''
      entries.push('本人报名'+company)
    }
    list('下属公司',registration.companies,names.registrationCompany)
    if (entries.length===1) entries.push('待选择报名归属')
  } else if (benefit.ownSales) {
    entries.push('本人销售')
  } else {
    list('指定组织',benefit.salesUnits,names.unit)
    list('销售类别',benefit.salesCategoryIds,names.category)
    list('销售公司',benefit.salesCompanies,names.company)
    list('销售人员',benefit.salesPeople,names.person)
    if (!entries.length && benefit.restrictPayments) entries.push('待选择销售范围')
  }
  const standards = benefit.method==null || benefit.method==='rate' ? benefit.standardRates : undefined
  if (standards?.length && !standards.some(row=>practiceStandardRateScope(row)==='general' && row.value>0)) {
    const active = standards.filter(row=>row.value>0)
    const ids = field => [...new Set(active.flatMap(row=>row[field] || []))]
    list('指定组织',ids('salesUnits'),names.unit)
    list('销售公司',ids('salesCompanies'),names.company)
    list('销售类别',ids('salesCategoryIds'),names.category)
    if (active.some(row=>practiceStandardRateScope(row)==='external')) entries.push('其他匹配公司')
    if (!active.length) entries.push('未设置有效比例')
    else if (!entries.length) entries.push('待选择销售范围')
  }
  if (!entries.length) entries.push('全班')
  if (benefit.appointmentUnitId && (benefit.recipient == null || benefit.recipient==='person')) {
    entries.push(names.unit(benefit.appointmentUnitId)+'任职期间')
  }
  return entries.join(' · ')
}

function tierRows(rows, scale) {
  return (rows || []).map(row => ({
    ...row, from:row.from / scale, through:row.through == null ? undefined : row.through / scale, value:row.value / 100,
    retrain:row.retrain ? {...row.retrain,value:row.retrain.value / 100} : undefined,
  }))
}

export function practiceCountStartForm(start) {
  return {
    startMode:start.openingId ? 'opening' : start.countFromOrdinal ? 'ordinal' : 'time',
    openingId:start.openingId, countFromOrdinal:start.countFromOrdinal, countFromAt:localTime(start.countFromAt),
  }
}

export function practiceCountStartBody(start) {
  if (start.startMode==='opening') {
    if (!Number.isInteger(start.openingId) || start.openingId<1) throw new Error('请选择冻结期初')
    return {openingId:start.openingId}
  }
  if (start.startMode==='ordinal') {
    if (!Number.isInteger(start.countFromOrdinal) || start.countFromOrdinal<1) throw new Error('请填写起始期数')
    return {countFromOrdinal:start.countFromOrdinal}
  }
  const at = start.startMode==='time' && Date.parse(start.countFromAt+'+08:00')
  if (at===false || !Number.isFinite(at)) throw new Error('请填写起始开班时间')
  return {countFromAt:new Date(at).toISOString()}
}

function tierMetric(schedule) {
  return schedule.count?.length ? (schedule.performance?.length ? 'fallback' : 'count') : 'performance'
}

export const practiceCumulativeAmountMetrics = [
  {value:'cumulative_new',label:'累计新报金额'},
  {value:'cumulative_retrain',label:'累计复训金额'},
  {value:'cumulative_combined',label:'累计新报＋复训金额'},
]

export function tierFormUsesHistory(form) {
  return !!form.cumulativeMetric || form.forceCount || form.forceFallback || !form.onlyOverrides && form.metric!=='performance' ||
    form.companies.some(row=>row.metric!=='performance') ||
    form.categories.some(row=>row.metric!=='performance') ||
    form.people.some(row=>row.metric!=='performance')
}

function tierForm(tiers) {
  const schedules = tiers.companySchedules || []
  const pricedSchedules = [tiers,...schedules,...(tiers.categorySchedules || []),...(tiers.personSchedules || [])]
    .filter(schedule=>schedule.count?.length || schedule.performance?.length)
  const hasLegacyMetrics = !tiers.cumulativeMetric && pricedSchedules.some(schedule=>schedule.performance?.length)
  return {
    ...practiceCountStartForm(tiers),
    includeRetrain:Boolean(tiers.includeRetrain),
    hasRetrainPayout:Boolean(tiers.includeRetrain),
    cumulativeMetric:tiers.cumulativeMetric,
    hasLegacyMetrics,
    forceFallback:hasLegacyMetrics && pricedSchedules.every(schedule=>tierMetric(schedule)==='fallback'),
    forceCount:!tiers.cumulativeMetric && !hasLegacyMetrics,
    metric:tierMetric(tiers),
    onlyOverrides:!tiers.count?.length && !tiers.performance?.length,
    count:tierRows(tiers.count,1), performance:tierRows(tiers.performance,100),
    personCountStarts:(tiers.personCountStarts || []).map(start => ({personId:start.personId, ...practiceCountStartForm(start)})),
    companies:schedules.map(schedule => ({
      companyIds:[...exceptionCompanyIDs(schedule)], metric:tierMetric(schedule),
      count:tierRows(schedule.count,1), performance:tierRows(schedule.performance,100),
    })),
    categories:(tiers.categorySchedules || []).map(schedule => ({
      salesCategoryIds:[...standardCategoryIDs(schedule)], metric:tierMetric(schedule),
      count:tierRows(schedule.count,1), performance:tierRows(schedule.performance,100),
    })),
    people:(tiers.personSchedules || []).map(schedule => ({
      personId:schedule.personId, metric:tierMetric(schedule),
      count:tierRows(schedule.count,1), performance:tierRows(schedule.performance,100),
    })),
  }
}

function companyValue(row) {
  return {...row, value:row.value / (row.mode === 'rate' ? 10000 : 100),
    recommendationRate:row.recommendationRate == null ? undefined : row.recommendationRate / 10000}
}

export const standardCategoryIDs = row => row.salesCategoryIds || (row.salesCategoryId ? [row.salesCategoryId] : [])
export const exceptionCompanyIDs = row => row.companyIds || (row.companyId ? [row.companyId] : [])

export function practiceCompanyStandardIndicators(sales, formatRate, formatAmount) {
  const categories = [], companies = []
  for (const row of sales.categories || []) {
    const overrides = row.overrides || []
    if (standardCategoryIDs(row).length) {
      categories.push(row)
    } else {
      const overridden = new Set(overrides.flatMap(exceptionCompanyIDs))
      const overriddenUnits = new Set(overrides.flatMap(row=>row.salesUnits || []))
      if (row.companies?.some(id=>!overridden.has(id)) || row.salesUnits?.some(id=>!overriddenUnits.has(id))) companies.push(row)
    }
    companies.push(...overrides.filter(row=>exceptionCompanyIDs(row).length || row.salesUnits?.length))
  }
  const summary = rows => ['rate','fixed'].map(mode => {
    const values = rows.filter(row=>row.mode===mode && Number.isFinite(row.value)).map(row=>row.value)
    if (!values.length) return ''
    const format = mode==='rate' ? formatRate : formatAmount
    const minimum = Math.min(...values), maximum = Math.max(...values)
    return minimum===maximum ? format(minimum) : format(minimum)+'–'+format(maximum)
  }).filter(Boolean).join(' / ')
  const scopes = practiceCompanyScopes(sales)
  const orgCount = scopes.companies.length + scopes.units.length
  const only = [orgCount>0,scopes.categories.length>0,scopes.hasDefault,scopes.hasGeneral].filter(Boolean).length===1 ? '仅' : ''
  const withStandard = (scope, rows) => [scope,summary(rows)].filter(Boolean).join(' · ')
  return [
    orgCount && withStandard(only+'按组织 · '+orgCount+' 个',companies),
    scopes.categories.length && withStandard(only+'按销售类别 · '+scopes.categories.length+' 类',categories),
    scopes.hasDefault && '既有兜底比例 · '+formatRate(sales.defaultRate),
    scopes.hasGeneral && withStandard('通用标准',[sales.general]),
  ].filter(Boolean)
}

export function practiceCompanyScopes(sales = {}) {
  const rows = sales.categories || []
  return {
    companies:[...new Set(rows.flatMap(row=>[...(row.companies || []),...(row.overrides || []).flatMap(exceptionCompanyIDs)]))],
    units:[...new Set(rows.flatMap(row=>[...(row.salesUnits || []),...(row.overrides || []).flatMap(x=>x.salesUnits || [])]))],
    categories:[...new Set(rows.flatMap(standardCategoryIDs))],
    hasDefault:Number.isFinite(sales.defaultRate),
    hasGeneral:sales.scopeMode!=='team' && sales.general?.value!=null,
  }
}

export function practiceCompanyCalculationText(sales) {
  const modes = new Set((sales.categories || []).flatMap(row=>[row,...(row.overrides || [])]).map(row=>row.mode))
  if (Number.isFinite(sales.defaultRate)) modes.add('rate')
  if (sales.scopeMode!=='team' && sales.general?.mode) modes.add(sales.general.mode)
  return [modes.has('rate') && '比例',modes.has('fixed') && '固定金额'].filter(Boolean).join(' / ') || '在计提标准中设置'
}

export function practiceCompanyMatchText({companies,units,categories,hasDefault}) {
  const orgCount = companies.length + units.length
  const scopes = [
    orgCount && `按组织（${orgCount} 个）`,
    categories.length && `按销售类别（${categories.length} 类）`,
    hasDefault && '既有兜底比例',
  ].filter(Boolean)
  if (scopes.length>1) return scopes.join('、')
  if (orgCount) return `仅按组织计提（${orgCount} 个）`
  if (categories.length) return `仅按销售类别计提（${categories.length} 类）`
  return hasDefault ? '按已保存的兜底比例计提' : '待配置计提标准'
}

export function practiceSalesScopeText(part, names, limit = 2) {
  const entries = []
  const list = (label, ids, name) => {
    const values = [...new Set(ids)]
    if (values.length) entries.push(label+' '+values.slice(0,limit).map(name).join('、')+
      (values.length>limit ? ' 等 '+values.length+' 个' : ''))
  }
  if (part.companySales) {
    const scopes = practiceCompanyScopes(part.companySales)
    if (part.companySales.ownSales || part.companySales.scopeMode==='own') return '本人销售'
    if (scopes.hasGeneral || part.companySales.scopeMode==='all') return '全班'
    if (!limit) return practiceCompanyMatchText(scopes)
    const {companies,units,categories,hasDefault} = scopes
    list('按组织',companies.map(names.company).concat(units.map(names.unit)),name=>name)
    list('按销售类别',categories,names.category)
    if (hasDefault) entries.push('既有兜底比例')
    return entries.join(' · ') || '待配置计提标准'
  }
  if (part.saleScope?.ownSales) {
    list('销售公司',part.salesCompanies || [],names.company)
    return ['本人销售',...entries].join(' · ')
  }
  if (hasPracticePaymentScope(part.saleScope || {}) || part.restrictSaleScope) {
    return practicePeriodScopeText({...part.saleScope,salesCompanies:part.salesCompanies,restrictPayments:part.restrictSaleScope},names,limit)
  }
  if (part.type==='referral' && !part.salesCompanies?.length) return '全班适用报名'
  if (!['sales','referral'].includes(part.type)) return
  if (part.salesCompanies?.length) {
    list('销售公司',part.salesCompanies,names.company)
    return entries.join(' · ')
  }
  const tiers = part.mode==='tiered' && (part.tierConfig || part.tiers)
  if (tiers && (part.tierConfig?.onlyOverrides || !part.tierConfig && !tiers.count?.length && !tiers.performance?.length)) {
    list('销售类别',(tiers.categories || tiers.categorySchedules || []).flatMap(standardCategoryIDs),names.category)
    list('销售公司',(tiers.companies || tiers.companySchedules || []).flatMap(exceptionCompanyIDs),names.company)
    list('销售人员',(tiers.people || tiers.personSchedules || []).map(row=>row.personId).filter(Boolean),names.person)
    return entries.join(' · ') || '待配置销售档位'
  }
  if (part.classMember==='retrain' && ['fixed','rate'].includes(part.mode) && part.retrainSplits?.length) {
    list('已配置公司分账',part.retrainSplits.map(split=>split.companyId).filter(Boolean),names.company)
  }
  return ['本班符合条件的个人销售报名',...entries].join(' · ')
}

export function practiceRetrainSplitForm(split) {
  return {
    companyId:split.companyId,pool:{...split.pool},
    salesBasis:split.salesRate?.basis || 'fixed',
    personal:split.salesRate ? split.salesRate.value/10000 : split.personalMinor/100,
    serviceEnabled:Boolean(split.servicePersonId),
    serviceLabel:split.serviceLabel || '',
    serviceScopeMode:split.serviceSalesPeople?.length || split.serviceSalesUnits?.length ? 'selected' : 'all',
    serviceSalesPeople:split.serviceSalesPeople || [],serviceSalesUnits:split.serviceSalesUnits || [],
    servicePersonId:split.servicePersonId,serviceBasis:split.serviceRate?.basis || 'fixed',
    service:split.serviceRate ? split.serviceRate.value/10000 : (split.serviceMinor || 0)/100,
  }
}

export const practiceRetrainShareMethods = [
  {value:'commission',label:'按本份复训总提成比例'},
  {value:'net_receipts',label:'按本份报名净实收比例'},
  {value:'fixed',label:'每份报名固定金额'},
]

export function practiceRetrainSplitAmounts(split) {
  function share(basis, amount, label) {
    if (basis==='fixed') {
      const fixed = moneyAsMinor(amount)
      if (fixed==null) throw new Error('请填写'+label+'固定金额')
      return {fixed}
    }
    if (!practiceRetrainShareMethods.some(method=>method.value===basis)) throw new Error('请选择'+label+'的计提方式')
    const input = String(amount ?? '')
    if (!/^\d+(?:\.\d{1,4})?$/.test(input) || Number(input)>100) {
      throw new Error('请填写 0% 到 100% 之间的'+label+'比例')
    }
    return {rate:{basis,value:Math.round(Number(input)*10000)}}
  }
  const sales = share(split.salesBasis,split.personal,'销售份额')
  if (split.serviceEnabled && !split.servicePersonId) throw new Error('请选择服务收款人')
  const service = split.servicePersonId ? share(split.serviceBasis,split.service,'服务份额') : {}
  return {
    personalMinor:sales.fixed ?? 0,...(sales.rate ? {salesRate:sales.rate} : {}),
    ...(split.servicePersonId ? {servicePersonId:split.servicePersonId,...(service.rate ? {serviceRate:service.rate} : {serviceMinor:service.fixed})} : {}),
  }
}

export function practiceRetrainSplitEstimate(split, totalMinor) {
  if (totalMinor==null || split.salesRate?.basis==='net_receipts' || split.serviceRate?.basis==='net_receipts') return
  const scale = 1000000
  const amount = rate => Math.floor(totalMinor/scale)*rate+
    Math.floor(((totalMinor%scale)*rate+scale/2)/scale)
  const sales = split.salesRate ? amount(split.salesRate.value) : split.personalMinor || 0
  const service = split.salesRate && split.serviceRate ? amount(split.salesRate.value+split.serviceRate.value)-sales :
    split.serviceRate ? amount(split.serviceRate.value) : split.serviceMinor || 0
  return {sales,service,pool:totalMinor-sales-service}
}

export function practiceRetrainPoolRemainder(split, totalMinor) {
  return practiceRetrainSplitEstimate(split,totalMinor)?.pool
}

export function practiceRetrainSplitBody(split, totalMinor) {
  if (!split.companyId) throw new Error('请选择销售公司')
  const amounts = practiceRetrainSplitAmounts(split)
  if (split.servicePersonId && amounts.serviceMinor===0) throw new Error('请填写大于零的服务固定金额')
  const salesRate = amounts.salesRate
  const serviceRate = amounts.serviceRate
  if (salesRate && serviceRate && salesRate.basis===serviceRate.basis && salesRate.value+serviceRate.value>1000000) {
    throw new Error('使用同一计算基数时，销售和服务比例合计不能超过 100%')
  }
  if (practiceRetrainPoolRemainder(amounts,totalMinor)<0) throw new Error('销售与服务份额合计不能超过复训总提成')
  if (!split.pool?.poolId) throw new Error('请选择团队池')
  const service = {}
  if (split.servicePersonId) {
    if (split.serviceLabel?.trim()) service.serviceLabel = split.serviceLabel.trim()
    if (split.serviceScopeMode==='selected') {
      if (!split.serviceSalesPeople?.length && !split.serviceSalesUnits?.length) throw new Error('请选择参与服务提成的销售人员或团队')
      if (split.serviceSalesPeople?.length) service.serviceSalesPeople = split.serviceSalesPeople
      if (split.serviceSalesUnits?.length) service.serviceSalesUnits = split.serviceSalesUnits
    }
  }
  return {companyId:split.companyId,pool:split.pool,...amounts,...service}
}

export function practiceRetrainServiceScopeText(split, names, limit = 2) {
  const base = names.company(split.companyId)+' · 已签到且可计提的复训报名'
  if (split.serviceScopeMode==='all' || !split.serviceScopeMode && !split.serviceSalesPeople?.length && !split.serviceSalesUnits?.length) return base
  return base+' · '+practicePeriodScopeText({restrictPayments:true,salesPeople:split.serviceSalesPeople,
    salesUnits:split.serviceSalesUnits},names,limit)
}

export function practiceRetrainShareText(fixed, rate, formatMoney) {
  if (!rate) return formatMoney(fixed || 0)+' / 份'
  return rate.value/10000+'% · '+(rate.basis==='commission' ? '按复训总提成' : '按报名净实收')
}

export function copiedRuleComponents(savedComponents, optionLabel) {
  const components = JSON.parse(JSON.stringify(savedComponents))
  return components.map(component => {
    const {tiers, periodBenefit, companySales, retrainSplits, value, ...form} = component
    if (form.salesPersonId) form.salesRecipientKind = 'person'
    if (['sales','referral'].includes(form.type) && !form.label) form.label = ruleComponentLabel(component,optionLabel)
    form.classMember ??= ''
    form.amount = value / (['period_rate','director_rate'].includes(form.mode) ? 10000 : 100)
    for (const key of ['companyExceptions','personExceptions']) {
      if (form.saleScope?.[key]) form.saleScope[key] = form.saleScope[key].map(row=>({...row,amount:row.value/100}))
    }
    if (tiers) form.tierConfig = tierForm(tiers)
    if (periodBenefit) {
      form.periodBenefit = {...periodBenefit,
        standardRates:periodBenefit.standardRates?.map(row=>({...row,scope:practiceStandardRateScope(row),value:row.value/10000})),
        restrictPayments:hasPracticePaymentScope(periodBenefit),
        recipient:periodBenefit.pool ? 'pool' : periodBenefit.unitId ? 'unit' : periodBenefit.orderAppointmentLevel ? 'order_appointment'
          : periodBenefit.orderRecipientRole ? 'order_person'
          : periodBenefit.salesCompanyRecipient ? 'sales_company' : periodBenefit.referralCompanyRecipient ? 'referral_company' : 'person',
        pool:periodBenefit.pool || {},
        method:periodBenefit.fixedMinor != null ? (periodBenefit.fixedPerOrder ? 'order_fixed' : periodBenefit.fixedPerClass ? 'class_fixed' : 'fixed')
          : periodBenefit.tiers?.length ? 'tiers' : 'rate',
        fixedAmount:periodBenefit.fixedMinor == null ? undefined : periodBenefit.fixedMinor / 100,
        tiers:periodBenefit.tiers?.map(row => ({from:row.from / 100,
          through:row.through == null ? undefined : row.through / 100, value:row.value / 10000})) || [{from:0}],
      }
    }
    if (companySales) {
      form.companySales = {...companySales, label:ruleComponentLabel(component,optionLabel),
        defaultRate:companySales.defaultRate == null ? undefined : companySales.defaultRate / 10000,
        general:companySales.general ? companyValue(companySales.general) : undefined,
        payee:{kind:'sales_company',...companySales.payee,pool:companySales.payee?.pool || {}},
        categories:(companySales.categories || []).map(category => ({...companyValue(category),
          salesCategoryIds:standardCategoryIDs(category),
          scope:standardCategoryIDs(category).length ? 'category' : 'companies', companies:category.companies || [],
          overrides:(category.overrides || []).map(row=>({...companyValue(row),companyIds:exceptionCompanyIDs(row)})),
        })),
      }
    }
    if (retrainSplits) form.retrainSplits = retrainSplits.map(practiceRetrainSplitForm)
    return form
  })
}

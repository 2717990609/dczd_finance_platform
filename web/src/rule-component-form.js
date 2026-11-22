const localTime = value => value ? new Date(Date.parse(value) + 8 * 60 * 60 * 1000).toISOString().slice(0, -1) : undefined

export function rateAsBasisPoints(value) {
  const match = /^(\d{1,3})(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const rate = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return rate <= 10000 ? rate : undefined
}

export const catalogFlatMethods = [
  {value:'rate',label:'统一比例'},
  {value:'fixed',label:'每笔付款固定金额'},
]
export const catalogCalculationMethods = [...catalogFlatMethods,{value:'table',label:'按类别或公司设置比例'}]
export const catalogSalesCalculationMethods = catalogCalculationMethods.filter(method=>method.value!=='fixed')

export function moneyAsMinor(value) {
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const minor = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return Number.isSafeInteger(minor) ? minor : undefined
}

export const catalogRatePriorityLabel = '公司特例 → 销售类别默认'
export const practiceCompanyRatePriorityLabel = '指定公司 → 销售类别默认 → 外部公司默认'

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
    if (arrangement.defaultRate != null) {
      register(classMember,'外部公司默认比例',['default'],arrangement)
    }
    for (const standard of arrangement.categories) {
      register(classMember,'销售类别',standard.salesCategoryIds || [],arrangement)
      register(classMember,'指定公司',standard.companies,arrangement)
      for (const override of standard.overrides || []) {
        register(classMember,'指定公司',override.companyIds,arrangement)
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

export function catalogRateConditionBody(row, scope = row.scope) {
  const rateBps = rateAsBasisPoints(row.rate)
  if (!row.relationship || rateBps===undefined) throw new Error('请核对销售范围和比例。')
  if (scope==='category' && !row.categoryIds.length) throw new Error('请选择销售类别。')
  if (scope==='company' && !row.companyIds.length) throw new Error('请选择特例公司。')
  return {relationship:row.relationship,rateBps,
    ...(row.payee ? {payee:row.payee} : {}),
    ...(scope!=='category' && scope!=='general' && row.companyIds?.length ? {companyIds:row.companyIds} : {company:'ANY'}),
    ...(scope!=='general' && row.categoryIds?.length ? {categoryIds:row.categoryIds} : {})}
}

export const catalogSalesPayees = [
  {value:'seller',label:'销售本人'},
  {value:'sales_company',label:'销售所属公司'},
]

export function catalogPayeeComplete(payee) {
  switch (payee.target) {
    case 'seller':
    case 'referrer': return true
    case 'person': return !!(payee.personId && payee.role)
    case 'company': return !!payee.organization
    case 'pool': return !!(payee.organization && payee.role && payee.memberRole)
    default: return false
  }
}

export function ruleComponentLabel(component, optionLabel) {
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
  !!(benefit.salesCompanies?.length || benefit.salesPeople?.length || benefit.salesUnits?.length)

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
  } else {
    list('团队或小组',benefit.salesUnits,names.unit)
    list('销售公司',benefit.salesCompanies,names.company)
    list('销售人员',benefit.salesPeople,names.person)
    if (!entries.length) entries.push(benefit.restrictPayments ? '待选择销售范围' : '本班全部适用报名')
  }
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

function tierMetric(schedule) {
  return schedule.count?.length ? (schedule.performance?.length ? 'fallback' : 'count') : 'performance'
}

export function tierFormHasCount(form) {
  return !form.onlyOverrides && form.metric!=='performance' ||
    form.companies.some(row=>row.metric!=='performance') ||
    form.categories.some(row=>row.metric!=='performance') ||
    form.people.some(row=>row.metric!=='performance')
}

function tierForm(tiers) {
  const schedules = tiers.companySchedules || []
  return {
    ...practiceCountStartForm(tiers),
    includeRetrain:Boolean(tiers.includeRetrain),
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

export function practiceSalesScopeText(part, names, limit = 2) {
  const entries = []
  const list = (label, ids, name) => {
    const values = [...new Set(ids)]
    if (values.length) entries.push(label+' '+values.slice(0,limit).map(name).join('、')+
      (values.length>limit ? ' 等 '+values.length+' 个' : ''))
  }
  if (part.companySales) {
    const rows = part.companySales.categories || []
    list('销售类别',rows.flatMap(standardCategoryIDs),names.category)
    list('销售公司',rows.flatMap(row=>[...(row.companies || []),...(row.overrides || []).flatMap(exceptionCompanyIDs)]),names.company)
    if (part.companySales.defaultRate != null) entries.push('其他外部公司')
    return entries.join(' · ') || '待配置类别或公司'
  }
  if (part.type!=='sales') return
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
    servicePersonId:split.servicePersonId,serviceBasis:split.serviceRate?.basis || 'fixed',
    service:split.serviceRate ? split.serviceRate.value/10000 : (split.serviceMinor || 0)/100,
  }
}

export const practiceRetrainShareMethods = [
  {value:'commission',label:'按本份复训总提成比例'},
  {value:'net_receipts',label:'按本份报名净实收比例'},
  {value:'fixed',label:'每份报名固定金额'},
]

export const newPracticeRetrainSplit = () => ({salesBasis:'commission',serviceBasis:'commission',pool:{}})

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
  if (!split.pool?.poolId || !split.pool?.ownerId) throw new Error('请选择资金池和分配负责人')
  return {companyId:split.companyId,pool:split.pool,...amounts}
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
    form.classMember ??= ''
    form.amount = value / (form.mode === 'period_rate' ? 10000 : 100)
    if (tiers) form.tierConfig = tierForm(tiers)
    if (periodBenefit) {
      form.periodBenefit = {...periodBenefit,
        restrictPayments:hasPracticePaymentScope(periodBenefit),
        recipient:periodBenefit.pool ? 'pool' : periodBenefit.unitId ? 'unit' : 'person',
        pool:periodBenefit.pool || {},
        method:periodBenefit.fixedMinor != null ? (periodBenefit.fixedPerClass ? 'class_fixed' : 'fixed')
          : periodBenefit.tiers?.length ? 'tiers' : 'rate',
        fixedAmount:periodBenefit.fixedMinor == null ? undefined : periodBenefit.fixedMinor / 100,
        tiers:periodBenefit.tiers?.map(row => ({from:row.from / 100,
          through:row.through == null ? undefined : row.through / 100, value:row.value / 10000})) || [{from:0}],
      }
    }
    if (companySales) {
      form.companySales = {...companySales, label:ruleComponentLabel(component,optionLabel),
        internalCompanies:companySales.internalCompanies || [],
        defaultRate:companySales.defaultRate == null ? undefined : companySales.defaultRate / 10000,
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

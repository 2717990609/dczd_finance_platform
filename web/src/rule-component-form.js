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

export function moneyAsMinor(value) {
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const minor = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return Number.isSafeInteger(minor) ? minor : undefined
}

export function catalogRateConditionBody(row) {
  const rateBps = rateAsBasisPoints(row.rate)
  if (!row.relationship || rateBps===undefined) throw new Error('请核对销售范围和比例。')
  return {relationship:row.relationship,rateBps,
    ...(row.payee ? {payee:row.payee} : {}),
    ...(row.companyIds?.length ? {companyIds:row.companyIds} : {company:'ANY'}),
    ...(row.categoryIds?.length ? {categoryIds:row.categoryIds} : {})}
}

export const catalogSalesPayees = [
  {value:'seller',label:'销售本人'},
  {value:'sales_company',label:'销售所属公司'},
]

export function catalogPayeeComplete(payee) {
  switch (payee.target) {
    case 'referrer': return true
    case 'person': return !!(payee.personId && payee.role)
    case 'company': return !!payee.organization
    case 'pool': return !!(payee.organization && payee.role)
    default: return false
  }
}

export function ruleComponentLabel(component, optionLabel) {
  if (component.companySales?.label != null) return component.companySales.label.trim() || '未命名收款安排'
  const label = component.periodBenefit?.label
  const type = optionLabel('component',component.type) || '分润分项'
  if (!label) return type
  const title = optionLabel('catalogAppointmentRole',label)
  return title!==label ? title : /^[A-Z][A-Z0-9_]*$/.test(label) ? type : label
}

function tierRows(rows, scale) {
  return (rows || []).map(row => ({
    ...row, from:row.from / scale, through:row.through == null ? undefined : row.through / scale, value:row.value / 100,
  }))
}

function countStart(start) {
  return {
    startMode:start.openingId ? 'opening' : start.countFromOrdinal ? 'ordinal' : 'time',
    openingId:start.openingId, countFromOrdinal:start.countFromOrdinal, countFromAt:localTime(start.countFromAt),
  }
}

function tierForm(tiers) {
  const schedules = tiers.companySchedules || []
  const hasCount = [tiers, ...schedules].some(schedule => schedule.count?.length)
  const hasPerformance = [tiers, ...schedules].some(schedule => schedule.performance?.length)
  return {
    ...countStart(tiers),
    metric:hasCount ? (hasPerformance ? 'fallback' : 'count') : 'performance',
    onlyCompanies:!tiers.count?.length && !tiers.performance?.length,
    count:tierRows(tiers.count,1), performance:tierRows(tiers.performance,100),
    personCountStarts:(tiers.personCountStarts || []).map(start => ({personId:start.personId, ...countStart(start)})),
    companies:schedules.map(schedule => ({
      companyId:schedule.companyId, count:tierRows(schedule.count,1), performance:tierRows(schedule.performance,100),
    })),
  }
}

function companyValue(row) {
  return {...row, value:row.value / (row.mode === 'rate' ? 10000 : 100),
    recommendationRate:row.recommendationRate == null ? undefined : row.recommendationRate / 10000}
}

export const standardCategoryIDs = row => row.salesCategoryIds || (row.salesCategoryId ? [row.salesCategoryId] : [])
export const exceptionCompanyIDs = row => row.companyIds || (row.companyId ? [row.companyId] : [])

export function copiedRuleComponents(savedComponents, optionLabel) {
  const components = JSON.parse(JSON.stringify(savedComponents))
  return components.map(component => {
    const {tiers, periodBenefit, companySales, retrainSplits, value, ...form} = component
    form.classMember ??= ''
    form.amount = value / (form.mode === 'period_rate' ? 10000 : 100)
    if (tiers) form.tierConfig = tierForm(tiers)
    if (periodBenefit) {
      form.periodBenefit = {...periodBenefit,
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
        payee:{kind:'sales_company',...companySales.payee,pool:companySales.payee?.pool || {}},
        categories:companySales.categories.map(category => ({...companyValue(category),
          salesCategoryIds:standardCategoryIDs(category),
          scope:standardCategoryIDs(category).length ? 'category' : 'companies', companies:category.companies || [],
          overrides:(category.overrides || []).map(row=>({...companyValue(row),companyIds:exceptionCompanyIDs(row)})),
        })),
      }
    }
    if (retrainSplits) form.retrainSplits = retrainSplits.map(split => ({companyId:split.companyId,
      pool:split.pool, personal:split.personalMinor / 100, servicePersonId:split.servicePersonId, service:(split.serviceMinor || 0) / 100,
    }))
    return form
  })
}

import { practiceSalesForm, practiceSalesPayload } from './practice-plugin-sales.js'
import { copiedRuleComponents, practiceCompanySalesBody, practiceRetrainSplitBody, practiceRetrainSplitForm } from './rule-component-form.js'

function required(value, message) {
  if (value == null || value === '' || value === 0) throw new Error(message)
  return value
}

function amount(value, scale, name, max = 1000000000) {
  if (value == null || !Number.isFinite(value) || value < 0 || value > max) throw new Error('请填写有效的'+name)
  return Math.round(value*scale)
}

function tiers(rows) {
  if (rows.length<2) throw new Error('阶梯至少需要两档')
  const starts = rows.map(row=>amount(row.from,100,'档位起点'))
  if (starts[0]!==0 || starts.some((from,index)=>index && from<=starts[index-1])) {
    throw new Error('阶梯从 0 开始，后续档位的起点须依次增加')
  }
  return rows.map((row,index)=>({
    min_inclusive:starts[index],max_exclusive:starts[index+1],rate_bps:amount(row.rate,10000,'计提比例',100),
  }))
}

function targetFields(row) {
  switch (row.target) {
    case 'person':
      if (row.orderAppointment) {
        const duty = row.orderAppointment
        return {orderAppointment:{
          level:required(duty.level,'请选择成交组织层级'),role:required(duty.role,'请选择收款岗位'),
          groupIds:duty.level==='TEAM' ? duty.groupIds : undefined,
          skipSellerHead:duty.level==='TEAM' && duty.skipSellerHead || undefined,
        }}
      }
      return {personId:required(row.personId,'请选择收款人'),appointment:row.appointment ? {
        scope:row.appointment.scope || undefined,
        unitId:!row.appointment.scope || row.appointment.scope==='ORGANIZATION_UNIT' ? required(row.appointment.unitId,'请选择任职组织') : undefined,
        role:required(row.appointment.role,'请选择任职岗位'),
      } : undefined}
    case 'company':
      return {unitId:required(row.unitId,'请选择收款公司')}
    case 'pool':
      return {poolId:required(row.poolId,'请选择团队池')}
    default:
      return {}
  }
}

function calculationFields(row) {
  if (row.method==='fixed') return {fixedMinor:amount(row.amount,100,'固定金额')}
  if (row.method==='tiers') return {tiers:tiers(row.tiers)}
  const rate = amount(row.rate,10000,'计提比例',100)
  if (row.target!=='order_seller' || row.grain!=='payment' || !row.rateOverrides?.length) return {rate}
  const seen = new Set()
  const rateOverrides = row.rateOverrides.map(override=>{
    const id = required(override.id,'请选择指定公司或销售类别')
    const key = override.scope+'-'+id
    if (seen.has(key)) throw new Error('同一个公司或类别只能配置一条比例')
    seen.add(key)
    return {[override.scope==='company' ? 'companyId' : 'categoryId']:id,rate:amount(override.rate,10000,'计提比例',100)}
  })
  return {rate,rateOverrides}
}

export function rulePluginPayload(name, rows, baseId) {
  const label = required(name.trim(),'请填写规则名称')
  if (!rows.length) throw new Error('请添加计提项')
  return {baseId,label,arrangements:rows.map(row=>{
    if (row.practiceCompany) {
      const practiceCompanySales = practiceCompanySalesBody(row.practiceCompany)
      return {key:row.key,label:practiceCompanySales.label,targetKind:'company',method:'rate',grain:'payment',
        signupType:row.practiceCompany.classMember,practiceCompanySales}
    }
    const identity = {key:row.key,label:required(row.name.trim(),'请填写计提项名称')}
    if (row.internalSplit) throw new Error('请移除旧的内部销售拆分项，将销售和岗位提成分别配置')
    if (row.practiceSales) return {...identity,targetKind:'order_seller',method:'tiers',grain:'payment',signupType:'new',
      practiceSales:practiceSalesPayload(row.practiceSales)}
    if (row.practiceRetrain) {
      const fixedMinor = amount(row.practiceRetrain.amount,100,'每份复训总提成')
      if (!row.practiceRetrain.retrainSplits.length) throw new Error('请添加至少一家复训拆分公司')
      return {...identity,targetKind:'order_seller',method:'fixed',grain:'payment',signupType:'retrain',fixedBasis:'signup',
        fixedMinor,practiceRetrainSplits:row.practiceRetrain.retrainSplits.map(split=>practiceRetrainSplitBody(split,fixedMinor))}
    }
    return {...identity,targetKind:row.target,
      method:row.method,grain:row.grain,signupType:row.signupType || undefined,salesUnits:row.units,salesCategories:row.categories,
      salesPeople:row.salesPeople?.length ? row.salesPeople : undefined,
      fixedBasis:row.method==='fixed' && row.grain==='payment' && row.fixedBasis==='signup' ? 'signup' : undefined,
      tierScope:row.method==='tiers' && row.grain==='period' && row.tierScope==='class_net' ? 'class_net' : undefined,
      ...targetFields(row),...calculationFields(row),
    }
  })}
}

export const isClassOnlyRule = rule => rule.arrangements.some(row=>
  row.practiceSales || row.practiceCompanySales || row.practiceRetrainSplits?.length || row.salesPeople?.length ||
  ['retrain','new_retrain'].includes(row.signupType) || row.fixedBasis==='signup' || row.tierScope==='class_net')

export const isCalendarOnlyRule = rule => rule.arrangements.some(row=>
  row.internalSplit || row.orderAppointment || ['PROJECT','PRODUCT'].includes(row.appointment?.scope))

export function rulePluginRows(arrangements, practiceCompanies = []) {
  const companies = new Map(practiceCompanies.map(company=>[company.value,company]))
  return arrangements.flatMap(row=>{
    if (!row.practiceRetrainSplits?.length || row.fixedMinor==null || row.practiceRetrainSplits.some(split=>
      !companies.get(split.companyId)?.scope?.organizationId || split.salesRate || split.serviceRate ||
      split.serviceSalesPeople?.length || split.serviceSalesUnits?.length)) return [rulePluginRow(row)]
    const units = new Set(row.practiceRetrainSplits.map(split=>companies.get(split.companyId).scope.organizationId))
    if (units.size!==row.practiceRetrainSplits.length) return [rulePluginRow(row)]

    const items = new Map()
    function add(label, targetKind, unitId, fixedMinor, recipient = {}) {
      if (fixedMinor===0) return
      const key = JSON.stringify([label,targetKind,fixedMinor,recipient])
      const existing = items.get(key)
      if (existing) {
        existing.salesUnits.push(unitId)
        return
      }
      items.set(key,{
        key:row.key+'-'+items.size,label,targetKind,grain:'payment',method:'fixed',fixedMinor,
        fixedBasis:'signup',signupType:'retrain',salesUnits:[unitId],...recipient,
      })
    }
    for (const split of row.practiceRetrainSplits) {
      const company = companies.get(split.companyId)
      const unitId = company.scope.organizationId
      const serviceMinor = split.serviceMinor || 0
      add('复训销售提成','order_seller',unitId,split.personalMinor)
      if (split.servicePersonId) {
        add(split.serviceLabel || company.label+'复训服务提成','person',unitId,serviceMinor,{personId:split.servicePersonId})
      }
      add(company.label+'复训团队池','pool',unitId,row.fixedMinor-split.personalMinor-serviceMinor,{poolId:split.pool.poolId})
    }
    return items.size ? [...items.values()].map(rulePluginRow) : [rulePluginRow(row)]
  })
}

export function rulePluginRow(row) {
  return {key:row.key,name:row.label,target:row.targetKind,personId:row.personId,personName:row.personName,
    unitId:row.unitId,poolId:row.poolId,grain:row.grain,method:row.method,
    amount:row.fixedMinor==null ? undefined : row.fixedMinor/100,
    rate:row.rate==null ? undefined : row.rate/10000,
    rateOverrides:(row.rateOverrides || []).map(override=>({
      scope:override.companyId ? 'company' : 'category',id:override.companyId || override.categoryId,rate:override.rate/10000,
    })),
    tiers:row.tiers?.map(tier=>({from:(tier.min_inclusive || 0)/100,rate:tier.rate_bps/10000})) || [{from:0},{}],
    units:row.salesUnits || [],categories:row.salesCategories || [],salesPeople:row.salesPeople || [],appointment:row.appointment,
    orderAppointment:row.orderAppointment,
    signupType:row.signupType || (row.newOnly ? 'new' : ''),fixedBasis:row.fixedBasis || 'payment',tierScope:row.tierScope || '',
    practiceSales:row.practiceSales ? practiceSalesForm(row.practiceSales) : undefined,
    practiceCompany:row.practiceCompanySales ? copiedRuleComponents([{
      type:'company_sales',mode:'company_sales',value:0,classMember:row.signupType,companySales:row.practiceCompanySales,
    }],()=>row.label)[0] : undefined,
    practiceRetrain:row.practiceRetrainSplits?.length ? {mode:'fixed',amount:row.fixedMinor/100,
      retrainSplits:row.practiceRetrainSplits.map(practiceRetrainSplitForm)} : undefined,
    internalSplit:row.internalSplit ? {...row.internalSplit,groupIds:row.internalSplit.groupIds || [],
      ...Object.fromEntries(['sellerRate','headSellerRate','groupRate','teamRate','serviceRate'].map(key=>[key,row.internalSplit[key]/10000])),
    } : undefined,
  }
}

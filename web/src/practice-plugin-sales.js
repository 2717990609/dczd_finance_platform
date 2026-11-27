import { practiceCountStartBody, practiceCountStartForm } from './rule-component-form.js'

const newPerformance = () => [{from:0}]

export function practiceSalesSchedule() {
  return {companyIds:[],performance:newPerformance()}
}

export function practiceSalesForm(tiers) {
  const schedules = [...(tiers?.companySchedules || [])]
  if (tiers?.performance?.length) schedules.unshift(tiers)
  const count = schedules.find(schedule=>schedule.count?.length)?.count[0]
  return {
    metric:count ? 'count' : 'performance',
    threshold:count?.from,
    fromOrdinal:tiers?.countFromOrdinal,
    fromAt:tiers?.countFromAt,
    openingId:tiers?.openingId,
    openingMode:tiers?.openingId ? 'opening' : tiers?.countFromAt ? 'time' : 'ordinal',
    personCountStarts:(tiers?.personCountStarts || []).map(start=>({personId:start.personId,...practiceCountStartForm(start)})),
    schedules:schedules.length ? schedules.map(schedule=>({
      companyIds:schedule.companyIds || (schedule.companyId ? [schedule.companyId] : []),
      performance:schedule.performance.map(row=>({from:row.from/100,rate:row.value/100})),
      countRate:schedule.count?.length ? schedule.count[0].value/100 : undefined,
    })) : [practiceSalesSchedule()],
  }
}

function rate(value) {
  if (!Number.isFinite(value) || value<0 || value>100) throw new Error('请填写实战班销售阶梯的有效比例')
  const result = Math.round(value*100)
  if (Math.abs(value*100-result)>0.000001) throw new Error('销售阶梯比例最多保留两位小数')
  return result
}

export function practiceSalesPayload(form) {
  const history = form.metric==='count'
  const result = {companySchedules:[]}
  if (history) {
    if (!Number.isSafeInteger(form.threshold) || form.threshold<1) throw new Error('请填写累计新报人数门槛')
    if (form.openingMode==='opening') {
      if (!form.openingId) throw new Error('请选择已确认的期初人数')
      result.openingId = form.openingId
    } else if (form.openingMode==='time') {
      if (!form.fromAt) throw new Error('请填写累计起始时间')
      result.countFromAt = form.fromAt
    } else {
      if (!Number.isSafeInteger(form.fromOrdinal) || form.fromOrdinal<1) throw new Error('请填写累计起始期数')
      result.countFromOrdinal = form.fromOrdinal
    }
    if (form.personCountStarts?.length) {
      const people = new Set()
      result.personCountStarts = form.personCountStarts.map(start=>{
        if (!Number.isSafeInteger(start.personId) || start.personId<1) throw new Error('请选择需要单独起算的人员')
        if (people.has(start.personId)) throw new Error('同一人员只能配置一个累计起点')
        people.add(start.personId)
        return {personId:start.personId,...practiceCountStartBody(start)}
      })
    }
  }
  const companyIds = new Set()
  for (const schedule of form.schedules) {
    if (schedule.performance.some(tier=>!Number.isFinite(tier.from) || tier.from<0 || tier.from>1000000000)) {
      throw new Error('请填写有效的业绩档位起点')
    }
    const starts = schedule.performance.map(tier=>Math.round(tier.from*100))
    if (starts[0]!==0 || starts.some((from,index)=>index && from<=starts[index-1])) {
      throw new Error('业绩档位从 0 元开始，后续档位的起点须依次增加')
    }
    const tiers = {performance:schedule.performance.map((tier,index)=>({
      from:starts[index],through:index+1<starts.length ? starts[index+1]-1 : undefined,mode:'rate',value:rate(tier.rate),
    }))}
    if (history) tiers.count = [{from:form.threshold,mode:'rate',value:rate(schedule.countRate)}]
    if (schedule.companyIds.length) {
      for (const id of schedule.companyIds) {
        if (companyIds.has(id)) throw new Error('同一家销售公司只能使用一张阶梯表')
        companyIds.add(id)
      }
      result.companySchedules.push({...tiers,companyIds:schedule.companyIds})
    } else {
      if (result.performance) throw new Error('请为其他档位选择销售公司')
      Object.assign(result,tiers)
    }
  }
  return result
}

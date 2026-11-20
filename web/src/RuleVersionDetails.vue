<script setup>
import { onMounted, ref } from 'vue'

const props = defineProps({
  rule: Object, canCopy: Boolean, classProduct: Boolean, api: Function, optionLabel: Function,
  formatTime: Function, formatMoney: Function, people: Array, openingLabel: Function,
})
const emit = defineEmits(['close','copy'])
const directory = ref()
const label = (group, key) => props.optionLabel(group, key) || key
const roleName = key => label('orderAttributionRole', key) !== key
  ? label('orderAttributionRole', key) : label('practicePeriodSourceRole', key)
const personName = id => directory.value?.people.find(person=>person.id===id)?.name
  || props.people.find(person=>person.id===id)?.name || '人员 '+id
const unitName = id => directory.value?.units.find(unit=>unit.id===id)?.name || '组织 '+id
const poolName = target => {
  const name = directory.value?.pools.find(pool=>pool.id===target.poolId)?.name || '资金池 '+target.poolId
  return name+' · 分配负责人 '+personName(target.ownerId)
}
const companyName = key => label('practiceSalesCompany', key)
const rate = (value, scale = 100) => (value / scale)+'%'
const standard = (row, scale) => row.mode==='rate' ? rate(row.value,scale) : props.formatMoney(row.value)
const countStart = rule => rule.openingId ? props.openingLabel(rule.openingId)
  : rule.countFromOrdinal ? '第 '+rule.countFromOrdinal+' 期' : rule.countFromAt && props.formatTime(rule.countFromAt)
const scope = props.rule.periodName || (props.rule.fromOrdinal ? '从第 '+props.rule.fromOrdinal+' 期起' : '产品默认')
const range = (row, count) => {
  const value = n => count ? n+' 人' : props.formatMoney(n)
  return row.through == null ? value(row.from)+' 起' : value(row.from)+' 至 '+value(row.through)
}
function recipient(part) {
  const benefit = part.periodBenefit
  if (benefit?.pool) return poolName(benefit.pool)
  if (benefit?.personId) return personName(benefit.personId)
  if (benefit?.unitId) return unitName(benefit.unitId)
  if (part.companySales) return '规则指定的销售公司'
  return roleName(part.sourceRole) || '—'
}
onMounted(async () => {
  if (!props.classProduct || !props.canCopy || !props.rule.components.some(part=>part.periodBenefit || part.retrainSplits?.length || part.tiers?.personCountStarts?.length)) return
  try {
    directory.value = await props.api('/practice-rule-recipients?productId='+props.rule.productId)
  } catch {
    // The stored IDs remain available if the directory cannot be loaded.
  }
})
</script>

<template>
  <a-drawer open title="查看规则" :width="'min(660px, 100vw)'" @close="emit('close')">
    <section aria-label="规则详情" class="rule-preview">
      <h2>{{ rule.label }} <a-tag>v{{ rule.version }}</a-tag></h2>
      <a-descriptions bordered size="small" :column="1" class="rule-preview-scope">
        <a-descriptions-item label="产品">{{ rule.productName }}</a-descriptions-item>
        <a-descriptions-item label="适用范围">{{ scope }}</a-descriptions-item>
        <a-descriptions-item label="生效时间">{{ rule.effectiveFrom ? formatTime(rule.effectiveFrom) : '范围默认' }}<template v-if="rule.effectiveTo"> 至 {{ formatTime(rule.effectiveTo) }}</template></a-descriptions-item>
      </a-descriptions>
      <article v-for="(part,index) in rule.components" :key="index" class="rule-preview-part">
        <header>
          <h3>{{ part.periodBenefit?.label || label('component', part.type) }}</h3>
          <a-tag v-if="part.classMember">{{ label('classMember', part.classMember) }}</a-tag>
        </header>
        <a-descriptions size="small" :column="1">
          <a-descriptions-item label="受益对象">{{ recipient(part) }}</a-descriptions-item>
          <a-descriptions-item label="计提标准">
            <template v-if="part.mode==='period_rate'">{{ part.periodBenefit?.tiers?.length ? '按班次阶梯计提' : rate(part.value,10000) }}</template>
            <template v-else-if="part.tiers || part.companySales">{{ label('amountMode', part.mode) }}</template>
            <template v-else>{{ standard(part) }}</template>
          </a-descriptions-item>
          <a-descriptions-item v-if="part.refundPolicy" label="退款处理">{{ label('refundPolicy',part.refundPolicy) }}</a-descriptions-item>
        </a-descriptions>
        <details v-if="part.tiers || part.periodBenefit || part.companySales || part.retrainSplits?.length">
          <summary>查看计算条件</summary>
          <template v-if="part.tiers">
            <p v-if="countStart(part.tiers)">累计起点 {{ countStart(part.tiers) }}</p>
            <ul v-if="part.tiers.personCountStarts?.length">
              <li v-for="start in part.tiers.personCountStarts" :key="start.personId">{{ personName(start.personId) }} · 从 {{ countStart(start) }} 起</li>
            </ul>
            <template v-for="schedule in [part.tiers, ...(part.tiers.companySchedules || [])]" :key="schedule.companyId || ''">
              <h4 v-if="schedule.count?.length || schedule.performance?.length">{{ schedule.companyId ? companyName(schedule.companyId) : '通用阶梯' }}</h4>
              <ul>
                <li v-for="row in schedule.count" :key="'count-'+row.from">累计 {{ range(row,true) }} · {{ standard(row) }}</li>
                <li v-for="row in schedule.performance" :key="'paid-'+row.from">本期净实收 {{ range(row,false) }} · {{ standard(row) }}</li>
              </ul>
            </template>
          </template>
          <template v-if="part.periodBenefit">
            <p v-if="part.periodBenefit.salesCompanies?.length">销售公司 {{ part.periodBenefit.salesCompanies.map(companyName).join('、') }}</p>
            <p v-if="part.periodBenefit.salesPeople?.length">销售人员 {{ part.periodBenefit.salesPeople.map(personName).join('、') }} · {{ roleName(part.sourceRole) }}</p>
            <p v-if="part.periodBenefit.appointmentUnitId">任职期间 {{ unitName(part.periodBenefit.appointmentUnitId) }} · {{ label('appointmentKind',part.periodBenefit.appointmentKind) }}</p>
            <p v-if="part.periodBenefit.tiers?.length">{{ part.periodBenefit.tierOnWholeClass ? '用全班净实收选档' : '用适用报名净实收选档' }}</p>
            <ul v-if="part.periodBenefit.tiers?.length">
              <li v-for="row in part.periodBenefit.tiers" :key="row.from">{{ range(row,false) }} · {{ rate(row.value,10000) }}</li>
            </ul>
            <p v-if="!part.periodBenefit.salesCompanies?.length && !part.periodBenefit.salesPeople?.length && !part.periodBenefit.appointmentUnitId && !part.periodBenefit.tiers?.length">按本班符合条件的净实收计提</p>
          </template>
          <template v-if="part.companySales">
            <p v-if="part.companySales.internalCompanies?.length">内部销售公司 {{ part.companySales.internalCompanies.map(companyName).join('、') }}</p>
            <p v-if="part.companySales.recommendationRecipients?.length">公司推荐收款范围 {{ part.companySales.recommendationRecipients.map(companyName).join('、') }}</p>
            <template v-for="group in part.companySales.categories" :key="group.label">
              <h4>{{ group.label }} · {{ standard(group,10000) }}<template v-if="group.recommendationRate!=null"> · 推荐 {{ rate(group.recommendationRate,10000) }}</template></h4>
              <p>{{ group.companies.map(companyName).join('、') }}</p>
              <ul v-if="group.overrides?.length">
                <li v-for="row in group.overrides" :key="row.companyId">{{ companyName(row.companyId) }} · {{ standard(row,10000) }}<template v-if="row.recommendationRate!=null"> · 推荐 {{ rate(row.recommendationRate,10000) }}</template></li>
              </ul>
            </template>
          </template>
          <ul v-if="part.retrainSplits?.length">
            <li v-for="split in part.retrainSplits" :key="split.companyId">
              {{ companyName(split.companyId) }} · 个人 {{ formatMoney(split.personalMinor) }}
              <template v-if="split.servicePersonId"> · 服务 {{ personName(split.servicePersonId) }} {{ formatMoney(split.serviceMinor) }}</template>
              · {{ poolName(split.pool) }}
            </li>
          </ul>
        </details>
      </article>
      <div class="workspace-edit-footer">
        <a-button @click="emit('close')">关闭</a-button>
        <a-button v-if="canCopy" type="primary" @click="emit('copy')">复制配置</a-button>
      </div>
    </section>
  </a-drawer>
</template>

<style scoped>
.rule-preview h2 { display:flex; align-items:center; flex-wrap:wrap; gap:8px; margin:0 0 12px; font-size:18px; }
.rule-preview-scope { margin-bottom:16px; }
.rule-preview-part { padding:12px 14px; margin-bottom:12px; border:1px solid var(--line); border-radius:6px; }
.rule-preview-part header { display:flex; flex-wrap:wrap; align-items:center; gap:8px; margin-bottom:8px; }
.rule-preview-part h3 { margin:0; font-size:15px; }
.rule-preview-part h4, .rule-preview-part p { margin:8px 0 4px; font-size:13px; }
.rule-preview-part ul { margin:4px 0; padding-left:20px; }
.rule-preview-part summary { color:var(--accent); cursor:pointer; }
</style>

<script setup>
import { computed, onMounted, ref } from 'vue'
import { Drawer } from 'ant-design-vue'
import { DownOutlined } from '@ant-design/icons-vue'
import { ruleComponentLabel } from './rule-component-form'

const props = defineProps({
  rule: Object, canCopy: Boolean, classProduct: Boolean, api: Function, optionLabel: Function,
  formatTime: Function, formatMoney: Function, people: Array, openingLabel: Function, title: String, recipients: Object,
  embedded: Boolean,
})
const emit = defineEmits(['close','copy'])
const loadedRecipients = ref()
const activePart = ref(props.embedded || props.rule.components.length===1 ? 0 : undefined)
const directoryError = ref('')
const directory = computed(() => loadedRecipients.value ?? props.recipients)
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
const fixedUnit = computed(() => props.classProduct ? ' / 报名' : ' / 付款')
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
  if (part.companySales) {
    const payee = part.companySales.payee
    if (payee?.kind==='order_person') return roleName(payee.role)
    if (payee?.kind==='person') return personName(payee.personId)
    if (payee?.kind==='company') return unitName(payee.companyId)
    if (payee?.kind==='pool') return poolName(payee.pool)
    return '销售所属公司'
  }
  const source = roleName(part.sourceRole)
  return part.retrainSplits?.length ? [source, '拆分指定的服务人员及团队池'].filter(Boolean).join('、') : source || '—'
}
function formula(part) {
  if (part.mode==='period_rate') {
    return '本班适用报名净实收 × '+(part.periodBenefit?.tiers?.length ? '档位比例' : rate(part.value,10000))
  }
  if (part.companySales) return '按销售类别或公司标准计提'
  if (part.tiers) return '按累计人数或本班业绩匹配档位'
  if (part.mode==='rate') return (props.classProduct ? '报名净实收' : '付款实收')+' × '+standard(part)
  return standard(part)+fixedUnit.value
}
onMounted(async () => {
  if (props.embedded || props.recipients || !props.classProduct ||
    !props.rule.components.some(part=>part.periodBenefit || part.retrainSplits?.length ||
    part.tiers?.personCountStarts?.length || part.companySales?.recommendationRecipients?.length ||
    ['person','company','pool'].includes(part.companySales?.payee?.kind))) return
  try {
    loadedRecipients.value = await props.api('/practice-rule-recipients?ruleId='+props.rule.id)
  } catch (err) {
    directoryError.value = err.message || '收款对象名称读取失败'
  }
})
</script>

<template>
  <component :is="embedded ? 'div' : Drawer" v-bind="embedded ? {} : {open:true,title:title || '查看规则',width:'min(1120px, 100vw)'}" @close="emit('close')">
    <section aria-label="规则详情" class="rule-preview">
      <h2>{{ rule.label }} <a-tag>{{ embedded ? '待发布' : 'v'+rule.version }}</a-tag></h2>
      <a-alert v-if="directoryError" class="feedback" :message="directoryError" type="warning" show-icon/>
      <a-descriptions bordered size="small" :column="{xs:1,sm:1,md:2}" class="rule-preview-scope">
        <a-descriptions-item label="产品">{{ rule.productName }}</a-descriptions-item>
        <a-descriptions-item label="适用范围">{{ scope }}</a-descriptions-item>
        <a-descriptions-item label="生效时间">{{ rule.effectiveFrom ? formatTime(rule.effectiveFrom) : '范围默认' }}<template v-if="rule.effectiveTo"> 至 {{ formatTime(rule.effectiveTo) }}</template></a-descriptions-item>
        <a-descriptions-item v-if="rule.retiredFrom" label="停用时间">{{ formatTime(rule.retiredFrom) }}</a-descriptions-item>
      </a-descriptions>
      <div class="rule-blocks" aria-label="收款安排">
      <article v-for="(part,index) in rule.components" :key="index" class="rule-block">
        <button type="button" class="rule-block-header" :aria-expanded="activePart===index" :aria-controls="'rule-preview-'+index"
          @click="activePart=activePart===index ? undefined : index">
          <DownOutlined aria-hidden="true"/>
          <strong>{{ ruleComponentLabel(part,label) }}</strong>
          <span v-if="classProduct" class="rule-block-badge">{{ part.classMember ? label('classMember',part.classMember) : '通用报名' }}</span>
          <small>{{ recipient(part) }}</small>
        </button>
        <div v-if="activePart===index" :id="'rule-preview-'+index" class="rule-block-body rule-preview-part">
        <a-descriptions size="small" :column="1">
          <a-descriptions-item label="发给谁">{{ recipient(part) }}</a-descriptions-item>
          <a-descriptions-item label="提成怎么算">{{ formula(part) }}</a-descriptions-item>
          <a-descriptions-item v-if="part.periodBenefit?.tiers?.length" label="阶梯选档范围">
            {{ part.periodBenefit.tierOnWholeClass ? '全班净实收（新报及复训）' : '适用报名净实收' }}
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
            <p v-if="part.periodBenefit.salesUnits?.length">业绩部门或小组 {{ part.periodBenefit.salesUnits.map(unitName).join('、') }} · {{ roleName(part.sourceRole) }}</p>
            <p v-if="part.periodBenefit.appointmentUnitId">任职期间 {{ unitName(part.periodBenefit.appointmentUnitId) }} · {{ label('appointmentKind',part.periodBenefit.appointmentKind) }}</p>
            <ul v-if="part.periodBenefit.tiers?.length">
              <li v-for="row in part.periodBenefit.tiers" :key="row.from">{{ range(row,false) }} · {{ rate(row.value,10000) }}</li>
            </ul>
            <p v-if="!part.periodBenefit.salesCompanies?.length && !part.periodBenefit.salesPeople?.length && !part.periodBenefit.salesUnits?.length && !part.periodBenefit.appointmentUnitId && !part.periodBenefit.tiers?.length">按本班符合条件的净实收计提</p>
          </template>
          <template v-if="part.companySales">
            <p v-if="part.companySales.internalCompanies?.length">内部销售公司 {{ part.companySales.internalCompanies.map(companyName).join('、') }}</p>
            <p v-if="part.companySales.recommendationRecipients?.length">公司推荐收款范围 {{ part.companySales.recommendationRecipients.map(unitName).join('、') }}</p>
            <template v-for="group in part.companySales.categories" :key="group.salesCategoryId || group.label">
              <h4>{{ group.salesCategoryId ? label('practiceSalesCategory',group.salesCategoryId) : group.label }} · {{ standard(group,10000) }}<template v-if="group.recommendationRate!=null"> · 推荐 {{ rate(group.recommendationRate,10000) }}</template></h4>
              <p v-if="group.companies?.length">{{ group.companies.map(companyName).join('、') }}</p>
              <ul v-if="group.overrides?.length">
                <li v-for="row in group.overrides" :key="row.companyId">{{ companyName(row.companyId) }} · {{ standard(row,10000) }}<template v-if="row.recommendationRate!=null"> · 推荐 {{ rate(row.recommendationRate,10000) }}</template></li>
              </ul>
            </template>
          </template>
          <ul v-if="part.retrainSplits?.length">
            <li v-for="split in part.retrainSplits" :key="split.companyId">
              {{ companyName(split.companyId) }} · 个人 {{ formatMoney(split.personalMinor) }}
              <template v-if="split.servicePersonId"> · 服务 {{ personName(split.servicePersonId) }} {{ formatMoney(split.serviceMinor) }}</template>
              · {{ poolName(split.pool) }} {{ formatMoney(part.value - split.personalMinor - (split.serviceMinor || 0)) }}
            </li>
          </ul>
        </details>
        </div>
      </article>
      </div>
      <div v-if="!embedded" class="workspace-edit-footer">
        <a-button @click="emit('close')">关闭</a-button>
        <a-button v-if="canCopy" type="primary" @click="emit('copy')">复制配置</a-button>
      </div>
    </section>
  </component>
</template>

<style scoped>
.rule-preview h2 { display:flex; align-items:center; flex-wrap:wrap; gap:8px; margin:0 0 12px; font-size:18px; }
.rule-preview-scope { margin-bottom:16px; }
.rule-preview-part h4, .rule-preview-part p { margin:8px 0 4px; font-size:13px; }
.rule-preview-part ul { margin:4px 0; padding-left:20px; }
.rule-preview-part summary { color:var(--accent); cursor:pointer; }
</style>

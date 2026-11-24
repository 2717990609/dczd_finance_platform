<script setup>
import { computed, ref } from 'vue'
import PracticeTierRanges from './PracticeTierRanges.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import { practiceCumulativeAmountMetrics } from './rule-component-form'

const props = defineProps({
  config: { type: Object, required: true },
  metrics: { type: Array, required: true },
  amounts: { type: Array, required: true },
  people: { type: Array, required: true },
  companies: { type: Array, required: true },
  salesCategories: { type: Array, required: true },
  showLegacyRetrain: Boolean,
  retrainUnavailable: Boolean,
})
const metricOptions = computed(() => [
  {value:'count',label:'累计新报人数'},
  ...practiceCumulativeAmountMetrics,
  ...(props.config.hasLegacyMetrics ? [{value:'legacy',label:'沿用各组档位指标'}] : []),
])
const amountLabel = computed(() => practiceCumulativeAmountMetrics.find(metric=>metric.value===props.config.cumulativeMetric)?.label)
const metricMode = computed(() => props.config.cumulativeMetric || (props.config.forceCount ? 'count' : 'legacy'))
function setMetric(metric) {
  const config = props.config
  config.cumulativeMetric = practiceCumulativeAmountMetrics.some(option=>option.value===metric) ? metric : undefined
  config.forceCount = metric==='count'
  if (config.cumulativeMetric && config.startMode==='opening') config.startMode = undefined
  if (!config.cumulativeMetric && !config.startMode && config.openingId) config.startMode = 'opening'
}
function sectionsFor(metric) {
  if (amountLabel.value) return [{key:'performance',title:amountLabel.value,unit:'元',decimals:2}]
  if (props.config.forceCount) metric = 'count'
  const result = []
  if (metric !== 'performance') result.push({ key: 'count', title: '累计人数档', unit: '人', decimals: 0 })
  if (metric !== 'count') result.push({ key: 'performance', title: '本期实付档', unit: '元', decimals: 2 })
  return result
}
const sections = computed(() => sectionsFor(props.config.metric))
const assignedCategories = computed(() => new Set(props.config.categories.flatMap(row=>row.salesCategoryIds)))
const canAddCategory = computed(() => props.salesCategories.some(category=>!assignedCategories.value.has(category.value)))
const assignedCompanies = computed(() => new Set(props.config.companies.flatMap(row=>row.companyIds || [])))
const canAddCompany = computed(() => props.companies.some(company=>!assignedCompanies.value.has(company.value)))
const assignedPeople = computed(() => props.config.people.filter(row=>row.personId).length)
const activeGroup = ref(props.config.categories.length ? 'category' : props.config.companies.length ? 'company' :
  props.config.people.length ? 'person' : 'general')
const groups = computed(() => [
  {value:'category',count:assignedCategories.value.size},
  {value:'company',count:assignedCompanies.value.size},
  {value:'person',count:assignedPeople.value},
  {value:'general'},
])
const priority = [
  {scope:'person',label:'个人特例'},
  {scope:'company',label:'组织特例'},
  {scope:'category',label:'销售类别'},
  {scope:'general',label:'通用档位'},
]
function eligiblePeople(rows, row) {
  return props.people.filter(person =>
    !rows.some(other => other !== row && other.personId === person.value))
}
function eligibleCompanies(row) {
  return props.companies.filter(company =>
    row.companyIds?.includes(company.value) || !assignedCompanies.value.has(company.value))
}
function eligibleCategories(row) {
  return props.salesCategories.filter(category =>
    row.salesCategoryIds.includes(category.value) || !assignedCategories.value.has(category.value))
}
function newSchedule() {
  const mode = props.amounts[0]?.value
  const payout = () => props.config.includeRetrain ? {mode,retrain:{mode}} : {mode}
  return {count:[payout()],performance:[{from:0,...payout()}]}
}
function setIncludeRetrain(checked) {
  props.config.includeRetrain = checked
  if (!checked) return
  for (const schedule of [props.config,...props.config.categories,...props.config.companies,...props.config.people]) {
    for (const row of [...schedule.count,...schedule.performance]) row.retrain ??= {mode:props.amounts[0]?.value}
  }
}
function removeSchedule(schedules, index) {
  schedules.splice(index,1)
}
</script>

<template>
  <div class="tier-editor">
    <div class="tier-toolbar">
      <div class="tier-settings">
        <a-form-item label="阶梯指标" html-for="tier-amount-metric">
          <a-select id="tier-amount-metric" :value="metricMode" :options="metricOptions" @change="setMetric"/>
        </a-form-item>
        <a-checkbox v-if="showLegacyRetrain" :checked="config.includeRetrain" :disabled="retrainUnavailable && !config.includeRetrain"
          @change="setIncludeRetrain($event.target.checked)">使用原有复训档值</a-checkbox>
      </div>
    </div>
    <RuleCommissionStandards v-model="activeGroup" :groups="groups" :priority="priority" embedded>
      <p class="tier-note tier-bounds">上下限均含本值，上限留空至下一档前，最后一档可留空表示不限。</p>
      <section v-if="activeGroup==='category'" class="tier-group" aria-label="销售类别默认">
        <div v-for="(category,index) in config.categories" :key="index" class="tier-range">
          <div class="tier-values">
            <a-form-item :html-for="'tier-category-'+index" label="销售类别" required>
              <a-select :id="'tier-category-'+index" v-model:value="category.salesCategoryIds" :options="eligibleCategories(category)"
                mode="multiple" :max-tag-count="3" allow-clear option-filter-prop="label"/>
            </a-form-item>
            <a-form-item v-if="metricMode==='legacy'" :html-for="'tier-category-metric-'+index" label="阶梯指标">
              <a-select :id="'tier-category-metric-'+index" v-model:value="category.metric" :options="metrics"/>
            </a-form-item>
          </div>
          <PracticeTierRanges :config="category" :sections="sectionsFor(category.metric)" :amounts="amounts"
            :include-retrain="config.includeRetrain" :prefix="'category-'+index+'-'"/>
          <a-button type="link" danger size="small" @click="removeSchedule(config.categories,index)">移除类别档位</a-button>
        </div>
        <a-button v-if="canAddCategory" type="dashed" size="small"
          @click="config.categories.push({salesCategoryIds:[],metric:config.metric,...newSchedule()})">添加类别档位</a-button>
        <p v-else-if="!salesCategories.length && !config.categories.length" class="tier-note">暂无可选销售类别。</p>
      </section>
      <section v-else-if="activeGroup==='company'" class="tier-group" aria-label="公司特例">
        <div v-for="(company,index) in config.companies" :key="index" class="tier-range">
          <div class="tier-values">
            <a-form-item :html-for="'tier-company-'+index" label="销售公司" required>
              <a-select :id="'tier-company-'+index" v-model:value="company.companyIds" :options="eligibleCompanies(company)"
                mode="multiple" :max-tag-count="3" allow-clear option-filter-prop="label"/>
            </a-form-item>
            <a-form-item v-if="metricMode==='legacy'" :html-for="'tier-company-metric-'+index" label="公司阶梯指标">
              <a-select :id="'tier-company-metric-'+index" v-model:value="company.metric" :options="metrics"/>
            </a-form-item>
          </div>
          <PracticeTierRanges :config="company" :sections="sectionsFor(company.metric)" :amounts="amounts"
            :include-retrain="config.includeRetrain" :prefix="'company-'+index+'-'"/>
          <a-button type="link" danger size="small" @click="removeSchedule(config.companies,index)">移除公司档位</a-button>
        </div>
        <a-button v-if="canAddCompany" type="dashed" size="small" @click="config.companies.push({companyIds:[],metric:config.metric,...newSchedule()})">添加公司档位</a-button>
        <p v-else-if="!companies.length" class="tier-note">暂无可选销售公司。</p>
      </section>
      <section v-else-if="activeGroup==='person'" class="tier-group" aria-label="个人特例">
        <div v-for="(person,index) in config.people" :key="index" class="tier-range">
          <div class="tier-values">
            <a-form-item :html-for="'tier-seller-'+index" label="销售人员" required>
              <a-select :id="'tier-seller-'+index" v-model:value="person.personId"
                :options="eligiblePeople(config.people,person)" show-search option-filter-prop="label"/>
            </a-form-item>
            <a-form-item v-if="metricMode==='legacy'" :html-for="'tier-seller-metric-'+index" label="个人阶梯指标">
              <a-select :id="'tier-seller-metric-'+index" v-model:value="person.metric" :options="metrics"/>
            </a-form-item>
          </div>
          <PracticeTierRanges :config="person" :sections="sectionsFor(person.metric)" :amounts="amounts"
            :include-retrain="config.includeRetrain" :prefix="'seller-'+index+'-'"/>
          <a-button type="link" danger size="small" @click="removeSchedule(config.people,index)">移除个人档位</a-button>
        </div>
        <a-button v-if="config.people.length<people.length" type="dashed" size="small"
          @click="config.people.push({metric:config.metric,...newSchedule()})">添加个人档位</a-button>
        <p v-else-if="!people.length" class="tier-note">暂无可选销售人员。</p>
      </section>
      <section v-else class="tier-group" aria-label="通用档位">
        <div class="tier-settings">
          <a-checkbox :checked="!config.onlyOverrides" @change="config.onlyOverrides = !$event.target.checked">未命中特例或类别时使用</a-checkbox>
          <a-form-item v-if="!config.onlyOverrides && metricMode==='legacy'" html-for="tier-metric" label="通用阶梯指标">
            <a-select id="tier-metric" v-model:value="config.metric" :options="metrics"/>
          </a-form-item>
        </div>
        <PracticeTierRanges v-if="!config.onlyOverrides" :config="config" :sections="sections" :amounts="amounts" :include-retrain="config.includeRetrain"/>
      </section>
    </RuleCommissionStandards>
  </div>
</template>

<style scoped>
.tier-editor { padding:8px 10px; margin:4px 0 8px; background:#f3f6fa; border:1px solid var(--line); border-radius:6px }
.tier-toolbar,.tier-settings { display:flex; flex-wrap:wrap; align-items:end; gap:4px 12px }
.tier-toolbar,.tier-group > .tier-settings { margin-bottom:6px }
.tier-settings :deep(.ant-form-item) { width:min(100%,230px); margin:0 }
.tier-settings :deep(.ant-checkbox-wrapper) { margin-block:3px }
.tier-bounds { margin-bottom:5px }
.tier-range { padding:6px 8px; margin-bottom:6px; background:white; border:1px solid var(--line); border-radius:5px }
.tier-values { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,235px),1fr)); gap:0 8px }
.tier-values :deep(.ant-form-item) { min-width:0 }
.tier-range > .ant-btn { margin-top:4px; padding-inline:0 }
</style>

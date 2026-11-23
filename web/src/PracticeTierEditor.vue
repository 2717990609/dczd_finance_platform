<script setup>
import { computed } from 'vue'
import PracticeTierRanges from './PracticeTierRanges.vue'
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
  ...(props.config.hasLegacyMetrics ? [{value:'legacy',label:'现有档位指标'}] : []),
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
    <div class="tier-settings">
      <a-form-item label="阶梯指标" html-for="tier-amount-metric">
        <a-select id="tier-amount-metric" :value="metricMode" :options="metricOptions" @change="setMetric"/>
      </a-form-item>
      <a-checkbox v-if="showLegacyRetrain" :checked="config.includeRetrain" :disabled="retrainUnavailable && !config.includeRetrain"
        @change="setIncludeRetrain($event.target.checked)">使用原有复训档值</a-checkbox>
    </div>
    <p class="tier-note">匹配顺序　个人特例 → 公司特例 → 销售类别 → 通用档位</p>
    <details v-if="salesCategories.length || config.categories.length" class="tier-person-starts"
      :open="config.categories.length>0 || config.onlyOverrides && !config.companies.length && !config.people.length">
      <summary>销售类别默认档位<small v-if="assignedCategories.size"> · {{ assignedCategories.size }} 类</small></summary>
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
        <a-button type="link" danger @click="removeSchedule(config.categories,index)">移除类别档位</a-button>
      </div>
      <a-button v-if="canAddCategory" type="dashed" block
        @click="config.categories.push({salesCategoryIds:[],metric:config.metric,...newSchedule()})">添加类别档位</a-button>
    </details>
    <details class="tier-person-starts" :open="config.companies.length>0 && !config.categories.length">
      <summary>公司特例<small v-if="assignedCompanies.size"> · {{ assignedCompanies.size }} 家</small></summary>
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
        <a-button type="link" danger @click="removeSchedule(config.companies,index)">移除公司档位</a-button>
      </div>
      <a-button v-if="canAddCompany" type="dashed" block @click="config.companies.push({companyIds:[],metric:config.metric,...newSchedule()})">添加公司档位</a-button>
      <p v-else-if="!companies.length" class="tier-note">暂无可选销售公司。</p>
    </details>
    <details class="tier-person-starts" :open="config.people.length>0 && !config.categories.length && !config.companies.length">
      <summary>个人特例<small v-if="assignedPeople"> · {{ assignedPeople }} 人</small></summary>
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
        <a-button type="link" danger @click="removeSchedule(config.people,index)">移除个人档位</a-button>
      </div>
      <a-button v-if="config.people.length<people.length" type="dashed" block
        @click="config.people.push({metric:config.metric,...newSchedule()})">添加个人档位</a-button>
      <p v-else-if="!people.length" class="tier-note">暂无可选销售人员。</p>
    </details>
    <details class="tier-person-starts" :open="!config.onlyOverrides">
      <summary>通用档位</summary>
      <a-checkbox :checked="!config.onlyOverrides" @change="config.onlyOverrides = !$event.target.checked">未命中特例或类别时使用</a-checkbox>
      <a-form-item v-if="!config.onlyOverrides && metricMode==='legacy'" html-for="tier-metric" label="通用阶梯指标">
        <a-select id="tier-metric" v-model:value="config.metric" :options="metrics"/>
      </a-form-item>
      <PracticeTierRanges v-if="!config.onlyOverrides" :config="config" :sections="sections" :amounts="amounts" :include-retrain="config.includeRetrain"/>
    </details>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import PracticeTierRanges from './PracticeTierRanges.vue'
import { tierFormHasCount } from './rule-component-form'

const props = defineProps({
  config: { type: Object, required: true },
  metrics: { type: Array, required: true },
  startModes: { type: Array, required: true },
  amounts: { type: Array, required: true },
  people: { type: Array, required: true },
  openings: { type: Array, required: true },
  companies: { type: Array, required: true },
  retrainUnavailable: Boolean,
})
const personalStartModes = computed(() => props.startModes.filter(option =>
  !option.scope?.countScopes || option.scope.countScopes.includes('person')))
function sectionsFor(metric) {
  const result = []
  if (metric !== 'performance') result.push({ key: 'count', title: '累计人数档', unit: '人', decimals: 0 })
  if (metric !== 'count') result.push({ key: 'performance', title: '本期实付档', unit: '元', decimals: 2 })
  return result
}
const sections = computed(() => sectionsFor(props.config.metric))
function eligiblePeople(rows, row) {
  return props.people.filter(person =>
    !rows.some(other => other !== row && other.personId === person.value))
}
function eligibleCompanies(row) {
  return props.companies.filter(company =>
    !props.config.companies.some(other => other !== row && other.companyId === company.value))
}
function newSchedule() {
  const mode = props.amounts[0]?.value
  const payout = () => props.config.includeRetrain ? {mode,retrain:{mode}} : {mode}
  return {count:[payout()],performance:[{from:0,...payout()}]}
}
function setIncludeRetrain(checked) {
  props.config.includeRetrain = checked
  if (!checked) return
  for (const schedule of [props.config,...props.config.companies,...props.config.people]) {
    for (const row of [...schedule.count,...schedule.performance]) row.retrain ??= {mode:props.amounts[0]?.value}
  }
}
function removeSchedule(schedules, index) {
  schedules.splice(index,1)
  if (!props.config.companies.length && !props.config.people.length) props.config.onlyOverrides = false
}
</script>

<template>
  <div class="tier-editor">
    <a-form-item html-for="tier-metric" label="阶梯指标">
      <a-select id="tier-metric" v-model:value="config.metric" :options="metrics"/>
    </a-form-item>
    <a-checkbox :checked="config.includeRetrain" :disabled="retrainUnavailable && !config.includeRetrain"
      @change="setIncludeRetrain($event.target.checked)">复训共用此阶梯</a-checkbox>
    <p v-if="retrainUnavailable" class="tier-note">移除单独的复训销售安排后可启用。</p>
    <template v-if="tierFormHasCount(config)">
      <a-form-item html-for="tier-start-mode" label="默认累计起点">
        <a-select id="tier-start-mode" v-model:value="config.startMode" :options="startModes"/>
      </a-form-item>
      <a-form-item v-if="config.startMode==='ordinal'" html-for="tier-start-ordinal" label="起始期数" required>
        <a-input-number id="tier-start-ordinal" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
      </a-form-item>
      <a-form-item v-else-if="config.startMode==='opening'" html-for="tier-opening" label="冻结期初"
        :extra="openings.length ? '从冻结截止时刻之后开始追加新报。' : '当前产品尚未登记累计期初。'" required>
        <a-select id="tier-opening" v-model:value="config.openingId" :options="openings" />
      </a-form-item>
      <a-form-item v-else html-for="tier-start-time" label="起始开班时间" extra="使用北京时间。" required>
        <a-input id="tier-start-time" v-model:value="config.countFromAt" type="datetime-local" step="any"/>
      </a-form-item>
      <details class="tier-person-starts">
        <summary>按人员设置累计起点</summary>
        <p class="tier-note">指定起点到达后，该人员按自己的起点累计。</p>
        <div v-for="(row,index) in config.personCountStarts" :key="index" class="tier-range">
          <a-form-item :html-for="'tier-person-'+index" label="人员" required>
            <a-select :id="'tier-person-'+index" v-model:value="row.personId" :options="eligiblePeople(config.personCountStarts,row)" show-search option-filter-prop="label"/>
          </a-form-item>
          <a-form-item :html-for="'tier-person-start-mode-'+index" label="个人累计起点">
            <a-select :id="'tier-person-start-mode-'+index" v-model:value="row.startMode" :options="personalStartModes"/>
          </a-form-item>
          <a-form-item v-if="row.startMode==='ordinal'" :html-for="'tier-person-start-ordinal-'+index" label="个人起始期数" required>
            <a-input-number :id="'tier-person-start-ordinal-'+index" v-model:value="row.countFromOrdinal" :min="1" :precision="0"/>
          </a-form-item>
          <a-form-item v-else :html-for="'tier-person-start-time-'+index" label="个人起始开班时间" extra="使用北京时间。" required>
            <a-input :id="'tier-person-start-time-'+index" v-model:value="row.countFromAt" type="datetime-local" step="any"/>
          </a-form-item>
          <a-button type="link" danger @click="config.personCountStarts.splice(index,1)">移除个人起点</a-button>
        </div>
        <a-button type="dashed" block @click="config.personCountStarts.push({startMode:personalStartModes[0]?.value})">添加个人起点</a-button>
      </details>
    </template>
    <a-checkbox v-if="config.companies.length || config.people.length" v-model:checked="config.onlyOverrides">仅使用指定公司或个人的档位</a-checkbox>
    <PracticeTierRanges v-if="!config.onlyOverrides" :config="config" :sections="sections" :amounts="amounts" :include-retrain="config.includeRetrain"/>
    <details class="tier-person-starts">
      <summary>按销售公司配置档位</summary>
      <p class="tier-note">按原付款中的销售公司选档，同一人的累计在所选公司间合并。</p>
      <div v-for="(company,index) in config.companies" :key="index" class="tier-range">
        <a-form-item :html-for="'tier-company-'+index" label="销售公司" required>
          <a-select :id="'tier-company-'+index" v-model:value="company.companyId" :options="eligibleCompanies(company)" show-search option-filter-prop="label"/>
        </a-form-item>
        <PracticeTierRanges :config="company" :sections="sections" :amounts="amounts"
          :include-retrain="config.includeRetrain" :prefix="'company-'+index+'-'"/>
        <a-button type="link" danger @click="removeSchedule(config.companies,index)">移除公司档位</a-button>
      </div>
      <a-button v-if="config.companies.length<companies.length" type="dashed" block @click="config.companies.push(newSchedule())">添加公司档位</a-button>
      <p v-else-if="!companies.length" class="tier-note">暂无可选销售公司。</p>
    </details>
    <details class="tier-person-starts">
      <summary>按销售人员配置档位</summary>
      <p class="tier-note">个人档位优先于公司与通用档位。</p>
      <div v-for="(person,index) in config.people" :key="index" class="tier-range">
        <div class="tier-values">
          <a-form-item :html-for="'tier-seller-'+index" label="销售人员" required>
            <a-select :id="'tier-seller-'+index" v-model:value="person.personId"
              :options="eligiblePeople(config.people,person)" show-search option-filter-prop="label"/>
          </a-form-item>
          <a-form-item :html-for="'tier-seller-metric-'+index" label="个人阶梯指标">
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
  </div>
</template>

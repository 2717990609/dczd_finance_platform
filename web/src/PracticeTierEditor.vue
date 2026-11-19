<script setup>
import { computed } from 'vue'
import PracticeTierRanges from './PracticeTierRanges.vue'

const props = defineProps({
  config: { type: Object, required: true },
  metrics: { type: Array, required: true },
  startModes: { type: Array, required: true },
  amounts: { type: Array, required: true },
  people: { type: Array, required: true },
  openings: { type: Array, required: true },
  companies: { type: Array, required: true },
})
const personalStartModes = computed(() => props.startModes.filter(option =>
  !option.scope?.countScopes || option.scope.countScopes.includes('person')))
const sections = computed(() => {
  const result = []
  if (props.config.metric !== 'performance') result.push({ key: 'count', title: '累计人数档', unit: '人', decimals: 0 })
  if (props.config.metric !== 'count') result.push({ key: 'performance', title: '本期实付档', unit: '元', decimals: 2 })
  return result
})
function eligiblePeople(row) {
  return props.people.filter(person =>
    !props.config.personCountStarts.some(other => other !== row && other.personId === person.value))
}
function eligibleCompanies(row) {
  return props.companies.filter(company =>
    !props.config.companies.some(other => other !== row && other.companyId === company.value))
}
function addCompany() {
  const mode = props.amounts[0]?.value
  props.config.companies.push({count:[{mode}],performance:[{from:0,mode}]})
}
function removeCompany(index) {
  props.config.companies.splice(index,1)
  if (!props.config.companies.length) props.config.onlyCompanies = false
}
</script>

<template>
  <div class="tier-editor">
    <a-form-item html-for="tier-metric" label="阶梯指标">
      <a-select id="tier-metric" v-model:value="config.metric" :options="metrics"/>
    </a-form-item>
    <template v-if="config.metric !== 'performance'">
      <a-form-item html-for="tier-start-mode" label="默认累计起点">
        <a-select id="tier-start-mode" v-model:value="config.startMode" :options="startModes"/>
      </a-form-item>
      <a-form-item v-if="config.startMode==='ordinal'" html-for="tier-start-ordinal" label="起始期数" required>
        <a-input-number id="tier-start-ordinal" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
      </a-form-item>
      <a-form-item v-else-if="config.startMode==='opening'" html-for="tier-opening" label="冻结期初"
        :extra="openings.length ? '从冻结截止时刻之后开始追加新报。' : '当前产品暂无导入的冻结期初。'" required>
        <a-select id="tier-opening" v-model:value="config.openingId" :options="openings" />
      </a-form-item>
      <a-form-item v-else html-for="tier-start-time" label="起始开班时间" extra="使用北京时间。" required>
        <a-input id="tier-start-time" v-model:value="config.countFromAt" type="datetime-local"/>
      </a-form-item>
      <details class="tier-person-starts">
        <summary>按人员设置累计起点</summary>
        <p class="tier-note">指定起点到达后，该人员按自己的起点累计。</p>
        <div v-for="(row,index) in config.personCountStarts" :key="index" class="tier-range">
          <a-form-item :html-for="'tier-person-'+index" label="人员" required>
            <a-select :id="'tier-person-'+index" v-model:value="row.personId" :options="eligiblePeople(row)" show-search option-filter-prop="label"/>
          </a-form-item>
          <a-form-item :html-for="'tier-person-start-mode-'+index" label="个人累计起点">
            <a-select :id="'tier-person-start-mode-'+index" v-model:value="row.startMode" :options="personalStartModes"/>
          </a-form-item>
          <a-form-item v-if="row.startMode==='ordinal'" :html-for="'tier-person-start-ordinal-'+index" label="个人起始期数" required>
            <a-input-number :id="'tier-person-start-ordinal-'+index" v-model:value="row.countFromOrdinal" :min="1" :precision="0"/>
          </a-form-item>
          <a-form-item v-else :html-for="'tier-person-start-time-'+index" label="个人起始开班时间" extra="使用北京时间。" required>
            <a-input :id="'tier-person-start-time-'+index" v-model:value="row.countFromAt" type="datetime-local"/>
          </a-form-item>
          <a-button type="link" danger @click="config.personCountStarts.splice(index,1)">移除个人起点</a-button>
        </div>
        <a-button type="dashed" block @click="config.personCountStarts.push({startMode:personalStartModes[0]?.value})">添加个人起点</a-button>
      </details>
    </template>
    <PracticeTierRanges v-if="!config.onlyCompanies" :config="config" :sections="sections" :amounts="amounts"/>
    <details class="tier-person-starts">
      <summary>按销售公司配置档位</summary>
      <p class="tier-note">按原付款中的销售公司选档，同一人的累计在所选公司间合并。</p>
      <label class="tier-note">
        <input v-model="config.onlyCompanies" type="checkbox"> 仅计算已配置公司
      </label>
      <div v-for="(company,index) in config.companies" :key="index" class="tier-range">
        <a-form-item :html-for="'tier-company-'+index" label="销售公司" required>
          <a-select :id="'tier-company-'+index" v-model:value="company.companyId" :options="eligibleCompanies(company)" show-search option-filter-prop="label"/>
        </a-form-item>
        <PracticeTierRanges :config="company" :sections="sections" :amounts="amounts" :prefix="'company-'+index+'-'"/>
        <a-button type="link" danger @click="removeCompany(index)">移除公司档位</a-button>
      </div>
      <a-button v-if="config.companies.length<companies.length" type="dashed" block @click="addCompany">添加公司档位</a-button>
      <p v-else-if="!companies.length" class="tier-note">暂无可选销售公司。</p>
    </details>
  </div>
</template>

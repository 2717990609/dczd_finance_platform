<script setup>
import { computed, watch } from 'vue'

const props = defineProps({component:Object,index:Number,companies:Array,amounts:Array,sourceRoles:Array})
const categories = computed(() => props.component.companySales.categories)
const internalOptions = computed(() => props.companies.filter(option=>!categories.value.some(c=>c.companies.includes(option.value))))
const amounts = computed(() => props.amounts.filter(option =>
  props.component.classMember==='retrain' || option.value==='rate'))
const field = name => 'practice-company-'+props.index+'-'+name
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())

function availableCompanies(category) {
  return props.companies.filter(option =>
    !props.component.companySales.internalCompanies.includes(option.value) &&
    !categories.value.some(row=>row!==category && row.companies.includes(option.value)))
}
function selectCompanies(category, companies) {
  category.companies = companies
  category.overrides = category.overrides.filter(row=>companies.includes(row.companyId))
}
function overrideOptions(category, override) {
  return props.companies.filter(option=>category.companies.includes(option.value) &&
    !category.overrides.some(row=>row!==override && row.companyId===option.value))
}
function modeChanged(item, mode) {
  item.mode = mode
  item.value = undefined
}
watch(amounts, choices => {
  for (const category of categories.value) {
    for (const row of [category,...category.overrides]) {
      if (!choices.some(choice=>choice.value===row.mode)) modeChanged(row,choices[0]?.value)
    }
  }
})
</script>

<template>
  <a-form-item label="销售来源角色" :html-for="field('role')" required>
    <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
  </a-form-item>
  <a-form-item label="适用个人销售规则的内部公司" :html-for="field('internal')"
    extra="新报和复训共用。销售公司归类不明确时，订单列为待核。">
    <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
      mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <section v-for="(category,i) in categories" :key="i" class="company-category">
    <div class="company-category-title">
      <strong>公司类别</strong>
      <a-button v-if="categories.length>1" type="link" danger @click="categories.splice(i,1)">移除此类别</a-button>
    </div>
    <a-form-item label="类别名称" :html-for="field('category-'+i)" required>
      <a-input :id="field('category-'+i)" v-model:value="category.label" :maxlength="80"/>
    </a-form-item>
    <a-form-item label="适用公司" :html-for="field('companies-'+i)" required>
      <a-select :id="field('companies-'+i)" :value="category.companies" :options="availableCompanies(category)"
        mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" @change="selectCompanies(category,$event)"/>
    </a-form-item>
    <div class="company-value">
      <a-form-item label="类别计算方式" :html-for="field('method-'+i)">
        <a-select :id="field('method-'+i)" :value="category.mode" :options="amounts" @change="modeChanged(category,$event)"/>
      </a-form-item>
      <a-form-item :label="category.mode==='rate' ? '类别比例（%）' : '类别金额（元）'" :html-for="field('value-'+i)" required>
        <a-input-number :id="field('value-'+i)" v-model:value="category.value" :min="0"
          :max="category.mode==='rate'?100:undefined" :precision="category.mode==='rate'?4:2"/>
      </a-form-item>
    </div>
    <a-collapse v-if="category.companies.length" ghost>
      <a-collapse-panel key="overrides" header="公司单独标准">
        <div class="company-override" v-for="(override,j) in category.overrides" :key="j">
          <a-form-item label="公司" :html-for="field('override-'+i+'-'+j)" required>
            <a-select :id="field('override-'+i+'-'+j)" v-model:value="override.companyId"
              :options="overrideOptions(category,override)" show-search :filter-option="byName"/>
          </a-form-item>
          <div class="company-value">
            <a-form-item label="计算方式" :html-for="field('override-method-'+i+'-'+j)">
              <a-select :id="field('override-method-'+i+'-'+j)" :value="override.mode" :options="amounts" @change="modeChanged(override,$event)"/>
            </a-form-item>
            <a-form-item :label="override.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="field('override-value-'+i+'-'+j)" required>
              <a-input-number :id="field('override-value-'+i+'-'+j)" v-model:value="override.value" :min="0"
                :max="override.mode==='rate'?100:undefined" :precision="override.mode==='rate'?4:2"/>
            </a-form-item>
          </div>
          <a-button type="link" danger @click="category.overrides.splice(j,1)">移除单独标准</a-button>
        </div>
        <a-button v-if="category.overrides.length<category.companies.length" type="dashed" block
          @click="category.overrides.push({mode:category.mode})">添加单独标准</a-button>
      </a-collapse-panel>
    </a-collapse>
  </section>
  <a-button type="dashed" block @click="categories.push({mode:amounts[0]?.value,companies:[],overrides:[]})">添加公司类别</a-button>
</template>

<style scoped>
.company-category {border:1px solid var(--line);border-radius:6px;padding:12px;margin-bottom:12px}
.company-category-title {display:flex;align-items:center;justify-content:space-between;min-height:32px}
.company-value {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
.company-value :deep(.ant-input-number) {width:100%}
.company-override {border-bottom:1px solid var(--line);padding-bottom:8px;margin-bottom:10px}
</style>

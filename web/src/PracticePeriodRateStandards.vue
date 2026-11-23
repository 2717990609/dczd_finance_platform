<script setup>
import { computed } from 'vue'
import { practiceStandardRateGroups } from './rule-component-form'

defineProps({prefix:String,companies:Array,categories:Array})
const rows = defineModel({type:Array,required:true})
const groups = computed(() => practiceStandardRateGroups(rows.value))
const newStandard = {
  company:{label:'添加公司特例',field:'salesCompanies'},
  category:{label:'添加类别默认比例',field:'salesCategoryIds'},
  external:{label:'设置外部公司默认比例'},
  general:{label:'添加通用比例'},
}
function add(scope) {
  const field = newStandard[scope].field
  rows.value.push(field ? {scope,[field]:[]} : {scope})
}
</script>

<template>
  <section class="practice-company-criteria" :aria-labelledby="prefix+'-heading'">
    <header class="practice-company-scope-heading">
      <h4 :id="prefix+'-heading'">比例标准</h4>
      <small class="finance-rate-priority">匹配顺序 {{ groups.map(group=>group.label).join(' → ') }}</small>
    </header>
    <section v-for="{scope,label,items} in groups" :key="scope" :aria-label="label" class="practice-company-criterion"
      :class="{'fallback-standard':!newStandard[scope].field && items.length}">
      <header>
        <strong>{{ label }}</strong>
        <small v-if="!items.length">未配置</small>
        <a-button v-if="newStandard[scope].field || !items.length" size="small" @click="add(scope)">{{ newStandard[scope].label }}</a-button>
      </header>
      <div v-for="({row,index:i}) in items" :key="i" class="practice-standard-row" :class="{'fallback-rate':!newStandard[scope].field}">
        <a-form-item v-if="scope==='category'" label="销售类别" :html-for="prefix+'-category-'+i" required>
          <a-select :id="prefix+'-category-'+i" v-model:value="row.salesCategoryIds" :options="categories" mode="multiple"
            :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择适用类别"/>
        </a-form-item>
        <a-form-item v-else-if="scope==='company'" label="适用公司" :html-for="prefix+'-company-'+i" required>
          <a-select :id="prefix+'-company-'+i" v-model:value="row.salesCompanies" :options="companies" mode="multiple"
            :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择有例外的公司"/>
        </a-form-item>
        <a-form-item :label="newStandard[scope].field ? '比例（%）' : undefined" :html-for="prefix+'-rate-'+i" required>
          <a-input-number :id="prefix+'-rate-'+i" v-model:value="row.value" :min="0" :max="100" :precision="4"
            :aria-label="newStandard[scope].field ? undefined : '比例（%）'" :addon-after="newStandard[scope].field ? undefined : '%'"/>
        </a-form-item>
        <a-button type="link" danger size="small" @click="rows.splice(i,1)">移除</a-button>
      </div>
    </section>
  </section>
</template>

<style scoped>
.practice-standard-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(85px,128px) auto;align-items:end;gap:8px;margin:4px 0}
.fallback-standard {display:flex;flex-wrap:wrap;justify-content:space-between;align-items:center;gap:0 8px}
.practice-standard-row.fallback-rate {grid-template-columns:minmax(85px,128px) auto;margin-inline-start:auto}
.practice-standard-row :deep(.ant-form-item) {margin:0;min-width:0}
.practice-standard-row :deep(.ant-input-number) {width:100%}
</style>

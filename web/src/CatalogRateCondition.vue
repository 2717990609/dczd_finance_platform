<script setup>
import { computed } from 'vue'
import { catalogSalesPayees } from './rule-component-form'

const props = defineProps({options:Object, suffix:{type:String,default:''}, scope:String, salesPayee:Boolean})
const value = defineModel({type:Object,required:true})
const companies = computed(() => props.options.companies.filter(option=>option.value!=='ANY'))
const scope = computed(() => props.scope ?? value.value.scope)
const scopes = computed(() => [
  ...(props.options.categories.length || value.value.categoryIds?.length ? [{value:'category',label:'按销售类别'}] : []),
  {value:'company',label:'公司特例'},
  ...(props.salesPayee ? [{value:'external',label:'外部公司默认比例'}] : []),
  {value:'general',label:'不限公司和类别'},
])

function changeScope(next) {
  if (value.value.scope===next) return
  value.value.scope = next
  value.value.companyIds = []
  value.value.categoryIds = []
}
</script>

<template>
  <div class="field-grid catalog-rate-fields">
    <div v-if="value.scope && !props.scope">
      <label :for="'catalog-add-scope'+suffix">适用范围</label>
      <a-select :id="'catalog-add-scope'+suffix" :value="scope" :options="scopes" size="small" @update:value="changeScope"/>
    </div>
    <div v-if="scope==='company'" class="catalog-rate-selector">
      <label :for="'catalog-add-company'+suffix">特例公司</label>
      <a-select :id="'catalog-add-company'+suffix" v-model:value="value.companyIds"
        :options="companies" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label" size="small"
        :placeholder="scope==='company' ? '选择公司' : '不限公司'"/>
    </div>
    <div v-if="['company','category'].includes(scope) && (options.categories.length || value.categoryIds?.length)" class="catalog-rate-selector">
      <label :for="'catalog-add-category'+suffix">{{ scope==='company' ? '限销售类别（可选）' : '销售类别' }}</label>
      <a-select :id="'catalog-add-category'+suffix" v-model:value="value.categoryIds" :options="options.categories"
        :placeholder="scope==='category' ? '选择销售类别' : '不限销售类别'" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label" size="small"/>
    </div>
    <div v-if="scope!=='external'">
      <label :for="'catalog-add-relationship'+suffix">销售关系</label>
      <a-select :id="'catalog-add-relationship'+suffix" v-model:value="value.relationship" :options="options.relationships" size="small"/>
    </div>
    <div>
      <label :for="'catalog-add-rate'+suffix">实收比例（%）</label>
      <a-input-number :id="'catalog-add-rate'+suffix" v-model:value="value.rate" :min="0" :max="100" :precision="2" size="small" string-mode/>
    </div>
    <div v-if="salesPayee && scope!=='external'">
      <label :for="'catalog-add-payee'+suffix">收款方</label>
      <a-select :id="'catalog-add-payee'+suffix" :value="value.payee || 'seller'" :options="catalogSalesPayees" size="small"
        @update:value="value.payee=$event"/>
    </div>
    <slot/>
  </div>
</template>

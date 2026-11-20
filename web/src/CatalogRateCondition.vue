<script setup>
import { computed } from 'vue'
import { catalogSalesPayees } from './rule-component-form'

const props = defineProps({options:Object, suffix:{type:String,default:''}, scope:String, salesPayee:Boolean})
const value = defineModel({type:Object,required:true})
const companies = computed(() => props.options.companies.filter(option=>option.value!=='ANY'))
</script>

<template>
  <div class="field-grid catalog-rate-fields">
    <div>
      <label :for="'catalog-add-category'+suffix">销售类别</label>
      <a-select :id="'catalog-add-category'+suffix" v-model:value="value.categoryIds" :options="options.categories"
        :placeholder="scope==='category' ? '选择销售类别' : '不限销售类别'" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label"/>
    </div>
    <div v-if="scope!=='category'">
      <label :for="'catalog-add-company'+suffix">公司特例</label>
      <a-select :id="'catalog-add-company'+suffix" v-model:value="value.companyIds"
        :options="companies" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label"
        :placeholder="scope==='company' ? '选择特例公司' : '按类别适用'"/>
    </div>
    <div>
      <label :for="'catalog-add-relationship'+suffix">销售关系</label>
      <a-select :id="'catalog-add-relationship'+suffix" v-model:value="value.relationship" :options="options.relationships"/>
    </div>
    <div>
      <label :for="'catalog-add-rate'+suffix">新增比例（%）</label>
      <a-input-number :id="'catalog-add-rate'+suffix" v-model:value="value.rate" :min="0" :max="100" :precision="2" string-mode/>
    </div>
    <div v-if="salesPayee">
      <label :for="'catalog-add-payee'+suffix">收款方</label>
      <a-select :id="'catalog-add-payee'+suffix" :value="value.payee || 'seller'" :options="catalogSalesPayees"
        @update:value="value.payee=$event"/>
    </div>
  </div>
</template>

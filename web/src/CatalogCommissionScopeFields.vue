<script setup>
import { computed } from 'vue'

const props = defineProps({prefix:String,options:Array,unitNames:Object,disabled:Boolean,byRateTable:Boolean})
const units = defineModel({type:Array,default:null})
const modes = computed(() => [
  {value:'all',label:props.byRateTable ? '按下方比例条件匹配' : '本规则全部订单'},
  {value:'units',label:'指定销售组织'},
])
const availableUnits = computed(() => {
  const options = props.options || []
  const active = new Set(options.map(option=>option.value))
  return [...options,...(units.value || []).filter(id=>!active.has(id))
    .map(id=>({value:id,label:props.unitNames?.[id] || '已选组织名称待核'}))]
})
</script>

<template>
  <div class="commission-scope-fields">
    <a-form-item label="提成范围" :html-for="prefix+'-scope-type'">
      <a-select :id="prefix+'-scope-type'" :value="units ? 'units' : 'all'" :options="modes" :disabled="disabled"
        @change="units=$event==='all' ? null : []"/>
    </a-form-item>
    <a-form-item v-if="units" label="适用销售组织" :html-for="prefix+'-scope-units'" required>
      <a-select :id="prefix+'-scope-units'" v-model:value="units" :options="availableUnits" :disabled="disabled"
        mode="multiple" allow-clear :max-tag-count="3" show-search option-filter-prop="label" placeholder="选择公司、部门或小组"/>
    </a-form-item>
  </div>
</template>

<style scoped>
.commission-scope-fields {display:flex;flex-wrap:wrap;gap:0 10px}
.commission-scope-fields > :deep(.ant-form-item) {flex:0 1 210px;min-width:0;max-width:100%}
.commission-scope-fields > :deep(.ant-form-item:nth-child(2)) {flex:1 1 280px}
</style>

<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'

const props = defineProps({prefix:String,options:Array,unitNames:Object,disabled:Boolean,canOwn:Boolean})
const scope = defineModel({type:[Array,String],default:null})
const units = computed(() => Array.isArray(scope.value) ? scope.value : null)
const mode = computed(() => scope.value==='own' ? 'own' : units.value ? 'team' : 'all')
const availableUnits = computed(() => {
  const options = props.options || []
  const active = new Set(options.map(option=>option.value))
  return [...options,...(units.value || []).filter(id=>!active.has(id))
    .map(id=>({value:id,label:props.unitNames?.[id] || '已选组织名称待核'}))]
})
</script>

<template>
  <div class="commission-scope-fields">
    <RuleOrderScope :id="prefix+'-scope-type'" :model-value="mode" :disabled="disabled" :can-own="canOwn"
      @update:model-value="scope=$event==='team' ? [] : $event==='own' ? 'own' : null"/>
    <a-form-item v-if="units" label="适用销售组织" :html-for="prefix+'-scope-units'" required>
      <a-select :id="prefix+'-scope-units'" :value="units" @update:value="scope=$event" :options="availableUnits" :disabled="disabled"
        mode="multiple" allow-clear :max-tag-count="3" show-search option-filter-prop="label" placeholder="选择公司、部门或小组"/>
    </a-form-item>
  </div>
</template>

<style scoped>
.commission-scope-fields {display:flex;flex-wrap:wrap;gap:0 10px}
.commission-scope-fields > :deep(.ant-form-item) {flex:0 1 210px;min-width:0;max-width:100%}
.commission-scope-fields > :deep(.ant-form-item:nth-child(2)) {flex:1 1 280px}
</style>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  id:String,label:String,options:Array,legacyOptions:Array,placeholder:String,
  disabled:Boolean,required:Boolean,maxTagCount:{type:Number,default:2},
})
const values = defineModel({type:Array,default:()=>[]})
const legacyValues = defineModel('legacyValues',{type:Array,default:()=>[]})
const emit = defineEmits(['change'])
const legacyPrefix = 'source-company:'
const activeIDs = computed(() => new Set((props.options || []).map(option=>option.value)))
const sourceCompanies = computed(() => new Map((props.legacyOptions || []).map(option=>[option.value,option])))
const originalChoices = computed(() => (legacyValues.value || []).map(id=>{
  const company = sourceCompanies.value.get(id)
  const organization = company?.scope?.organizationId
  return activeIDs.value.has(organization) ? {value:organization} :
    {value:legacyPrefix+id,label:company?.label || '已选组织 · '+id}
}))
const selected = computed(() => [...new Set([...(values.value || []),...originalChoices.value.map(option=>option.value)])])
const choices = computed(() => [
  ...(props.options || []),
  ...originalChoices.value.filter(option=>!activeIDs.value.has(option.value)),
])
function select(next) {
  const ids = next.filter(id=>!String(id).startsWith(legacyPrefix))
  const unmapped = next.filter(id=>String(id).startsWith(legacyPrefix)).map(id=>id.slice(legacyPrefix.length))
  values.value = ids
  legacyValues.value = unmapped
  emit('change',{ids,legacyValues:unmapped})
}
</script>

<template>
  <a-form-item :label="label" :html-for="id" :required="required">
    <a-select :id="id" :value="selected" :options="choices" :placeholder="placeholder" :disabled="disabled"
      mode="multiple" :max-tag-count="maxTagCount" allow-clear show-search option-filter-prop="label" @change="select"/>
  </a-form-item>
</template>

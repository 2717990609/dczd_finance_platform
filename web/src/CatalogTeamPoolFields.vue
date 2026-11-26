<script setup>
import { computed } from 'vue'

const props = defineProps({prefix:String,pools:Array,disabled:Boolean,savedLabel:String,existing:Boolean})
const poolId = defineModel({type:Number})
const options = computed(() => {
  const choices = (props.pools || []).map(pool=>({value:pool.id,label:pool.name+' · '+pool.unitName}))
  if (poolId.value && !choices.some(pool=>pool.value===poolId.value)) {
    choices.unshift({value:poolId.value,label:props.savedLabel || '团队池 '+poolId.value})
  }
  if (props.existing) choices.unshift({value:0,label:props.savedLabel ? props.savedLabel+'（原归集）' : '原团队池'})
  return choices
})
</script>

<template>
  <a-form-item label="团队池" :html-for="prefix+'-pool'" required>
    <a-select :id="prefix+'-pool'" :value="poolId || (existing ? 0 : undefined)" :options="options" :disabled="disabled"
      show-search option-filter-prop="label" placeholder="选择已登记的团队池" @update:value="poolId=$event || undefined"/>
  </a-form-item>
</template>

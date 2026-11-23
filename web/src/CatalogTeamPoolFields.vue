<script setup>
import { computed, ref } from 'vue'

const props=defineProps({prefix:String, pools:Array, units:Array, productId:Number, api:Function,
  disabled:Boolean, savedLabel:String, existing:Boolean})
const emit=defineEmits(['created'])
const poolId=defineModel({type:Number})
const creating=ref(false)
const saving=ref(false)
const name=ref('')
const unitId=ref()
const error=ref('')
const options=computed(()=>{
  const choices=(props.pools || []).map(pool=>({value:pool.id,label:pool.name+' · '+pool.unitName}))
  if (poolId.value && !choices.some(pool=>pool.value===poolId.value)) {
    choices.unshift({value:poolId.value,label:props.savedLabel || '团队池 '+poolId.value})
  }
  if (props.existing) choices.unshift({value:0,label:props.savedLabel ? props.savedLabel+'（当前归集）' : '当前团队池'})
  return choices
})
async function createPool() {
  if (saving.value || !name.value.trim() || !unitId.value) return
  error.value=''
  saving.value=true
  try {
    const pool=await props.api('/team-pools',{method:'POST',body:JSON.stringify({productId:props.productId,name:name.value.trim(),unitId:unitId.value})})
    emit('created',pool)
    poolId.value=pool.id
    creating.value=false
    name.value=''
    unitId.value=undefined
  } catch (err) {
    error.value=err.message
  } finally {
    saving.value=false
  }
}
</script>

<template>
  <a-form-item label="团队池" :html-for="prefix+'-pool'" required>
    <a-select :id="prefix+'-pool'" :value="poolId || (existing ? 0 : undefined)" :options="options" :disabled="disabled || saving"
      show-search option-filter-prop="label" placeholder="选择团队池" @update:value="poolId=$event || undefined"/>
    <a-button v-if="!disabled" type="link" size="small" :disabled="saving" @click="creating=!creating">
      {{ creating ? '取消新增' : '新增团队池' }}
    </a-button>
  </a-form-item>
  <div v-if="creating" class="pool-create">
    <a-alert v-if="error" :message="error" type="error" show-icon/>
    <a-form-item label="团队池名称" :html-for="prefix+'-pool-name'" required>
      <a-input :id="prefix+'-pool-name'" v-model:value="name" :maxlength="80" :disabled="saving"/>
    </a-form-item>
    <a-form-item label="归集组织" :html-for="prefix+'-pool-unit'" required>
      <a-select :id="prefix+'-pool-unit'" v-model:value="unitId" :options="units" :disabled="saving" show-search option-filter-prop="label"/>
    </a-form-item>
    <a-button size="small" :disabled="!name.trim() || !unitId || disabled" :loading="saving" @click="createPool">保存团队池</a-button>
  </div>
</template>

<style scoped>
.pool-create {grid-column:1 / -1;padding:10px;border:1px solid var(--line);border-radius:6px}
</style>

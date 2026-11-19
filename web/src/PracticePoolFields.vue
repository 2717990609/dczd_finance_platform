<script setup>
import { ref } from 'vue'

const props = defineProps({ target:Object, prefix:String, pools:Array, people:Array, units:Array, createPool:Function })
const creating = ref(false)
const saving = ref(false)
const error = ref('')
const name = ref('')
const unit = ref()

async function addPool() {
  if (!name.value.trim() || !unit.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    props.target.poolId = await props.createPool(name.value.trim(), unit.value)
    creating.value = false
    name.value = ''
    unit.value = undefined
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-form-item label="资金池" :html-for="prefix+'-pool'" required>
    <a-select :id="prefix+'-pool'" v-model:value="target.poolId" :options="pools" show-search option-filter-prop="label"
      placeholder="选择资金池"/>
    <a-button type="link" @click="creating=!creating">{{ creating ? '取消新增' : '新增资金池' }}</a-button>
  </a-form-item>
  <div v-if="creating" class="pool-create">
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <a-form-item label="资金池名称" :html-for="prefix+'-pool-name'">
      <a-input :id="prefix+'-pool-name'" v-model:value="name" :maxlength="80"/>
    </a-form-item>
    <a-form-item label="归集组织" :html-for="prefix+'-pool-unit'">
      <a-select :id="prefix+'-pool-unit'" v-model:value="unit" :options="units" show-search option-filter-prop="label"/>
    </a-form-item>
    <a-button :disabled="!name.trim() || !unit" :loading="saving" @click="addPool">保存资金池</a-button>
  </div>
  <a-form-item label="分配负责人" :html-for="prefix+'-pool-owner'" required>
    <a-select :id="prefix+'-pool-owner'" v-model:value="target.ownerId" :options="people" show-search option-filter-prop="label"/>
  </a-form-item>
</template>

<style scoped>
.pool-create {margin-bottom:16px;padding:12px;border:1px solid var(--line);border-radius:6px}
</style>

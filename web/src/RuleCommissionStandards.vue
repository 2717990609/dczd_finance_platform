<script setup>
defineProps({groups:Array,priority:String,disabled:Boolean})
const active = defineModel({type:String})
const labels = {
  company:'按组织计提',companies:'按组织计提',category:'按销售类别计提',
  general:'通用标准',external:'已保存的兜底比例',inherited:'公司补充比例',
}
</script>

<template>
  <section class="commission-standards" aria-label="计提标准">
    <header class="commission-standards-header">
      <h4>计提标准</h4>
      <small v-if="priority" class="finance-rate-priority">匹配顺序 {{ priority }}</small>
      <div v-if="$slots.actions" class="commission-standards-actions"><slot name="actions"/></div>
    </header>
    <div v-if="groups?.length" class="commission-standards-toolbar">
      <a-radio-group v-model:value="active" :disabled="disabled" role="radiogroup" aria-label="查看计提标准">
        <a-radio v-for="group in groups" :key="group.value" :value="group.value">
          {{ labels[group.value] }}<small v-if="group.count" class="finance-rate-count">{{ group.count }}</small>
        </a-radio>
      </a-radio-group>
      <slot name="tools"/>
    </div>
    <slot/>
  </section>
</template>

<style scoped>
.commission-standards { display:grid; gap:5px; min-width:0; margin:5px 0 }
.commission-standards-header,.commission-standards-toolbar,.commission-standards-actions {
  display:flex; flex-wrap:wrap; align-items:center; gap:4px 10px; min-width:0;
}
.commission-standards-header h4 { margin:0; font:inherit; font-weight:600 }
.commission-standards-actions { margin-inline-start:auto }
.commission-standards-toolbar :deep(.ant-radio-group) { display:flex; flex-wrap:wrap; gap:3px }
.commission-standards :slotted(.commission-uniform-rate) { width:min(100%,200px); margin-bottom:0 }
</style>

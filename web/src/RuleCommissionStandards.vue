<script setup>
import { computed } from 'vue'

const props = defineProps({groups:Array,priority:{type:Array,default:()=>[]},disabled:Boolean,embedded:Boolean})
const active = defineModel({type:String})
const labels = {
  company:'按组织计提',companies:'按组织计提',category:'按销售类别计提',
  person:'个人特例',general:'通用标准',external:'已保存的兜底比例',inherited:'公司补充比例',
}
const hasScopeTabs = computed(() => props.groups?.some(group=>group.value==='category') &&
  props.groups.some(group=>['company','companies'].includes(group.value)))
const hasPersonPriority = computed(() => props.priority.some(item=>item.scope==='person'))
const fallbackOrder = computed(() => props.priority.filter(item=>item.scope!=='company').map(item=>item.label).join(' → '))
</script>

<template>
  <section class="commission-standards" :aria-label="embedded ? '编辑计提标准' : '计提标准'">
    <header v-if="!embedded || hasScopeTabs || priority.length>1" class="commission-standards-header">
      <h4 v-if="!embedded">计提标准</h4>
      <small v-if="hasScopeTabs" class="finance-rate-priority">
        <strong class="commission-standards-priority">{{ hasPersonPriority ? '组织特例优先于销售类别' : '组织特例优先' }}</strong>
        <span v-if="hasPersonPriority"> · {{ priority.map(item=>item.label).join(' → ') }}</span>
        <span v-else-if="fallbackOrder"> · 未命中按 {{ fallbackOrder }}</span>
        <span v-else> · 类别和组织可同时配置</span>
      </small>
      <small v-else-if="priority.length>1" class="finance-rate-priority">
        匹配顺序 {{ priority.map(item=>item.label).join(' → ') }}
      </small>
    </header>
    <a-tabs v-if="groups?.length>1" v-model:active-key="active" :animated="false" size="small" :tab-bar-gutter="16"
      class="commission-standards-tabs">
      <a-tab-pane v-for="group in groups" :key="group.value" :disabled="disabled">
        <template #tab>
          {{ labels[group.value] }}<small v-if="group.count" class="finance-rate-count">{{ group.count }}</small>
        </template>
        <template v-if="active===group.value">
          <div v-if="$slots.scope" class="commission-standard-scope"><slot name="scope"/></div>
          <slot/>
        </template>
      </a-tab-pane>
      <template v-if="$slots.tools" #rightExtra>
        <div class="commission-standards-toolbar"><slot name="tools"/></div>
      </template>
    </a-tabs>
    <template v-else>
      <div v-if="groups?.length && $slots.tools" class="commission-standards-toolbar"><slot name="tools"/></div>
      <div v-if="$slots.scope" class="commission-standard-scope"><slot name="scope"/></div>
      <slot/>
    </template>
  </section>
</template>

<style scoped>
.commission-standards { display:grid; gap:5px; min-width:0; margin:5px 0; container-type:inline-size }
.commission-standards-header,.commission-standards-toolbar {
  display:flex; flex-wrap:wrap; align-items:center; gap:4px 10px; min-width:0;
}
.commission-standards-header h4 { margin:0; font:inherit; font-weight:600 }
.commission-standards-priority { color:var(--accent); font-size:12px; font-weight:600 }
.commission-standards-toolbar { justify-content:flex-end; flex-wrap:nowrap }
.commission-standards-toolbar:empty { display:none }
.commission-standards-tabs :deep(.ant-tabs-nav) { margin-bottom:6px }
.commission-standards-tabs :deep(.ant-tabs-tab) { padding-block:6px }
.commission-standards-tabs :deep(.ant-tabs-extra-content) { margin-inline-start:12px }
.commission-standards-tabs :deep(.ant-tabs-tabpane-active) { display:grid; gap:5px; min-width:0 }
@container (max-width:700px) {
  .commission-standards-tabs :deep(.ant-tabs-nav) { flex-wrap:wrap }
  .commission-standards-tabs :deep(.ant-tabs-nav-wrap) { flex:1 1 100%; min-width:0 }
  .commission-standards-tabs :deep(.ant-tabs-extra-content) { max-width:100%; margin:4px 0 }
  .commission-standards-toolbar { justify-content:flex-start; flex-wrap:wrap }
}
@container (min-width:600px) {
  .commission-standard-scope:has(> :deep(.rule-scope-filters > .ant-form-item:only-child)) {
    grid-template-columns:repeat(3,minmax(0,1fr));
  }
  .commission-standard-scope > :deep(.rule-scope-filters:has(> .ant-form-item:only-child)) {
    display:contents;
  }
}
.commission-standard-scope { display:grid; gap:0 10px; grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr)); }
.commission-standard-scope :deep(.rule-scope-filters) { display:grid; grid-column:1 / -1; gap:0 10px; grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr)) }
.commission-standard-scope :deep(.rule-order-scope) { width:min(100%,380px) }
.commission-standards :slotted(.commission-uniform-rate) { width:min(100%,200px); margin-bottom:0 }
</style>

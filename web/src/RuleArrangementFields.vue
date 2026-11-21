<script setup>
import { useTemplateRef } from 'vue'

defineProps({nameId:String,disabled:Boolean})
const name = defineModel('name',{type:String})
const input = useTemplateRef('input')
defineExpose({focus:()=>input.value?.focus()})
</script>

<template>
  <div class="rule-basics">
    <a-form-item v-if="nameId" label="收款安排名称" :html-for="nameId" required>
      <a-input :id="nameId" ref="input" v-model:value="name" :disabled="disabled" :maxlength="80"/>
    </a-form-item>
    <slot/>
  </div>
</template>

<style scoped>
.rule-basics { grid-template-columns:repeat(auto-fit,minmax(min(100%,155px),1fr)) }
.rule-basics :deep(.ant-form-item) { min-width:0 }
.rule-basics :deep(.ant-input-number) { width:100% }
</style>

<script setup>
import { useTemplateRef } from 'vue'

defineProps({nameId:String,dutyId:String,duties:Array,orderTypeId:String,orderTypes:Array,disabled:Boolean})
const name = defineModel('name',{type:String})
const duty = defineModel('duty',{type:String})
const orderType = defineModel('orderType',{type:String})
const input = useTemplateRef('input')
defineExpose({focus:()=>input.value?.focus()})
</script>

<template>
  <div class="rule-basics">
    <a-form-item v-if="nameId" label="收款安排名称" :html-for="nameId" required>
      <a-input :id="nameId" ref="input" v-model:value="name" :disabled="disabled" :maxlength="80"/>
    </a-form-item>
    <slot/>
    <a-form-item v-if="dutyId" label="收款人职责" :html-for="dutyId">
      <a-select :id="dutyId" v-model:value="duty" :options="duties" :disabled="disabled"
        show-search allow-clear option-filter-prop="label" placeholder="选填"/>
    </a-form-item>
    <a-form-item v-if="orderTypeId" label="计提订单类型" :html-for="orderTypeId">
      <a-select :id="orderTypeId" v-model:value="orderType" :options="orderTypes" :disabled="disabled"/>
    </a-form-item>
    <slot name="calculation"/>
  </div>
</template>

<style scoped>
.rule-basics { grid-template-columns:repeat(auto-fit,minmax(min(100%,155px),1fr)) }
.rule-basics :deep(.ant-form-item) { min-width:0 }
.rule-basics :deep(.ant-input-number) { width:100% }
</style>

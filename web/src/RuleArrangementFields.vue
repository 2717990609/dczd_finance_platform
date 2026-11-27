<script setup>
import { useTemplateRef } from 'vue'

defineProps({nameId:String,nameLabel:{type:String,default:'收款安排名称'},orderTypeId:String,orderTypes:Array,
  calculationText:String,orderTypeText:String,scopeText:String,disabled:Boolean})
const name = defineModel('name',{type:String})
const orderType = defineModel('orderType',{type:String})
const input = useTemplateRef('input')
defineExpose({focus:()=>input.value?.focus()})
</script>

<template>
  <div class="rule-arrangement-fields">
    <div v-if="nameId || $slots.default" class="rule-basics">
      <a-form-item v-if="nameId" :label="nameLabel" :html-for="nameId" required>
        <a-input :id="nameId" ref="input" v-model:value="name" :disabled="disabled" :maxlength="80"/>
      </a-form-item>
      <slot/>
    </div>
    <div v-if="$slots.calculation || calculationText || $slots.orderType || orderTypeId || orderTypeText || scopeText || $slots.scope" class="rule-basics rule-arrangement-terms">
      <slot name="calculation"/>
      <a-form-item v-if="calculationText" label="计提方式"><span class="rule-field-value">{{ calculationText }}</span></a-form-item>
      <slot name="orderType">
        <a-form-item v-if="orderTypeId" label="计提订单类型" :html-for="orderTypeId">
          <a-select :id="orderTypeId" v-model:value="orderType" :options="orderTypes" :disabled="disabled"/>
        </a-form-item>
        <a-form-item v-else-if="orderTypeText" label="计提订单类型"><span class="rule-field-value">{{ orderTypeText }}</span></a-form-item>
      </slot>
      <a-form-item v-if="scopeText" label="计提订单范围"><span class="rule-field-value">{{ scopeText }}</span></a-form-item>
      <slot name="scope"/>
    </div>
    <slot name="standards"/>
    <slot name="advanced"/>
  </div>
</template>

<style scoped>
.rule-basics { grid-template-columns:repeat(auto-fit,minmax(min(100%,155px),1fr)) }
.rule-arrangement-terms { grid-template-columns:repeat(auto-fit,minmax(min(100%,175px),1fr)) }
.rule-arrangement-terms :deep(.rule-scope-filters) {
  grid-column:1 / -1;
  display:grid;
  grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr));
  gap:0 10px;
}
@media (min-width:800px) {
  .rule-arrangement-terms { grid-template-columns:repeat(3,minmax(0,1fr)) }
}
@media (min-width:1000px) {
  .rule-basics:not(.rule-arrangement-terms):not(:has(> :nth-child(5))) {
    grid-template-columns:repeat(4,minmax(0,1fr));
  }
}
.rule-basics :deep(.ant-form-item) { min-width:0 }
.rule-basics :deep(.catalog-readonly-arrangement) { grid-column:1 / -1 }
.rule-basics :deep(.ant-input-number) { width:100% }
.rule-field-value { display:block; padding:4px 0; overflow-wrap:anywhere }
</style>

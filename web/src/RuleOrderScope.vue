<script setup>
import { computed } from 'vue'

const props = defineProps({id:String,canOwn:Boolean,additional:Array,disabled:Boolean,teamLabel:String,allLabel:String})
const scope = defineModel({type:String,required:true})
const options = computed(() => [
  ...(props.canOwn ? [{value:'own',label:'本人销售'}] : []),
  {value:'team',label:props.teamLabel || '指定团队'},
  {value:'all',label:props.allLabel || '整个适用账期'},
  ...(props.additional || []),
])
</script>

<template>
  <a-form-item label="计提订单范围" :html-for="id" class="rule-order-scope">
    <a-select :id="id" v-model:value="scope" :options="options" :disabled="disabled"/>
  </a-form-item>
</template>

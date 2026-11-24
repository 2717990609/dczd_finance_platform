<script setup>
import { computed } from 'vue'

const props = defineProps({id:String,canOwn:Boolean,additional:Array,disabled:Boolean,teamOptions:Array})
const scope = defineModel({type:String,required:true})
const options = computed(() => [
  ...(props.canOwn ? [{value:'own',label:'本人销售'}] : []),
  ...(props.teamOptions ?? [{value:'team',label:'指定销售范围'}]),
  {value:'all',label:'全部适用订单'},
  ...(props.additional || []),
])
</script>

<template>
  <a-form-item label="计提订单范围" :html-for="id" class="rule-order-scope">
    <a-select :id="id" v-model:value="scope" :options="options" :disabled="disabled"/>
  </a-form-item>
</template>

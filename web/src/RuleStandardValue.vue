<script setup>
import {computed} from 'vue'

const props = defineProps({
  id:String, method:{type:String,default:'rate'}, label:String, max:Number,
  precision:{type:Number,default:2}, disabled:Boolean, stringMode:Boolean,
})
const value = defineModel({type:[Number,String]})
const label = computed(()=>props.label || (props.method==='rate' ? '比例（%）' : '金额（元）'))
const max = computed(()=>props.max ?? (props.method==='rate' ? 100 : undefined))
</script>

<template>
  <a-form-item class="commission-uniform-rate" :label="label" :html-for="id" required>
    <a-input-number :id="id" v-model:value="value" :min="0" :max="max" :precision="precision"
      :disabled="disabled" :string-mode="stringMode"/>
  </a-form-item>
</template>

<script setup>
import { catalogAppointmentLevels } from './rule-component-form'

defineProps({prefix:String,roles:Array,disabled:Boolean})
const level = defineModel('level')
const role = defineModel('role')
</script>

<template>
  <a-form-item label="从哪个组织取收款人" :html-for="prefix+'-appointment-level'" required>
    <a-select :id="prefix+'-appointment-level'" v-model:value="level" :options="catalogAppointmentLevels" :disabled="disabled"
      placeholder="选择公司、部门或小组"/>
  </a-form-item>
  <a-form-item label="收款岗位" :html-for="prefix+'-appointment-role'" required>
    <a-select :id="prefix+'-appointment-role'" v-model:value="role" :options="roles" :disabled="disabled"
      show-search option-filter-prop="label" placeholder="选择岗位"/>
  </a-form-item>
</template>

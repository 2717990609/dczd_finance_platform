<script setup>
import RuleScopeSelect from './RuleScopeSelect.vue'

defineProps({
  organizationId:String, categoryId:String, personId:String,
  organizations:Array, categories:Array, people:Array, sourceCompanies:Array, disabled:Boolean,
  organizationPlaceholder:{type:String,default:'选择公司、部门或小组'},
})
const organizationIds = defineModel('organizationIds',{type:Array})
const categoryIds = defineModel('categoryIds',{type:Array})
const personIds = defineModel('personIds',{type:Array})
const sourceCompanyIds = defineModel('sourceCompanyIds',{type:Array})
defineEmits(['organizations-change'])
</script>

<template>
  <RuleScopeSelect v-if="organizationId" :id="organizationId" v-model="organizationIds" v-model:legacy-values="sourceCompanyIds"
    label="适用销售组织" :options="organizations" :legacy-options="sourceCompanies" :disabled="disabled"
    :placeholder="organizationPlaceholder" @change="$emit('organizations-change',$event)"/>
  <RuleScopeSelect v-if="categoryId" :id="categoryId" v-model="categoryIds" label="适用销售类别"
    :options="categories" :disabled="disabled" placeholder="不限销售类别"/>
  <RuleScopeSelect v-if="personId" :id="personId" v-model="personIds" label="适用销售人员"
    :options="people" :disabled="disabled" :max-tag-count="3" placeholder="不限销售人员"/>
</template>

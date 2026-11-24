<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleScopeSelect from './RuleScopeSelect.vue'
import { catalogScopeOptions } from './rule-component-form'

const props = defineProps({prefix:String,options:Array,categories:Array,unitNames:Object,categoryNames:Object,disabled:Boolean,canOwn:Boolean,allLabel:String})
const scope = defineModel({type:[Object,String],default:null})
const mode = computed(() => scope.value==='own' ? 'own' : scope.value ? 'team' : 'all')
const units = computed(() => catalogScopeOptions(props.options || [],scope.value?.unitIds,props.unitNames,'已选组织名称待核'))
const categories = computed(() => catalogScopeOptions(props.categories || [],scope.value?.categoryIds,props.categoryNames,'已选类别名称待核'))
</script>

<template>
  <RuleOrderScope :id="prefix+'-scope-type'" :model-value="mode" :disabled="disabled" :can-own="canOwn" team-label="指定销售范围"
    :all-label="allLabel"
    @update:model-value="scope=$event==='team' ? {unitIds:[],categoryIds:[]} : $event==='own' ? 'own' : null"/>
  <div v-if="mode==='team'" class="rule-scope-filters">
    <RuleScopeSelect :id="prefix+'-scope-units'" label="适用销售组织" :model-value="scope.unitIds"
      :options="units" :disabled="disabled" placeholder="选择公司、部门或小组"
      @update:model-value="scope={...scope,unitIds:$event}"/>
    <RuleScopeSelect v-if="categories.length" :id="prefix+'-scope-categories'" label="适用销售类别" :model-value="scope.categoryIds"
      :options="categories" :disabled="disabled" placeholder="不限销售类别"
      @update:model-value="scope={...scope,categoryIds:$event}"/>
  </div>
</template>

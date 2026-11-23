<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'
import { catalogScopeOptions } from './rule-component-form'

const props = defineProps({prefix:String,options:Array,categories:Array,unitNames:Object,categoryNames:Object,disabled:Boolean,canOwn:Boolean})
const scope = defineModel({type:[Object,String],default:null})
const mode = computed(() => scope.value==='own' ? 'own' : scope.value ? 'team' : 'all')
const units = computed(() => catalogScopeOptions(props.options || [],scope.value?.unitIds,props.unitNames,'已选组织名称待核'))
const categories = computed(() => catalogScopeOptions(props.categories || [],scope.value?.categoryIds,props.categoryNames,'已选类别名称待核'))
</script>

<template>
  <RuleOrderScope :id="prefix+'-scope-type'" :model-value="mode" :disabled="disabled" :can-own="canOwn" team-label="指定销售范围"
    @update:model-value="scope=$event==='team' ? {unitIds:[],categoryIds:[]} : $event==='own' ? 'own' : null"/>
  <div v-if="mode==='team'" class="rule-scope-filters">
      <a-form-item label="适用销售组织" :html-for="prefix+'-scope-units'">
        <a-select :id="prefix+'-scope-units'" :value="scope.unitIds" @update:value="scope={...scope,unitIds:$event}" :options="units" :disabled="disabled"
          mode="multiple" allow-clear :max-tag-count="2" show-search option-filter-prop="label" placeholder="不限公司、部门或小组"/>
      </a-form-item>
      <a-form-item v-if="categories.length" label="适用销售类别" :html-for="prefix+'-scope-categories'">
        <a-select :id="prefix+'-scope-categories'" :value="scope.categoryIds" @update:value="scope={...scope,categoryIds:$event}" :options="categories"
          :disabled="disabled" mode="multiple" allow-clear :max-tag-count="2" show-search option-filter-prop="label" placeholder="不限销售类别"/>
      </a-form-item>
  </div>
</template>

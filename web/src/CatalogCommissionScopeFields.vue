<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleSalesScopeFields from './RuleSalesScopeFields.vue'
import { catalogScopeOptions } from './rule-component-form'

const props = defineProps({prefix:String,options:Array,categories:Array,unitNames:Object,categoryNames:Object,disabled:Boolean,canOwn:Boolean})
const scope = defineModel({type:[Object,String],default:null})
const mode = computed(() => scope.value==='own' ? 'own' : scope.value ? 'team' : 'all')
const units = computed(() => catalogScopeOptions(props.options || [],scope.value?.unitIds,props.unitNames,'已选组织名称待核'))
const categories = computed(() => catalogScopeOptions(props.categories || [],scope.value?.categoryIds,props.categoryNames,'已选类别名称待核'))
</script>

<template>
  <RuleOrderScope :id="prefix+'-scope-type'" :model-value="mode" :disabled="disabled" :can-own="canOwn"
    @update:model-value="scope=$event==='team' ? {unitIds:[],categoryIds:[]} : $event==='own' ? 'own' : null"/>
  <div v-if="mode==='team'" class="rule-scope-filters">
    <RuleSalesScopeFields :organization-id="prefix+'-scope-units'" :category-id="categories.length ? prefix+'-scope-categories' : undefined"
      :organizations="units" :categories="categories" :disabled="disabled"
      :organization-ids="scope.unitIds" :category-ids="scope.categoryIds"
      @update:organization-ids="scope={...scope,unitIds:$event}" @update:category-ids="scope={...scope,categoryIds:$event}"/>
  </div>
</template>

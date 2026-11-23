<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'

const props = defineProps({row:Object,index:Number,companies:Array,categories:Array,units:Array})
const flat = computed(() => props.row.mode!=='tiered' && !props.row.retrainSplits?.length)
const organizationOptions = computed(() => props.units.filter(unit=>unit.kind!=='COMPANY' || props.row.saleScope?.salesUnits?.includes(unit.value)))
const field = name => 'practice-personal-'+props.index+'-'+name
const mode = computed(() => {
  const row = props.row
  if (row.saleScope?.ownSales && row.salesRecipientKind==='person') return 'own'
  return row.restrictSaleScope || row.salesCompanies?.length || row.saleScope?.salesUnits?.length || row.saleScope?.salesCategoryIds?.length ? 'team' : 'all'
})

function changeScope(next) {
  const row = props.row
  row.restrictSaleScope = next==='team'
  row.saleScope = next==='own' ? {ownSales:true} : undefined
  if (next!=='team') row.salesCompanies = []
}

function updateFilter(key, value) {
  props.row.saleScope = {...props.row.saleScope,[key]:value}
}
</script>

<template>
  <RuleOrderScope :id="field('scope')" :model-value="mode" :can-own="flat && row.salesRecipientKind==='person'"
    team-label="指定销售范围" all-label="全班" @update:model-value="changeScope"/>
  <div v-if="mode==='team' || mode==='own' && row.salesCompanies?.length" class="rule-scope-filters">
    <a-form-item label="适用销售公司" :html-for="field('companies')">
      <a-select :id="field('companies')" v-model:value="row.salesCompanies" :options="companies" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="不限销售公司"/>
    </a-form-item>
    <a-form-item v-if="mode==='team' && flat && categories.length" label="适用销售类别" :html-for="field('categories')">
      <a-select :id="field('categories')" :value="row.saleScope?.salesCategoryIds" :options="categories" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="不限销售类别" @change="updateFilter('salesCategoryIds',$event)"/>
    </a-form-item>
    <a-form-item v-if="mode==='team' && flat" label="适用销售组织" :html-for="field('units')">
      <a-select :id="field('units')" :value="row.saleScope?.salesUnits" :options="organizationOptions" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="不限部门或小组" @change="updateFilter('salesUnits',$event)"/>
    </a-form-item>
  </div>
</template>

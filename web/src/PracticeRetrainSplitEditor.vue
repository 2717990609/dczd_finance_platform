<script setup>
import { computed } from 'vue'
import PracticeRetrainSplitFields from './PracticeRetrainSplitFields.vue'

const props = defineProps({ component:Object, index:Number, companies:Array, pools:Array, people:Array, units:Array, createPool:Function, formatMoney:Function })
const field = (name,index) => 'practice-retrain-'+props.index+'-'+index+'-'+name
const eligibleCompanies = computed(() => props.component.salesCompanies?.length
  ? props.companies.filter(company=>props.component.salesCompanies.includes(company.value))
  : props.companies)

function addSplit() {
  props.component.retrainSplits ||= []
  props.component.retrainSplits.push({personal:0,pool:{}})
}
</script>

<template>
  <details class="rule-block practice-count-starts">
    <summary>复训提成拆分{{ component.retrainSplits?.length ? ' · '+component.retrainSplits.length+' 家公司' : '' }}</summary>
    <div class="retrain-content">
      <section v-for="(split,i) in component.retrainSplits" :key="i" class="retrain-split">
        <a-form-item label="销售公司" :html-for="field('company',i)" required>
          <a-select :id="field('company',i)" v-model:value="split.companyId" :options="eligibleCompanies"
            show-search option-filter-prop="label"/>
        </a-form-item>
        <PracticeRetrainSplitFields :split="split" :prefix="'practice-retrain-'+index+'-'+i" :amount="component.amount"
          :pools="pools" :people="people" :units="units" :create-pool="createPool" :format-money="formatMoney"/>
        <a-button type="link" danger @click="component.retrainSplits.splice(i,1)">移除公司拆分</a-button>
      </section>
      <a-button block type="dashed" @click="addSplit">添加公司拆分</a-button>
    </div>
  </details>
</template>

<style scoped>
summary { color:var(--accent); cursor:pointer; }
.retrain-content { margin-top:10px; }
.retrain-split {margin-bottom:12px;padding:12px;border:1px solid var(--line);border-radius:6px}
</style>

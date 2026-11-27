<script setup>
import { computed } from 'vue'
import PracticeRetrainSplitFields from './PracticeRetrainSplitFields.vue'
import PracticeRetrainServiceEditor from './PracticeRetrainServiceEditor.vue'

const props = defineProps({ component:Object, index:[String,Number], companies:Array, pools:Array, people:Array, units:Array, formatMoney:Function, allowAdd:Boolean })
const emit = defineEmits(['add-company'])
const field = (name,index) => 'practice-retrain-'+props.index+'-'+index+'-'+name
const eligibleCompanies = computed(() => props.component.salesCompanies?.length
  ? props.companies.filter(company=>props.component.salesCompanies.includes(company.value))
  : props.companies)

function removeService(split) {
  Object.assign(split,{serviceEnabled:false,servicePersonId:undefined,serviceLabel:'',
    serviceBasis:'fixed',service:0,serviceScopeMode:'all',serviceSalesPeople:[],serviceSalesUnits:[]})
}

</script>

<template>
  <details class="rule-block practice-count-starts" :open="allowAdd || undefined">
    <summary>复训提成拆分{{ component.retrainSplits?.length ? ' · '+component.retrainSplits.length+' 家公司' : '' }}</summary>
    <div class="retrain-content">
      <section v-for="(split,i) in component.retrainSplits" :key="i" class="retrain-split">
        <a-form-item label="销售公司" :html-for="field('company',i)" required>
          <a-select :id="field('company',i)" v-model:value="split.companyId" :options="eligibleCompanies"
            show-search option-filter-prop="label"/>
        </a-form-item>
        <PracticeRetrainSplitFields :split="split" :prefix="'practice-retrain-'+index+'-'+i" :amount="component.mode==='fixed' ? component.amount : undefined"
          :pools="pools" :people="people" :units="units" :format-money="formatMoney"/>
        <template v-if="allowAdd">
          <PracticeRetrainServiceEditor v-if="split.serviceEnabled" :split="split" :index="index+'-'+i"
            :people="people" :sales-units="units" :total="component.amount" :format-money="formatMoney"
            @remove="removeService(split)"/>
          <a-button v-else type="link" @click="split.serviceEnabled=true">添加服务提成</a-button>
        </template>
        <a-button type="link" danger @click="component.retrainSplits.splice(i,1)">移除公司拆分</a-button>
      </section>
      <a-button v-if="allowAdd" @click="emit('add-company')">添加公司拆分</a-button>
    </div>
  </details>
</template>

<style scoped>
summary { color:var(--accent); cursor:pointer; }
.retrain-content { margin-top:10px; }
.retrain-split {margin-bottom:12px;padding:12px;border:1px solid var(--line);border-radius:6px}
</style>

<script setup>
import { computed } from 'vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import { moneyAsMinor, practiceRetrainShareMethods, practiceRetrainSplitAmounts, practiceRetrainSplitEstimate } from './rule-component-form'

const props = defineProps({split:Object,index:Number,people:Array,salesUnits:Array,total:Number,formatMoney:Function})
const emit = defineEmits(['remove'])
const field = name => 'practice-retrain-service-'+props.index+'-'+name
const estimate = computed(() => {
  try {
    return practiceRetrainSplitEstimate(practiceRetrainSplitAmounts(props.split),moneyAsMinor(props.total))
  } catch {
    return undefined
  }
})

function setBasis(basis) {
  props.split.serviceBasis = basis
  props.split.service = undefined
}
</script>

<template>
  <RuleArrangementFields :name-id="field('label')" v-model:name="split.serviceLabel" order-type-text="复训">
    <a-form-item label="收款对象" :html-for="field('person')" required>
      <a-select :id="field('person')" v-model:value="split.servicePersonId" :options="people"
        show-search option-filter-prop="label" placeholder="指定个人" size="small"/>
    </a-form-item>
    <template #calculation>
      <a-form-item label="计提方式" :html-for="field('basis')" required>
        <a-select :id="field('basis')" :value="split.serviceBasis" :options="practiceRetrainShareMethods"
          size="small" @update:value="setBasis"/>
      </a-form-item>
      <a-form-item :label="split.serviceBasis==='fixed' ? '金额（元）' : '比例（%）'" :html-for="field('value')" required>
        <a-input-number :id="field('value')" v-model:value="split.service" :min="0"
          :max="split.serviceBasis==='fixed' ? total : 100" :precision="split.serviceBasis==='fixed' ? 2 : 4" size="small"/>
      </a-form-item>
    </template>
    <template #scope>
      <a-form-item label="计提订单范围" :html-for="field('scope')">
        <a-select :id="field('scope')" v-model:value="split.serviceScopeMode" :options="[
          {value:'all',label:'该公司全部复训报名'},
          {value:'selected',label:'指定销售范围'},
        ]" size="small"/>
      </a-form-item>
      <div v-if="split.serviceScopeMode==='selected'" class="rule-scope-filters">
        <a-form-item label="适用销售人员" :html-for="field('salespeople')">
          <a-select :id="field('salespeople')" v-model:value="split.serviceSalesPeople" :options="people" mode="multiple"
            show-search option-filter-prop="label" allow-clear :max-tag-count="2" placeholder="不限人员" size="small"/>
        </a-form-item>
        <a-form-item label="适用团队或小组" :html-for="field('salesunits')">
          <a-select :id="field('salesunits')" v-model:value="split.serviceSalesUnits" :options="salesUnits" mode="multiple"
            show-search option-filter-prop="label" allow-clear :max-tag-count="2" placeholder="不限团队" size="small"/>
        </a-form-item>
      </div>
    </template>
  </RuleArrangementFields>
  <div class="retrain-service-footer">
    <p v-if="estimate" class="tier-note" role="status">
      {{ estimate.pool<0 ? '销售与服务份额合计超出复训总提成' :
        (split.serviceScopeMode==='selected' ? '整份报名命中时' : '每份报名')+' · 销售实得 '+formatMoney(estimate.sales)+' · 服务 '+formatMoney(estimate.service)+' · 团队池 '+formatMoney(estimate.pool) }}
    </p>
    <a-button type="link" size="small" danger @click="emit('remove')">移除此服务提成</a-button>
  </div>
</template>

<style scoped>
.retrain-service-footer {display:flex; flex-wrap:wrap; gap:4px 12px; justify-content:space-between; align-items:center}
.retrain-service-footer p {margin:0}
</style>

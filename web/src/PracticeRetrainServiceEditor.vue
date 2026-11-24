<script setup>
import { computed } from 'vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleSalesScopeFields from './RuleSalesScopeFields.vue'
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
    <a-form-item label="收款对象"><span class="rule-field-value">个人</span></a-form-item>
    <a-form-item label="指定收款人" :html-for="field('person')" required>
      <a-select :id="field('person')" v-model:value="split.servicePersonId" :options="people"
        show-search option-filter-prop="label" placeholder="指定个人" size="small"/>
    </a-form-item>
    <template #calculation>
      <a-form-item label="计提方式" :html-for="field('basis')" required>
        <a-select :id="field('basis')" :value="split.serviceBasis" :options="practiceRetrainShareMethods"
          size="small" @update:value="setBasis"/>
      </a-form-item>
    </template>
    <template #scope>
      <RuleOrderScope :id="field('scope')" :model-value="split.serviceScopeMode==='selected' ? 'team' : 'all'"
        @update:model-value="split.serviceScopeMode=$event==='team' ? 'selected' : 'all'"/>
      <div v-if="split.serviceScopeMode==='selected'" class="rule-scope-filters">
        <RuleSalesScopeFields :organization-id="field('salesunits')" :person-id="field('salespeople')"
          v-model:organization-ids="split.serviceSalesUnits" v-model:person-ids="split.serviceSalesPeople"
          :organizations="salesUnits" :people="people"/>
      </div>
    </template>
    <template #standards>
      <RuleCommissionStandards>
        <RuleStandardValue :id="field('value')" v-model="split.service" :method="split.serviceBasis"
          :max="split.serviceBasis==='fixed' ? total : 100" :precision="split.serviceBasis==='fixed' ? 2 : 4"/>
      </RuleCommissionStandards>
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

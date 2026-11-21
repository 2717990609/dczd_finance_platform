<script setup>
import { computed } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import { moneyAsMinor, practiceRetrainPoolRemainder, practiceRetrainShareMethods, practiceRetrainSplitAmounts } from './rule-component-form'

const props = defineProps({ split:Object, prefix:String, amount:Number, pools:Array, people:Array, units:Array, createPool:Function, formatMoney:Function })
const remainder = computed(() => {
  try {
    return practiceRetrainPoolRemainder(practiceRetrainSplitAmounts(props.split),moneyAsMinor(props.amount))
  } catch {
    return undefined
  }
})
const dynamic = computed(() => props.amount==null || props.split.salesBasis==='net_receipts' ||
  Boolean(props.split.servicePersonId && props.split.serviceBasis==='net_receipts'))
function setBasis(field, amount, basis) {
  props.split[field] = basis
  props.split[amount] = undefined
}
</script>

<template>
  <div class="practice-retrain-payees">
    <fieldset>
      <legend>销售分成</legend>
      <a-form-item label="销售计提方式" :html-for="prefix+'-sales-basis'" required>
        <a-select :id="prefix+'-sales-basis'" :value="split.salesBasis" :options="practiceRetrainShareMethods"
          @update:value="setBasis('salesBasis','personal',$event)"/>
      </a-form-item>
      <a-form-item :label="split.salesBasis==='fixed' ? '销售份额（元）' : '销售比例（%）'" :html-for="prefix+'-sales'" required>
        <a-input-number :id="prefix+'-sales'" v-model:value="split.personal" :min="0"
          :max="split.salesBasis==='fixed' ? amount : 100" :precision="split.salesBasis==='fixed' ? 2 : 4"/>
      </a-form-item>
    </fieldset>
    <fieldset>
      <legend>资金池分成</legend>
      <p v-if="remainder!=null" class="tier-note" role="status">
        {{ remainder<0 ? '已配置份额合计超出复训总提成' : '每份报名归入 '+formatMoney(remainder) }}
      </p>
      <p v-else-if="dynamic" class="tier-note">归入每份复训提成扣除已配置份额后的余额</p>
      <PracticePoolFields :target="split.pool" :prefix="prefix+'-split'" :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
    </fieldset>
  </div>
</template>

<style scoped>
.practice-retrain-payees { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,235px),1fr)); gap:10px; }
.practice-retrain-payees fieldset { border:1px solid var(--line); border-radius:6px; padding:8px 10px 4px; margin:0; min-width:0; }
.practice-retrain-payees legend { width:auto; margin:0; padding:0 4px; font-size:14px; font-weight:600; float:none; }
.practice-retrain-payees :deep(.ant-input-number) { width:100%; }
</style>

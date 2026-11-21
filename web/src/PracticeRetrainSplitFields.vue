<script setup>
import { computed } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import { moneyAsMinor } from './rule-component-form'

const props = defineProps({ split:Object, prefix:String, amount:Number, pools:Array, people:Array, units:Array, createPool:Function, formatMoney:Function })
const remainder = computed(() => {
  const sales = moneyAsMinor(props.split.personal)
  const service = props.split.servicePersonId ? moneyAsMinor(props.split.service) : 0
  if (sales != null && service != null && props.amount != null) {
    return moneyAsMinor(props.amount)-sales-service
  }
})
</script>

<template>
  <div class="practice-retrain-payees">
    <fieldset>
      <legend>销售分成</legend>
      <a-form-item label="销售份额（元）" :html-for="prefix+'-sales'" required>
        <a-input-number :id="prefix+'-sales'" v-model:value="split.personal" :min="0" :max="amount" :precision="2"/>
      </a-form-item>
    </fieldset>
    <fieldset>
      <legend>服务分成</legend>
      <a-form-item label="服务人员" :html-for="prefix+'-service'">
        <a-select :id="prefix+'-service'" v-model:value="split.servicePersonId" :options="people" allow-clear
          show-search option-filter-prop="label" placeholder="选择服务人员"/>
      </a-form-item>
      <a-form-item v-if="split.servicePersonId" label="服务份额（元）" :html-for="prefix+'-service-amount'" required>
        <a-input-number :id="prefix+'-service-amount'" v-model:value="split.service" :min="0" :max="amount" :precision="2"/>
      </a-form-item>
    </fieldset>
    <fieldset>
      <legend>资金池分成</legend>
      <p v-if="remainder!=null" class="tier-note" role="status">
        {{ remainder<0 ? '销售和服务份额合计超出固定提成' : '每份报名归入 '+formatMoney(remainder) }}
      </p>
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

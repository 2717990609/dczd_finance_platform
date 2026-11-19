<script setup>
import PracticePoolFields from './PracticePoolFields.vue'

const props = defineProps({ component:Object, index:Number, companies:Array, pools:Array, people:Array, units:Array, createPool:Function })
const field = (name,index) => 'practice-retrain-'+props.index+'-'+index+'-'+name

function addSplit() {
  props.component.retrainSplits ||= []
  props.component.retrainSplits.push({personal:0,pool:{}})
}
</script>

<template>
  <a-collapse ghost>
    <a-collapse-panel key="retrain" header="按公司拆分复训提成">
      <p class="draft-report-note">固定提成先分给销售和服务人员，余额归入资金池。确认名单前部分退款且报名仍有效时，保留固定提成。</p>
      <section v-for="(split,i) in component.retrainSplits" :key="i" class="retrain-split">
        <a-form-item label="销售公司" :html-for="field('company',i)" required>
          <a-select :id="field('company',i)" v-model:value="split.companyId" :options="companies"
            show-search option-filter-prop="label"/>
        </a-form-item>
        <a-form-item label="销售份额（元）" :html-for="field('sales',i)" required>
          <a-input-number :id="field('sales',i)" v-model:value="split.personal" :min="0" :max="component.amount" :precision="2"/>
        </a-form-item>
        <a-form-item label="服务人员" :html-for="field('service',i)">
          <a-select :id="field('service',i)" v-model:value="split.servicePersonId" :options="people" allow-clear
            show-search option-filter-prop="label"/>
        </a-form-item>
        <a-form-item v-if="split.servicePersonId" label="服务份额（元）" :html-for="field('service-amount',i)" required>
          <a-input-number :id="field('service-amount',i)" v-model:value="split.service" :min="0" :max="component.amount" :precision="2"/>
        </a-form-item>
        <PracticePoolFields :target="split.pool" :prefix="field('split',i)" :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
        <a-button type="link" danger @click="component.retrainSplits.splice(i,1)">移除公司拆分</a-button>
      </section>
      <a-button block type="dashed" @click="addSplit">添加公司拆分</a-button>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.retrain-split {margin-bottom:12px;padding:12px;border:1px solid var(--line);border-radius:6px}
</style>

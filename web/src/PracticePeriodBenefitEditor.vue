<script setup>
import { computed } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'

const props = defineProps({
  component:Object, index:Number, people:Array, units:Array, companies:Array,
  recipientTypes:Array, appointmentKinds:Array, sourceRoles:Array, pools:Array, createPool:Function,
})
const config = computed(() => props.component.periodBenefit)
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())
const field = name => 'practice-benefit-'+props.index+'-'+name
</script>

<template>
  <a-form-item label="分项名称" required>
    <a-input v-model:value="config.label" aria-label="分项名称" :maxlength="80"/>
  </a-form-item>
  <div class="period-fields">
    <a-form-item label="受益方类型" :html-for="field('recipient')">
      <a-select :id="field('recipient')" v-model:value="config.recipient" :options="recipientTypes"/>
    </a-form-item>
    <a-form-item v-if="config.recipient==='unit'" label="收款组织" :html-for="field('unit')" required>
      <a-select :id="field('unit')" v-model:value="config.unitId" :options="units" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="config.recipient==='person'" label="收款人员" :html-for="field('person')" required>
      <a-select :id="field('person')" v-model:value="config.personId" :options="people" show-search :filter-option="byName"/>
    </a-form-item>
  </div>
  <PracticePoolFields v-if="config.recipient==='pool'" :target="config.pool" :prefix="field('target')"
    :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
  <a-form-item>
    <a-checkbox v-model:checked="config.useTiers">按净实收阶梯选比例</a-checkbox>
  </a-form-item>
  <a-form-item v-if="!config.useTiers" label="班次计提比例（%）" required>
    <a-input-number v-model:value="component.amount" aria-label="班次计提比例（%）" :min="0" :max="100" :precision="4"/>
  </a-form-item>
  <template v-else>
    <a-form-item>
      <a-checkbox v-model:checked="config.tierOnWholeClass">用全班净实收选档</a-checkbox>
    </a-form-item>
    <div v-for="(tier,i) in config.tiers" :key="i" class="period-fields period-tier">
      <a-form-item label="净实收下限（元）" required>
        <a-input-number v-model:value="tier.from" :aria-label="'净实收下限 '+(i+1)" :min="0" :precision="2"/>
      </a-form-item>
      <a-form-item label="净实收上限（元）">
        <a-input-number v-model:value="tier.through" :aria-label="'净实收上限 '+(i+1)" :min="0" :precision="2" placeholder="不限"/>
      </a-form-item>
      <a-form-item label="比例（%）" required>
        <a-input-number v-model:value="tier.value" :aria-label="'档位比例 '+(i+1)" :min="0" :max="100" :precision="4"/>
      </a-form-item>
      <a-button v-if="config.tiers.length>1" type="link" danger @click="config.tiers.splice(i,1)">移除档位</a-button>
    </div>
    <a-button type="dashed" block @click="config.tiers.push({})">添加档位</a-button>
  </template>
  <a-collapse ghost class="period-scope">
    <a-collapse-panel key="scope" header="限定销售范围或任职">
      <a-form-item label="销售公司" :html-for="field('companies')">
        <a-select :id="field('companies')" v-model:value="config.salesCompanies" :options="companies"
          mode="multiple" allow-clear :filter-option="byName" placeholder="全部销售公司"/>
      </a-form-item>
      <a-form-item label="销售人员" :html-for="field('sales')">
        <a-select :id="field('sales')" v-model:value="config.salesPeople" :options="people"
          mode="multiple" allow-clear :filter-option="byName" placeholder="全部销售人员"/>
      </a-form-item>
      <a-form-item v-if="config.salesPeople?.length" label="销售来源角色" :html-for="field('role')" required>
        <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
      </a-form-item>
      <template v-if="config.recipient==='person'">
        <a-form-item label="任职组织" :html-for="field('appointment')" extra="填写后，仅计提任职有效期间的付款。">
          <a-select :id="field('appointment')" v-model:value="config.appointmentUnitId" :options="units"
            show-search allow-clear :filter-option="byName"/>
        </a-form-item>
        <a-form-item v-if="config.appointmentUnitId" label="任职方式" :html-for="field('kind')" required>
          <a-select :id="field('kind')" v-model:value="config.appointmentKind" :options="appointmentKinds"/>
        </a-form-item>
      </template>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.period-fields {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
.period-tier {grid-template-columns:repeat(3,minmax(0,1fr));align-items:end}
.period-fields :deep(.ant-input-number) {width:100%}
.period-scope {margin-top:8px}
@media (max-width:480px) { .period-tier {grid-template-columns:repeat(2,minmax(0,1fr))} }
</style>

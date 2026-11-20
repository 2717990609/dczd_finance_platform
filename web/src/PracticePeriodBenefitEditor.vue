<script setup>
import { computed } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'

const props = defineProps({
  component:Object, index:Number, people:Array, units:Array, companies:Array,
  recipientTypes:Array, appointmentKinds:Array, sourceRoles:Array, salesUnits:Array, pools:Array, createPool:Function,
})
const config = computed(() => props.component.periodBenefit)
const registration = computed(() => config.value.registrationScope)
const salesSources = [{value:'payment',label:'付款订单'}, {value:'registration',label:'签到报名'}]
const registrationCompanies = computed(() => props.companies.filter(option=>/^[1-9]\d*$/.test(option.value)))
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())
const field = name => 'practice-benefit-'+props.index+'-'+name
const calculationMethods = [
  {value:'rate',label:'按净实收比例'},
  {value:'fixed',label:'每份报名固定金额'},
  {value:'class_fixed',label:'整班固定金额'},
  {value:'tiers',label:'按净实收阶梯'},
]
</script>

<template>
  <div class="period-fields">
    <a-form-item label="收款安排名称" required>
      <a-input v-model:value="config.label" aria-label="收款安排名称" :maxlength="80" placeholder="例如交付团队分润"/>
    </a-form-item>
    <a-form-item label="收款对象" :html-for="field('recipient')" required>
      <a-select :id="field('recipient')" v-model:value="config.recipient" :options="recipientTypes" placeholder="选择收款对象"/>
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
  <div class="period-fields">
    <a-form-item label="计算方式" :html-for="field('method')">
      <a-select :id="field('method')" v-model:value="config.method" :options="calculationMethods"/>
    </a-form-item>
    <a-form-item v-if="config.method==='rate'" label="班次计提比例（%）" required>
      <a-input-number v-model:value="component.amount" aria-label="班次计提比例（%）" :min="0" :max="100" :precision="4"/>
    </a-form-item>
    <a-form-item v-else-if="config.method==='fixed' || config.method==='class_fixed'"
      :label="config.method==='class_fixed' ? '整班金额（元）' : '每份报名（元）'" :html-for="field('fixed')" required>
      <a-input-number :id="field('fixed')" v-model:value="config.fixedAmount" :min="0" :max="1000000000" :precision="2"/>
    </a-form-item>
  </div>
  <template v-if="config.method==='tiers'">
    <a-form-item>
      <a-checkbox v-model:checked="config.tierOnWholeClass">用全班净实收选档（含新报和复训）</a-checkbox>
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
    <a-collapse-panel key="scope" header="哪些业绩参与">
      <a-form-item label="业绩来源" :html-for="field('sales-source')">
        <a-select :id="field('sales-source')" :value="registration ? 'registration' : 'payment'" :options="salesSources"
          @change="config.registrationScope=$event==='registration' ? {} : undefined"/>
      </a-form-item>
      <template v-if="registration">
        <div class="period-fields">
          <a-form-item label="本人销售账号" :html-for="field('registration-seller')">
            <a-input :id="field('registration-seller')" v-model:value="registration.sellerId" inputmode="numeric"
              :maxlength="19" placeholder="大春之道销售账号" allow-clear/>
          </a-form-item>
          <a-form-item label="本人销售公司" :html-for="field('registration-company')">
            <a-select :id="field('registration-company')" v-model:value="registration.sellerCompanyId" :options="registrationCompanies"
              show-search allow-clear :filter-option="byName"/>
          </a-form-item>
        </div>
        <a-form-item label="下属公司" :html-for="field('registration-companies')">
          <a-select :id="field('registration-companies')" v-model:value="registration.companies" :options="registrationCompanies"
            mode="multiple" :max-tag-count="4" allow-clear :filter-option="byName"/>
        </a-form-item>
      </template>
      <template v-else>
      <a-form-item label="销售公司" :html-for="field('companies')">
        <a-select :id="field('companies')" v-model:value="config.salesCompanies" :options="companies"
          mode="multiple" allow-clear :filter-option="byName" placeholder="全部销售公司"/>
      </a-form-item>
      <a-form-item label="销售人员" :html-for="field('sales')">
        <a-select :id="field('sales')" v-model:value="config.salesPeople" :options="people"
          mode="multiple" allow-clear :filter-option="byName" placeholder="全部销售人员"/>
      </a-form-item>
      <a-form-item label="业绩部门或小组" :html-for="field('sales-units')" extra="按付款时的归属匹配任一所选部门或小组。同一笔付款只计一次。">
        <a-select :id="field('sales-units')" v-model:value="config.salesUnits" :options="salesUnits"
          mode="multiple" allow-clear :filter-option="byName" placeholder="全部部门和小组"/>
      </a-form-item>
      <a-form-item v-if="config.salesPeople?.length || config.salesUnits?.length" label="销售来源角色" :html-for="field('role')" required>
        <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
      </a-form-item>
      </template>
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
.period-fields {display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,160px),1fr));gap:4px 10px}
.period-tier {grid-template-columns:repeat(3,minmax(0,1fr));align-items:end}
.period-fields :deep(.ant-input-number) {width:100%}
.period-scope {margin-top:8px}
@media (max-width:480px) { .period-tier {grid-template-columns:repeat(2,minmax(0,1fr))} }
</style>

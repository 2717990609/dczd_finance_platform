<script setup>
import { computed } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import PracticePeriodRateStandards from './PracticePeriodRateStandards.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogOrganizationAppointmentFields from './CatalogOrganizationAppointmentFields.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import { hasPracticePaymentScope, practiceRecipientIsPerson } from './rule-component-form'

const props = defineProps({
  component:Object, index:Number, people:Array, units:Array, companies:Array, salesCategories:Array,
  registrationCompanies:Array, registrationLoading:Boolean, registrationError:String,
  recipientTypes:Array, referralCompanies:Array, appointmentKinds:Array, sourceRoles:Array, payeeRoles:Array, salesUnits:Array, pools:Array, createPool:Function,
  memberChoices:Array, duties:Array,
})
const config = computed(() => props.component.periodBenefit)
const usesStandardRates = computed(() => config.value.method==='rate' && Array.isArray(config.value.standardRates))
function toggleStandardRates() {
  if (usesStandardRates.value) {
    props.component.amount = config.value.standardRates.find(row=>row.scope==='general')?.value
    config.value.standardRates = undefined
  } else {
    config.value.standardRates = props.component.amount == null ? [] : [{scope:'general',value:props.component.amount}]
  }
}
const personalRecipient = computed(() => practiceRecipientIsPerson(config.value.recipient))
const registration = computed(() => config.value.registrationScope)
const registrationOption = [{value:'registration',label:'签到报名归属'}]
const scopeMode = computed(() => registration.value ? 'registration' : config.value.ownSales ? 'own' :
  config.value.restrictPayments || hasPracticePaymentScope(config.value) ? 'team' : 'all')
function changeScope(mode) {
  const benefit = config.value
  benefit.registrationScope = mode==='registration' ? (benefit.registrationScope || {}) : undefined
  benefit.ownSales = mode==='own'
  benefit.restrictPayments = mode==='team'
  if (mode!=='team') {
    benefit.salesCategoryIds = []
    benefit.salesCompanies = []
    benefit.salesPeople = []
    benefit.salesUnits = []
  }
  if (mode==='own' || mode==='team') props.component.sourceRole ||= props.sourceRoles[0]?.value
}
function setRecipient(recipient) {
  config.value.recipient = recipient
  if (recipient!=='person' && config.value.ownSales) changeScope('all')
  if (recipient==='order_appointment') props.component.sourceRole ||= props.sourceRoles[0]?.value
  if (recipient==='order_appointment' && config.value.method==='class_fixed') config.value.method = 'order_fixed'
  if (recipient!=='order_appointment' && config.value.method==='order_fixed') config.value.method = 'class_fixed'
}
const registrationCompanies = computed(() => (props.registrationCompanies ?? props.companies).filter(option=>/^[1-9]\d*$/.test(option.value)))
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())
const field = name => 'practice-benefit-'+props.index+'-'+name
const calculationMethods = computed(() => [
  {value:'rate',label:'比例'},
  config.value.recipient==='order_appointment' ? {value:'order_fixed',label:'固定金额'} : {value:'class_fixed',label:'固定金额'},
  {value:'tiers',label:'阶梯'},
  ...(config.value.recipient==='order_appointment' && config.value.method==='class_fixed'
    ? [{value:'class_fixed',label:'按组织整班计提'}] : []),
  ...(config.value.method==='fixed' ? [{value:'fixed',label:'按报名计提固定金额'}] : []),
])
</script>

<template>
  <RuleArrangementFields class="period-arrangement" :name="config.label" :name-id="field('name')"
    :duty-id="personalRecipient ? field('duty') : undefined" :duties="duties" v-model:duty="config.recipientDuty"
    :order-type-id="'rule-member-'+index" :order-types="memberChoices" v-model:order-type="component.classMember"
    @update:name="config.label=$event; config.labelIsCustom=true">
    <a-form-item label="收款对象" :html-for="field('recipient')" required>
      <a-select :id="field('recipient')" :value="config.recipient" :options="recipientTypes" placeholder="选择收款对象" @change="setRecipient"/>
    </a-form-item>
    <a-form-item v-if="config.recipient==='unit'" label="收款组织" :html-for="field('unit')" required>
      <a-select :id="field('unit')" v-model:value="config.unitId" :options="units" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="config.recipient==='person'" label="指定收款人" :html-for="field('person')" required>
      <a-select :id="field('person')" v-model:value="config.personId" :options="people" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="config.recipient==='order_person'" label="从订单取收款人" :html-for="field('payee-role')" required>
      <a-select :id="field('payee-role')" v-model:value="config.orderRecipientRole" :options="payeeRoles"
        show-search :filter-option="byName" placeholder="选择订单角色"/>
    </a-form-item>
    <template v-if="config.recipient==='order_appointment'">
      <CatalogOrganizationAppointmentFields :prefix="field('recipient')" :roles="appointmentKinds"
        v-model:level="config.orderAppointmentLevel" v-model:role="config.orderAppointmentKind"/>
      <a-form-item label="订单上的销售角色" :html-for="field('role')" required>
        <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
      </a-form-item>
    </template>
    <a-form-item v-if="config.recipient==='referral_company'" label="限定收款公司" :html-for="field('referral-companies')">
      <a-select :id="field('referral-companies')" v-model:value="config.referralCompanyIds" :options="referralCompanies"
        mode="multiple" :max-tag-count="1" allow-clear :filter-option="byName" placeholder="按推荐关系结算"/>
    </a-form-item>
    <PracticePoolFields v-if="config.recipient==='pool'" :target="config.pool" :prefix="field('target')"
      :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
    <template #calculation>
      <a-form-item label="计提方式" :html-for="field('method')">
        <a-select :id="field('method')" v-model:value="config.method" :options="calculationMethods"/>
      </a-form-item>
      <a-form-item v-if="config.method==='rate' && !usesStandardRates" label="比例（%）" :html-for="field('rate')" required>
        <a-input-number :id="field('rate')" v-model:value="component.amount" :min="0" :max="100" :precision="4"/>
      </a-form-item>
      <a-form-item v-else-if="['fixed','class_fixed','order_fixed'].includes(config.method)"
        :label="config.method==='order_fixed' ? '每笔符合条件的订单金额（元）'
          : config.recipient==='order_appointment' && config.method==='class_fixed' ? '每个组织整班金额（元）' : '金额（元）'"
        :html-for="field('fixed')" required>
        <a-input-number :id="field('fixed')" v-model:value="config.fixedAmount" :min="0" :max="1000000000" :precision="2"/>
      </a-form-item>
      <a-button v-if="config.method==='rate'" class="practice-toggle-rate-table" size="small" @click="toggleStandardRates">
        {{ usesStandardRates ? '改用统一比例' : '按类别或公司设比例' }}
      </a-button>
    </template>
    <template #scope>
      <RuleOrderScope :id="field('sales-source')" :model-value="scopeMode" :can-own="config.recipient==='person'"
        team-label="指定销售范围" all-label="全班" :additional="registrationOption" @update:model-value="changeScope"/>
      <div v-if="scopeMode!=='all'" class="rule-scope-filters">
        <template v-if="scopeMode==='team'">
          <a-form-item label="适用销售类别" :html-for="field('categories')">
            <a-select :id="field('categories')" v-model:value="config.salesCategoryIds" :options="salesCategories"
              mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="不限销售类别"/>
          </a-form-item>
          <a-form-item label="适用销售组织" :html-for="field('sales-units')">
            <a-select :id="field('sales-units')" v-model:value="config.salesUnits" :options="salesUnits"
              mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择公司、部门或小组"/>
          </a-form-item>
          <a-form-item v-if="companies?.length" label="销售公司" :html-for="field('companies')">
            <a-select :id="field('companies')" v-model:value="config.salesCompanies" :options="companies"
              mode="multiple" :max-tag-count="1" allow-clear :filter-option="byName" placeholder="不限销售公司"/>
          </a-form-item>
          <a-form-item label="销售人员" :html-for="field('sales')">
            <a-select :id="field('sales')" v-model:value="config.salesPeople" :options="people"
              mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="不限销售人员"/>
          </a-form-item>
        </template>
        <a-form-item v-if="config.recipient!=='order_appointment' && (scopeMode==='own' || scopeMode==='team' && (config.salesPeople?.length || config.salesUnits?.length))"
          label="订单上的销售角色" :html-for="field('role')" required>
          <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
        </a-form-item>
        <template v-if="registration">
          <a-form-item label="本人销售账号" :html-for="field('registration-seller')">
            <a-input :id="field('registration-seller')" v-model:value="registration.sellerId" inputmode="numeric"
              :maxlength="19" placeholder="大春之道销售账号" allow-clear/>
          </a-form-item>
          <a-form-item label="本人销售公司" :html-for="field('registration-company')">
            <a-select :id="field('registration-company')" v-model:value="registration.sellerCompanyId" :options="registrationCompanies"
              :loading="registrationLoading" show-search allow-clear :filter-option="byName"/>
          </a-form-item>
          <a-form-item label="下属公司" :html-for="field('registration-companies')">
            <a-select :id="field('registration-companies')" v-model:value="registration.companies" :options="registrationCompanies"
              :loading="registrationLoading" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
          </a-form-item>
        </template>
      </div>
    </template>
  </RuleArrangementFields>
  <a-alert v-if="registration && registrationError" type="warning" :message="registrationError" show-icon/>
  <a-form-item v-if="registration">
    <a-checkbox v-model:checked="registration.exclusive">与其他已标记互斥的报名安排不重复匹配</a-checkbox>
  </a-form-item>
  <PracticePeriodRateStandards v-if="usesStandardRates" v-model="config.standardRates" :prefix="field('standards')"
    :companies="companies" :categories="salesCategories"/>
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
  <a-collapse v-if="config.recipient==='person'" ghost class="period-appointment">
    <a-collapse-panel key="scope" header="收款人任职条件">
      <div class="period-fields">
        <a-form-item label="任职组织" :html-for="field('appointment')" extra="填写后，仅计提任职有效期间的付款。">
          <a-select :id="field('appointment')" v-model:value="config.appointmentUnitId" :options="units"
            show-search allow-clear :filter-option="byName"/>
        </a-form-item>
        <a-form-item v-if="config.appointmentUnitId" label="任职方式" :html-for="field('kind')" required>
          <a-select :id="field('kind')" v-model:value="config.appointmentKind" :options="appointmentKinds"/>
        </a-form-item>
      </div>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.period-arrangement :deep(.pool-create) {grid-column:1 / -1}
.practice-toggle-rate-table {align-self:end;justify-self:start;margin-bottom:4px}
.period-fields {display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,175px),1fr));gap:0 10px;align-items:start}
.period-tier {grid-template-columns:repeat(3,minmax(0,1fr));align-items:end}
.period-fields :deep(.ant-input-number) {width:100%}
.period-appointment :deep(.ant-collapse-header),.period-appointment :deep(.ant-collapse-content-box) {padding:4px 0}
@media (max-width:480px) { .period-tier {grid-template-columns:repeat(2,minmax(0,1fr))} }
</style>

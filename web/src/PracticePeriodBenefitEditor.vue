<script setup>
import { computed, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import PracticePeriodRateStandards from './PracticePeriodRateStandards.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogOrganizationAppointmentFields from './CatalogOrganizationAppointmentFields.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleSalesScopeFields from './RuleSalesScopeFields.vue'
import RuleTierTable from './RuleTierTable.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import { hasPracticePaymentScope, practiceRecipientIsPerson } from './rule-component-form'

const props = defineProps({
  component:Object, index:Number, people:Array, units:Array, companies:Array, salesCategories:Array,
  registrationCompanies:Array, registrationLoading:Boolean, registrationError:String,
  recipientTypes:Array, referralCompanies:Array, appointmentKinds:Array, sourceRoles:Array, payeeRoles:Array, salesUnits:Array, pools:Array, createPool:Function,
  memberChoices:Array, duties:Array,
})
const config = computed(() => props.component.periodBenefit)
const usesStandardRates = computed(() => config.value.method==='rate' && Array.isArray(config.value.standardRates))
const usesOrganizationRates = computed(() => usesStandardRates.value && config.value.standardRates.some(row=>row.salesUnits?.length))
watch(usesOrganizationRates, uses => {
  if (uses) props.component.sourceRole ||= props.sourceRoles[0]?.value
})
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
    benefit.joinSalesOrganizations = false
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
const tierColumns = ['净实收下限（元）','净实收上限（元）','比例（%）']
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
    </template>
    <template #scope>
      <RuleOrderScope :id="field('sales-source')" :model-value="scopeMode" :can-own="config.recipient==='person'"
        :additional="registrationOption" @update:model-value="changeScope"/>
      <div v-if="scopeMode!=='all' || usesOrganizationRates" class="rule-scope-filters">
        <RuleSalesScopeFields v-if="scopeMode==='team'" :organization-id="field('sales-units')" :category-id="field('categories')" :person-id="field('sales')"
          v-model:organization-ids="config.salesUnits" v-model:source-company-ids="config.salesCompanies"
          v-model:category-ids="config.salesCategoryIds" v-model:person-ids="config.salesPeople"
          :organizations="salesUnits" :source-companies="companies" :categories="salesCategories" :people="people"
          @organizations-change="({ids,legacyValues})=>config.joinSalesOrganizations=!!(ids.length && legacyValues.length)"/>
        <a-form-item v-if="config.recipient!=='order_appointment' && (usesOrganizationRates || scopeMode==='own' || scopeMode==='team' && (config.salesPeople?.length || config.salesUnits?.length))"
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
  <PracticePeriodRateStandards v-if="config.method==='rate'" v-model="config.standardRates" v-model:rate="component.amount"
    :prefix="field('standards')" :uniform-id="field('rate')" :companies="companies" :organizations="salesUnits" :categories="salesCategories"/>
  <RuleCommissionStandards v-else-if="['fixed','class_fixed','order_fixed'].includes(config.method)">
    <a-form-item class="commission-uniform-rate" :label="config.method==='order_fixed' ? '每笔符合条件的订单金额（元）'
      : config.recipient==='order_appointment' && config.method==='class_fixed' ? '每个组织整班金额（元）' : '金额（元）'"
      :html-for="field('fixed')" required>
      <a-input-number :id="field('fixed')" v-model:value="config.fixedAmount" :min="0" :max="1000000000" :precision="2"/>
    </a-form-item>
  </RuleCommissionStandards>
  <section v-if="config.method==='tiers'" class="period-tier-editor">
    <div class="period-tier-option">
      <a-checkbox v-model:checked="config.tierOnWholeClass">用全班净实收选档（含新报和复训）</a-checkbox>
    </div>
    <RuleTierTable label="净实收阶梯" :columns="tierColumns" :rows="config.tiers">
      <template #cells="{row,index}">
        <td>
          <a-form-item :label="tierColumns[0]" :html-for="field('tier-from-'+index)" required>
            <a-input-number :id="field('tier-from-'+index)" v-model:value="row.from" :aria-label="'净实收下限 '+(index+1)" :min="0" :precision="2"/>
          </a-form-item>
        </td>
        <td>
          <a-form-item :label="tierColumns[1]" :html-for="field('tier-through-'+index)" :required="index<config.tiers.length-1">
            <a-input-number :id="field('tier-through-'+index)" v-model:value="row.through" :aria-label="'净实收上限 '+(index+1)"
              :min="0" :precision="2" :placeholder="index===config.tiers.length-1 ? '不限' : undefined"/>
          </a-form-item>
        </td>
        <td>
          <a-form-item :label="tierColumns[2]" :html-for="field('tier-rate-'+index)" required>
            <a-input-number :id="field('tier-rate-'+index)" v-model:value="row.value" :aria-label="'档位比例 '+(index+1)"
              :min="0" :max="100" :precision="4"/>
          </a-form-item>
        </td>
      </template>
      <template #actions="{index}">
        <a-button v-if="config.tiers.length>1" type="link" danger size="small" @click="config.tiers.splice(index,1)">移除档位</a-button>
      </template>
    </RuleTierTable>
    <a-button type="dashed" size="small" @click="config.tiers.push({})">添加档位</a-button>
  </section>
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
.period-fields {display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,175px),1fr));gap:0 10px;align-items:start}
.period-tier-editor {margin-top:4px}
.period-tier-option {margin-bottom:4px}
.period-appointment :deep(.ant-collapse-header),.period-appointment :deep(.ant-collapse-content-box) {padding:4px 0}
</style>

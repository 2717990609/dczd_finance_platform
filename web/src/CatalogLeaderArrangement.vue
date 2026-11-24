<script setup>
import { computed } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import RuleSalesScopeFields from './RuleSalesScopeFields.vue'
import { catalogFlatMethods, catalogMembershipTypes, catalogScopeOptions } from './rule-component-form'

const props = defineProps({itemKey:String,teams:Array,api:Function,peopleFilters:Object,disabled:Boolean,inherited:Boolean,orderTypeText:String,
  salesCategories:Array,categoryNames:Object,membershipEditable:Boolean,orderType:String})
const emit = defineEmits(['update:orderType'])
const form = defineModel({type:Object,required:true})
const field = name => props.itemKey+'-'+name
const isGroup = computed(() => props.itemKey==='internal-group-leader')
const targets = computed(() => [
  ...(props.inherited ? [{value:'inherit',label:isGroup.value ? '付款时的小组在任负责人' : '规则登记的团队负责人'}] : []),
  ...(!props.inherited && !isGroup.value ? [{value:'registered',label:'规则登记的团队负责人'}] : []),
  ...(!isGroup.value || !props.inherited ? [{value:'appointment',label:isGroup.value ? '付款时的小组在任负责人' : '付款时的团队在任负责人'}] : []),
  {value:'person',label:'指定个人'},
])
const orderScopes = [
  {value:'all',label:'范围内全部成交'},
  {value:'members',label:'范围内其他团队成员成交'},
  {value:'head',label:'仅团队负责人亲自成交'},
]
const companyKey = 'company:'
const groupKey = 'group:'
const groupsFor = companies => props.teams.filter(team=>!companies.length || companies.includes(team.key))
  .flatMap(team=>(team.groups || []).map(group=>({value:groupKey+group.value,label:team.label+' / '+group.label+' · 小组'})))
const organizationOptions = computed(() => [
  ...props.teams.map(team=>({value:companyKey+team.key,label:team.label+' · 公司'})),
  ...new Map(groupsFor(form.value.scope.companies).map(group=>[group.value,group])).values(),
])
const organizationIds = computed(() => [
  ...form.value.scope.companies.map(id=>companyKey+id),
  ...form.value.scope.groups.map(id=>groupKey+id),
])
const categoryOptions = computed(() => catalogScopeOptions(props.salesCategories || [],form.value.scope.categoryIds,
  props.categoryNames,'已选类别名称待核'))
function update(fields) {
  form.value = {...form.value,...fields}
}
function selectTarget(target) {
  update({target,personId:undefined,personName:undefined})
}
function updateScope(fields) {
  update({scope:{...form.value.scope,...fields}})
}
function changeScope(mode) {
  if (mode==='team') {
    update({restrictScope:true})
  } else {
    update({restrictScope:false,scope:{...form.value.scope,companies:[],groups:[],categoryIds:[]}})
  }
}
function selectOrganizations(ids) {
  const companies = ids.filter(id=>id.startsWith(companyKey)).map(id=>id.slice(companyKey.length))
  const available = new Set(groupsFor(companies).map(group=>group.value))
  const groups = ids.filter(id=>id.startsWith(groupKey) && available.has(id)).map(id=>id.slice(groupKey.length))
  updateScope({companies,groups})
}
</script>

<template>
  <RuleArrangementFields :name="form.label" :name-id="field('label')" :order-type-text="orderTypeText"
    :order-type-id="membershipEditable ? field('member-type') : undefined" :order-type="orderType" :order-types="catalogMembershipTypes"
    @update:order-type="emit('update:orderType',$event)"
    :disabled="disabled" @update:name="update({label:$event})">
    <a-form-item label="收款对象" :html-for="field('target')" required>
      <a-select :id="field('target')" :value="form.target" :options="targets" :disabled="disabled"
        @change="selectTarget"/>
    </a-form-item>
    <a-form-item v-if="form.target==='person'" label="指定收款人" :html-for="field('person')"
      extra="计提其任职期间的订单" required>
      <AppointmentPersonSelect :id="field('person')" :value="form.personId" :api="api" endpoint="/catalog-rule-people"
        :filters="peopleFilters" :disabled="disabled" :current="form.personId ? {id:form.personId,name:form.personName} : undefined"
        @change="(personId,option)=>update({personId,personName:option?.name})"/>
    </a-form-item>
    <template #calculation>
      <a-form-item label="计提方式" :html-for="field('method')">
        <a-select :id="field('method')" :value="form.method" :options="catalogFlatMethods" :disabled="disabled"
          @change="update({method:$event,amount:undefined})"/>
      </a-form-item>
    </template>
    <template #standards>
      <RuleCommissionStandards>
        <template #scope>
          <RuleOrderScope :id="field('scope')" :model-value="form.restrictScope ? 'team' : 'all'" all-label="本规则适用团队" :disabled="disabled"
            @update:model-value="changeScope"/>
          <div class="rule-scope-filters">
            <RuleSalesScopeFields v-if="form.restrictScope" :organization-id="field('companies')"
              :category-id="categoryOptions.length ? field('categories') : undefined"
              :organization-ids="organizationIds" :category-ids="form.scope.categoryIds"
              :organizations="organizationOptions" :categories="categoryOptions" :disabled="disabled" organization-placeholder="选择公司或小组"
              @update:organization-ids="selectOrganizations" @update:category-ids="updateScope({categoryIds:$event})"/>
            <a-form-item label="适用成交人员" :html-for="field('orders')" class="leader-order-filter">
              <a-select :id="field('orders')" :value="form.scope.orders" :options="orderScopes" :disabled="disabled"
                @change="updateScope({orders:$event})"/>
            </a-form-item>
          </div>
        </template>
        <RuleStandardValue :id="field('amount')" :method="form.method" :model-value="form.amount"
          :disabled="disabled" string-mode @update:model-value="update({amount:$event})"/>
      </RuleCommissionStandards>
    </template>
    <template #advanced>
      <a-collapse v-if="$slots.registered" ghost class="rule-appointment-details">
        <a-collapse-panel key="registered" header="规则登记负责人">
          <slot name="registered"/>
        </a-collapse-panel>
      </a-collapse>
    </template>
  </RuleArrangementFields>
</template>

<style scoped>
.leader-order-filter:only-child { width:min(100%,380px) }
</style>

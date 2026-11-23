<script setup>
import { computed } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import CatalogPoolOwnerFields from './CatalogPoolOwnerFields.vue'
import CatalogTeamPoolFields from './CatalogTeamPoolFields.vue'
import { catalogScopeOptions } from './rule-component-form'

const props = defineProps({options:Object,api:Function,peopleFilters:Object,prefix:String,disabled:Boolean,salesAvailable:Boolean,
  productId:Number,savedPoolLabel:String,existingPool:Boolean,unitNames:Object})
const payee = defineModel({type:Object,required:true})
const seller = {value:'seller',label:'按成交销售归属'}
const targets = computed(() => [
  ...(props.salesAvailable || payee.value.target==='seller' ? [seller] : []),
  ...(!props.salesAvailable && payee.value.target==='seller' ? [] : [
    {value:'person',label:'个人'},{value:'company',label:'公司'},{value:'pool',label:'团队池'},
    {value:'referrer',label:'订单直接推荐人'},
  ]),
])
const organizations = computed(() => props.options.organizations.filter(option=>
  payee.value.target!=='company' || option.kind==='COMPANY'))
const roles = computed(() => props.options.roles.filter(option=>payee.value.target==='pool' || option.personal || option.organization))
const appointmentRole = computed(() => roles.value.find(option=>option.value===payee.value.role))
const appointmentUnits = computed(() => catalogScopeOptions(props.options.salesUnits || [],
  payee.value.appointmentUnitId ? [payee.value.appointmentUnitId] : [],props.unitNames,'已选组织'))
const memberRoles = computed(() => {
  const available = props.options.roles.filter(option=>option.organization || option.value==='SALES')
  if (!payee.value.memberRole || available.some(option=>option.value===payee.value.memberRole)) return available
  const original = props.options.roles.find(option=>option.value===payee.value.memberRole)
  return [...available,original || {value:payee.value.memberRole,label:payee.value.memberRole}]
})
const field = key => props.prefix+'-'+key
const poolOwner = computed(() => ({role:payee.value.role,personId:payee.value.ownerPersonId,personName:payee.value.ownerName}))
function update(fields) {
  payee.value = {...payee.value,...fields}
}
function changeTarget(target) {
  update({target,role:undefined,memberRole:undefined,personId:undefined,personName:undefined,
    ownerPersonId:undefined,ownerName:undefined,organization:undefined,poolId:undefined,recipientDuty:undefined,appointmentUnitId:undefined})
}
function addPool(pool) {
  const pools=props.options.teamPools ??= []
  if (!pools.some(option=>option.id===pool.id)) pools.push(pool)
}
</script>

<template>
  <a-form-item label="收款对象" :html-for="field('target')" required>
    <a-select :id="field('target')" :value="payee.target" :options="targets" :disabled="disabled || targets.length===1"
      placeholder="选择收款对象" @change="changeTarget"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person'" label="指定收款人" :html-for="field('person')" required>
    <AppointmentPersonSelect :id="field('person')" :value="payee.personId" :api="api" endpoint="/catalog-rule-people"
      :filters="peopleFilters" :disabled="disabled" :current="payee.personId ? {id:payee.personId,name:payee.personName} : undefined"
      @change="(personId,option)=>update({personId,personName:option?.name})"/>
  </a-form-item>
  <a-form-item v-else-if="payee.target==='company'" label="收款公司" :html-for="field('organization')" required>
    <a-select :id="field('organization')" :value="payee.organization" :options="organizations"
      show-search option-filter-prop="label" :disabled="disabled" placeholder="从组织目录选择"
      @update:value="update({organization:$event})"/>
  </a-form-item>
  <CatalogTeamPoolFields v-else-if="payee.target==='pool'" :prefix="prefix" :pools="options.teamPools" :units="options.salesUnits"
    :api="api" :product-id="productId" :disabled="disabled" :model-value="payee.poolId"
    :saved-label="savedPoolLabel" :existing="existingPool && !!payee.organization"
    @created="addPool" @update:model-value="update({poolId:$event})"/>
  <a-form-item v-if="payee.target==='person'" label="收款人职责" :html-for="field('duty')">
    <a-select :id="field('duty')" :value="payee.recipientDuty" :options="options.duties"
      show-search option-filter-prop="label" :disabled="disabled" allow-clear placeholder="选填"
      @update:value="update({recipientDuty:$event})"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person'" label="任职条件" :html-for="field('role')">
    <a-select :id="field('role')" :value="payee.role" :options="roles" show-search option-filter-prop="label" :disabled="disabled"
      allow-clear placeholder="选填"
      @update:value="update({role:$event,appointmentUnitId:undefined})"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person' && (appointmentRole?.organization || payee.appointmentUnitId)"
    label="任职组织" :html-for="field('appointment-unit')" :required="appointmentRole?.organization && !appointmentRole.personal">
    <a-select :id="field('appointment-unit')" :value="payee.appointmentUnitId" :options="appointmentUnits" show-search
      option-filter-prop="label" :disabled="disabled" :allow-clear="appointmentRole?.personal"
      :placeholder="appointmentRole?.personal ? '留空按产品或项目任职' : '选择任职组织'"
      @update:value="update({appointmentUnitId:$event})"/>
  </a-form-item>
  <CatalogPoolOwnerFields v-if="payee.target==='pool'" :prefix="field('owner')" :roles="roles"
    :api="api" :people-filters="peopleFilters" :disabled="disabled"
    :model-value="poolOwner"
    @update:model-value="({role,personId,personName})=>update({role,ownerPersonId:personId,ownerName:personName})"/>
  <a-form-item v-if="payee.target==='pool'" label="可参与分配的成员岗位" :html-for="field('member-role')" required>
    <a-select :id="field('member-role')" :value="payee.memberRole" :options="memberRoles" show-search option-filter-prop="label"
      :disabled="disabled" @update:value="update({memberRole:$event})"/>
  </a-form-item>
</template>

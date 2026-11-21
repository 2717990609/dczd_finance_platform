<script setup>
import { computed } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({options:Object,api:Function,peopleFilters:Object,prefix:String,disabled:Boolean,salesAvailable:Boolean})
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
const roles = computed(() => props.options.roles.filter(option=>payee.value.target==='pool' || option.personal))
const memberRoles = computed(() => {
  const available = props.options.roles.filter(option=>option.organization || option.value==='SALES')
  if (!payee.value.memberRole || available.some(option=>option.value===payee.value.memberRole)) return available
  const original = props.options.roles.find(option=>option.value===payee.value.memberRole)
  return [...available,original || {value:payee.value.memberRole,label:payee.value.memberRole}]
})
const field = key => props.prefix+'-'+key
function update(fields) {
  payee.value = {...payee.value,...fields}
}
function changeTarget(target) {
  update({target,role:undefined,memberRole:undefined,personId:undefined,personName:undefined,organization:undefined})
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
  <a-form-item v-else-if="payee.target==='company' || payee.target==='pool'" :label="payee.target==='pool'?'归集组织':'收款公司'" :html-for="field('organization')" required>
    <a-select :id="field('organization')" :value="payee.organization" :options="organizations"
      show-search option-filter-prop="label" :disabled="disabled" placeholder="从组织目录选择"
      @update:value="update({organization:$event})"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person' || payee.target==='pool'" :label="payee.target==='pool'?'谁负责分配':'收款人须担任的岗位'"
    :html-for="field('role')" required>
    <a-select :id="field('role')" :value="payee.role" :options="roles" show-search option-filter-prop="label" :disabled="disabled"
      @update:value="update({role:$event})"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='pool'" label="可参与分配的成员岗位" :html-for="field('member-role')" required>
    <a-select :id="field('member-role')" :value="payee.memberRole" :options="memberRoles" show-search option-filter-prop="label"
      :disabled="disabled" @update:value="update({memberRole:$event})"/>
  </a-form-item>
</template>

<script setup>
import { computed } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({options:Object,api:Function,peopleFilters:Object,prefix:String,disabled:Boolean})
const payee = defineModel({type:Object,required:true})
const targets = [{value:'person',label:'个人'},{value:'company',label:'公司'},{value:'pool',label:'团队池'},
  {value:'referrer',label:'订单直接推荐人'}]
const organizations = computed(() => props.options.organizations.filter(option=>
  payee.value.target!=='company' || option.kind==='COMPANY'))
const roles = computed(() => props.options.roles.filter(option=>payee.value.target==='pool' || option.personal))
const field = key => props.prefix+'-'+key
function changeTarget(target) {
  payee.value = {...payee.value,target,role:undefined,personId:undefined,personName:undefined,organization:undefined}
}
</script>

<template>
  <a-form-item label="收款对象" :html-for="field('target')" required>
    <a-select :id="field('target')" :value="payee.target" :options="targets" :disabled="disabled" @change="changeTarget"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person'" label="指定收款人" :html-for="field('person')" required>
    <AppointmentPersonSelect :id="field('person')" v-model:value="payee.personId" :api="api" endpoint="/catalog-rule-people"
      :filters="peopleFilters" :disabled="disabled" :current="payee.personId ? {id:payee.personId,name:payee.personName} : undefined"
      @change="(_,option)=>payee.personName=option.name"/>
  </a-form-item>
  <a-form-item v-else-if="payee.target!=='referrer'" :label="payee.target==='pool'?'归集组织':'收款公司'" :html-for="field('organization')" required>
    <a-select :id="field('organization')" v-model:value="payee.organization" :options="organizations"
      show-search option-filter-prop="label" :disabled="disabled" placeholder="从组织目录选择"/>
  </a-form-item>
  <a-form-item v-if="payee.target==='person' || payee.target==='pool'" :label="payee.target==='pool'?'谁负责分配':'收款人须担任的岗位'"
    :html-for="field('role')" required>
    <a-select :id="field('role')" v-model:value="payee.role" :options="roles" show-search option-filter-prop="label" :disabled="disabled"/>
  </a-form-item>
</template>

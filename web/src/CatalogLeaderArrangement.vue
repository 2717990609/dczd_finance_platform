<script setup>
import { computed } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import { catalogFlatMethods } from './rule-component-form'

const props = defineProps({itemKey:String,teams:Array,api:Function,peopleFilters:Object,disabled:Boolean})
const form = defineModel({type:Object,required:true})
const field = name => props.itemKey+'-'+name
const isGroup = computed(() => props.itemKey==='internal-group-leader')
const targets = [
  {value:'appointment',label:'各组织付款时的在任负责人'},
  {value:'person',label:'指定个人'},
]
const orderScopes = [
  {value:'all',label:'范围内全部成交'},
  {value:'members',label:'范围内其他团队成员成交'},
  {value:'head',label:'仅团队负责人亲自成交'},
]
const teamOptions = computed(() => props.teams.map(team=>({value:team.key,label:team.label})))
const groupsFor = companies => Array.from(new Map(
  props.teams.filter(team=>!companies.length || companies.includes(team.key))
    .flatMap(team=>(team.groups || []).map(group=>[group.value,group])),
).values())
const groupOptions = computed(() => groupsFor(form.value.scope.companies))
function update(fields) {
  form.value = {...form.value,...fields}
}
function updateScope(fields) {
  update({scope:{...form.value.scope,...fields}})
}
function selectCompanies(companies) {
  const available = new Set(groupsFor(companies).map(group=>group.value))
  updateScope({companies,groups:form.value.scope.groups.filter(id=>available.has(id))})
}
</script>

<template>
  <RuleArrangementFields :name="form.label" :name-id="field('label')" :disabled="disabled" @update:name="update({label:$event})">
    <a-form-item label="收款对象" :html-for="field('target')" required>
      <a-select :id="field('target')" :value="form.target" :options="targets" :disabled="disabled"
        @change="update({target:$event,personId:undefined,personName:undefined})"/>
    </a-form-item>
    <a-form-item v-if="form.target==='person'" label="指定收款人" :html-for="field('person')" required>
      <AppointmentPersonSelect :id="field('person')" :value="form.personId" :api="api" endpoint="/catalog-rule-people"
        :filters="peopleFilters" :disabled="disabled" :current="form.personId ? {id:form.personId,name:form.personName} : undefined"
        @change="(personId,option)=>update({personId,personName:option?.name})"/>
    </a-form-item>
    <a-form-item label="计提方式" :html-for="field('method')">
      <a-select :id="field('method')" :value="form.method" :options="catalogFlatMethods" :disabled="disabled"
        @change="update({method:$event,amount:undefined})"/>
    </a-form-item>
    <a-form-item :label="form.method==='fixed' ? '金额（元/笔）' : '比例（%）'" :html-for="field('amount')" required>
      <a-input-number :id="field('amount')" :value="form.amount" :disabled="disabled"
        :min="0" :max="form.method==='rate' ? 100 : undefined" :precision="2" string-mode @update:value="update({amount:$event})"/>
    </a-form-item>
  </RuleArrangementFields>
  <div class="rule-basics">
    <a-form-item label="适用内部团队" :html-for="field('companies')">
      <a-select :id="field('companies')" :value="form.scope.companies" :options="teamOptions"
        :disabled="disabled" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="全部内部团队"
        @change="selectCompanies"/>
    </a-form-item>
    <a-form-item v-if="groupOptions.length" label="适用销售小组" :html-for="field('groups')">
      <a-select :id="field('groups')" :value="form.scope.groups" :options="groupOptions"
        :disabled="disabled" mode="multiple" :max-tag-count="2" allow-clear option-filter-prop="label"
        :placeholder="isGroup ? '上述团队内全部小组' : '上述团队全部成员'" @change="updateScope({groups:$event})"/>
    </a-form-item>
    <a-form-item label="适用成交" :html-for="field('orders')">
      <a-select :id="field('orders')" :value="form.scope.orders" :options="orderScopes" :disabled="disabled"
        @change="updateScope({orders:$event})"/>
    </a-form-item>
  </div>
  <small v-if="form.target==='person'" class="rule-block-recipient">按付款时在相应{{ isGroup ? '小组' : '团队' }}的任职期间计提</small>
</template>

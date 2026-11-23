<script setup>
import { computed, ref } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

defineProps({prefix:String, roles:Array, api:Function, peopleFilters:Object, disabled:Boolean})
const owner = defineModel({type:Object,required:true})
const choosingPerson = ref(false)
const mode = computed(() => owner.value.personId || choosingPerson.value ? 'person' : 'role')
const modes = [{value:'role',label:'按在任岗位'},{value:'person',label:'指定人员'}]

function changeMode(value) {
  choosingPerson.value = value==='person'
  owner.value = {}
}
</script>

<template>
  <a-form-item label="负责人来源" :html-for="prefix+'-source'">
    <a-select :id="prefix+'-source'" :value="mode" :options="modes" :disabled="disabled" @change="changeMode"/>
  </a-form-item>
  <a-form-item v-if="mode==='person'" label="分配负责人" :html-for="prefix+'-person'" required>
    <AppointmentPersonSelect :id="prefix+'-person'" :value="owner.personId" :api="api" endpoint="/catalog-rule-people"
      :filters="peopleFilters" :disabled="disabled" :current="owner.personId ? {id:owner.personId,name:owner.personName} : undefined"
      @change="(personId,option)=>owner={personId,personName:option?.name}"/>
  </a-form-item>
  <a-form-item v-else label="负责人岗位" :html-for="prefix+'-role'" required>
    <a-select :id="prefix+'-role'" :value="owner.role" :options="roles" show-search option-filter-prop="label" :disabled="disabled"
      @update:value="owner={role:$event}"/>
  </a-form-item>
</template>

<script setup>
import { ref,watch } from 'vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
const props=defineProps({unit:Object,api:Function,formatTime:Function,canWrite:Boolean})
const emit=defineEmits(['close'])
const company=ref(),data=ref(),error=ref(''),loading=ref(false)
async function load(){try{data.value=await props.api('/organization-dispatch/unit?unitId='+props.unit.id)}catch(e){error.value=e.message}}
watch(()=>props.unit.id,load,{immediate:true})
async function save(){loading.value=true;try{await props.api('/organization-dispatch/unit',{method:'POST',body:JSON.stringify({unitId:props.unit.id,companyId:company.value})});await load()}catch(e){error.value=e.message}finally{loading.value=false}}
</script>
<template>
 <OrganizationDispatch v-if="data?.linkedId" :company="{id:data.linkedId,name:data.linkedName}" :api="api" :can-write="canWrite" :format-time="formatTime" @close="emit('close')"/>
 <a-drawer v-else :open="true" :title="unit.name+' · 销售公司关联'" width="min(500px, 100vw)" @close="emit('close')">
  <a-alert v-if="error" type="error" :message="error"/>
  <a-form v-if="data && canWrite && data.candidates.length" layout="vertical" @finish="save">
   <a-form-item label="同名内部销售公司"><a-select v-model:value="company" :options="data.candidates"/></a-form-item>
   <a-button html-type="submit" :disabled="!company" :loading="loading">关联销售公司</a-button>
  </a-form>
  <a-empty v-else-if="data" description="暂无可关联的同名内部销售公司"/>
 </a-drawer>
</template>

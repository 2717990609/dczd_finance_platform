<script setup>
import {ref,watch} from 'vue'
const props=defineProps({api:Function,units:Array,parentId:String})
const emit=defineEmits(['close','saved'])
const name=ref(''),kind=ref('DEPARTMENT'),parent=ref(props.parentId),company=ref(),companies=ref([]),error=ref(''),busy=ref(false)
let seq=0
watch(name,async value=>{const req=++seq;company.value=undefined;companies.value=[]
 if(!value.trim())return
 try{const res=await props.api('/organization-sales-companies?type=INTERNAL&name='+encodeURIComponent(value.trim()));if(req===seq)companies.value=res.items.filter(c=>c.status==='ACTIVE' && c.name.trim().toLowerCase()===value.trim().toLowerCase()).map(c=>({value:c.id,label:c.name}))}
 catch(e){if(req===seq)error.value=e.message}
})
async function save(){busy.value=true;try{const data=await props.api('/organization-dispatch/create-unit',{method:'POST',body:JSON.stringify({name:name.value,kind:kind.value,parentId:parent.value,companyId:company.value})});emit('saved',data.id)}catch(e){error.value=e.message}finally{busy.value=false}}
</script>
<template>
 <a-modal :open="true" title="登记内部组织" ok-text="保存" cancel-text="取消" :ok-button-props="{disabled:!name.trim()}" :confirm-loading="busy" @ok="save" @cancel="emit('close')">
  <a-alert v-if="error" :message="error" type="error" show-icon/>
  <a-form layout="vertical">
   <a-form-item label="组织名称" required><a-input v-model:value="name" :maxlength="100"/></a-form-item>
   <a-form-item label="组织类型"><a-select v-model:value="kind" :options="[{value:'COMPANY',label:'公司'},{value:'DEPARTMENT',label:'部门'},{value:'TEAM',label:'小组'},{value:'STORE',label:'门店'}]"/></a-form-item>
   <a-form-item label="上级组织"><a-select v-model:value="parent" :options="units.filter(u=>u.status==='ACTIVE').map(u=>({value:u.id,label:u.name}))" allow-clear show-search option-filter-prop="label"/></a-form-item>
   <a-form-item label="关联同名内部销售公司"><a-select v-model:value="company" :options="companies" allow-clear placeholder="可留空"/></a-form-item>
  </a-form>
 </a-modal>
</template>

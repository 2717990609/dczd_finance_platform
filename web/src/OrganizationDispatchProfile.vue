<script setup>
import {ref,onMounted} from 'vue'
const props=defineProps({api:Function,company:Object,profile:Object,categories:Array})
const emit=defineEmits(['close','saved'])
const name=ref(props.profile?.name || ''),status=ref(props.profile?.status || 'ACTIVE'),type=ref('INTERNAL'),category=ref(),targets=ref([])
const parent=ref(props.profile?.parentId || undefined),choices=ref(props.profile?.parentId ? [{value:props.profile.parentId,label:props.profile.parentName}]:[])
const error=ref(''),busy=ref(false)
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
let request=0
async function search(term=''){
 const seq=++request
 try{const res=await props.api('/organization-sales-companies?name='+encodeURIComponent(term));if(seq===request)choices.value=res.items.filter(c=>c.id!==props.company?.id && c.status==='ACTIVE').map(c=>({value:c.id,label:c.name}))}catch(e){if(seq===request)error.value=e.message}
}
async function save(){
 busy.value=true;error.value=''
 try{const result=await props.api('/organization-dispatch/profile',{method:'POST',body:JSON.stringify({id:props.company?.id,name:name.value.trim(),status:status.value,companyType:type.value,categoryId:category.value,parentId:parent.value,targets:targets.value,expectedVersion:props.profile?.version || 0})});emit('saved',result)}
 catch(e){error.value=e.message}finally{busy.value=false}
}
</script>
<template>
 <a-modal :open="true" :title="company ? '维护销售公司' : '登记销售公司'" ok-text="保存" cancel-text="取消"
  :confirm-loading="busy" :ok-button-props="{disabled:!name.trim() || !company && !category}" @ok="save" @cancel="emit('close')">
  <a-alert v-if="error" :message="error" type="error" show-icon/>
  <a-form layout="vertical">
   <a-form-item label="公司名称" required><a-input v-model:value="name" :maxlength="100"/></a-form-item>
   <a-form-item label="上级销售公司"><a-select v-model:value="parent" :options="choices" show-search allow-clear :filter-option="false" @focus="search()" @search="search"/></a-form-item>
   <a-form-item v-if="company" label="公司状态"><a-radio-group v-model:value="status" :options="[{label:'启用',value:'ACTIVE'},{label:'停用',value:'INACTIVE'}]"/></a-form-item>
   <template v-else>
    <a-form-item label="公司类型"><a-radio-group v-model:value="type" :options="[{label:'内部公司',value:'INTERNAL'},{label:'外部公司',value:'EXTERNAL'}]"/></a-form-item>
    <a-form-item label="销售类别" required><a-select v-model:value="category" :options="categories" show-search option-filter-prop="label"/></a-form-item>
    <a-form-item label="同步目标"><a-checkbox-group v-model:value="targets" :options="systems"/></a-form-item>
   </template>
  </a-form>
 </a-modal>
</template>

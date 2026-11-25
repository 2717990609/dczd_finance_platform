<script setup>
import { computed, ref, watch } from 'vue'
import OrganizationDispatchProfile from './OrganizationDispatchProfile.vue'
import OrganizationDispatchAccounts from './OrganizationDispatchAccounts.vue'
const props=defineProps({api:Function,company:Object,personId:Number,canWrite:Boolean,formatTime:Function})
const emit=defineEmits(['close','changed'])
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const label=key=>systems.find(o=>o.value===key)?.label || key
const states={APPLIED:'已送达',PENDING:'待发送',PROCESSING:'发送中',CONFLICT:'需核对',RETRYABLE:'等待重试',STOPPED:'已停止',WAIT_CATEGORY:'等待类别',WAIT_PARENT_COMPANY:'等待上级公司'}
const operations={CREATE:'创建身份',ENABLE:'启用',DISABLE:'停用',SET_OWNER:'设负责人',UNSET_OWNER:'移交负责人资格',TRANSFER:'调动',RENAME:'同步姓名',RECONCILE:'纠正身份'}
const columns=[{title:'目标系统',key:'target',width:105},{title:'业务变更',key:'operation',width:180},{title:'状态 / 原因',key:'state',width:200},{title:'生效时间',key:'effective',width:154},{title:'回执编号',dataIndex:'externalId',width:140},{title:'操作',key:'actions',width:80}]
const editProfile=ref(false)
const data=ref(),targets=ref([]),page=ref(1),error=ref(''),busy=ref(false),notice=ref('')
const title=computed(()=>props.company?.name ? props.company.name+' · 组织同步' : props.personId ? '业务系统销售身份' : '组织同步记录')
let seq=0
async function refresh(){
 const req=++seq,params=new URLSearchParams({page:page.value});if(props.company)params.set('companyId',props.company.id);if(props.personId)params.set('personId',props.personId)
 try{error.value='';const value=await props.api('/organization-dispatch?'+params);if(req===seq){data.value=value;targets.value=[...value.targets]}}
 catch(e){if(req===seq)error.value=e.message}
}
watch(()=>[props.personId,props.company?.id,page.value],refresh,{immediate:true})
async function submit(path,body){
 busy.value=true;error.value=''
 try{await props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(body)});await refresh();notice.value='操作已保存';emit('changed')}
 catch(e){error.value=e.message}finally{busy.value=false}
}
</script>
<template>
 <a-drawer :open="true" :title="title" width="min(880px, 100vw)" @close="emit('close')">
  <a-space direction="vertical" style="width:100%" :size="16">
   <a-alert v-if="error" :message="error" type="error" show-icon/>
   <a-alert v-if="notice" :message="notice" type="success" show-icon closable @close="notice=''"/>
   <template v-if="company && data">
    <a-button v-if="canWrite && data.wireId" @click="editProfile=true">维护公司名称、上级和启停状态</a-button>
    <OrganizationDispatchProfile v-if="editProfile" :api="api" :company="company" :profile="data.profile" @close="editProfile=false" @saved="editProfile=false;refresh();emit('changed')"/>
    <label>公司的同步目标</label>
    <a-checkbox-group v-model:value="targets" :options="systems" :disabled="!canWrite || busy"/>
    <a-button v-if="canWrite" :loading="busy" @click="submit('targets',{companyId:company.id,targets,expectedTargets:data.targets})">保存目标系统</a-button>
   </template>
   <OrganizationDispatchAccounts v-if="personId && data" :person-id="personId" :api="api" :systems="systems" :can-write="canWrite" :bindings="data.bindings" @changed="refresh();emit('changed')"/>
   <a-space><h3>投递记录</h3><a-button size="small" @click="refresh">刷新</a-button></a-space>
   <a-table size="small" row-key="id" :columns="columns" :data-source="data?.items || []" :scroll="{x:860}"
    :pagination="{current:page,total:data?.total || 0,pageSize:25,showSizeChanger:false,hideOnSinglePage:true}" @change="page=$event.current">
    <template #bodyCell="{column,record}">
     <span v-if="column.key==='target'">{{label(record.target)}}</span>
     <span v-else-if="column.key==='operation'">{{record.name}} · {{operations[record.operation] || (record.kind==='COMPANY' ? '公司' : '类别')}}</span>
     <span v-else-if="column.key==='state'">{{states[record.state] || record.state}}<small v-if="record.reason"> · {{record.reason}}</small></span>
     <span v-else-if="column.key==='effective'">{{formatTime(record.effectiveAt)}}</span>
     <a-button v-else-if="column.key==='actions' && canWrite && record.kind==='MEMBER' && record.state==='PENDING' && Date.parse(record.effectiveAt)>Date.now()" size="small" @click="submit('cancel',{messageId:record.id})">取消计划</a-button>
     <a-button v-else-if="column.key==='actions' && canWrite && ['RETRYABLE','CONFLICT','WAIT_CATEGORY','WAIT_PARENT_COMPANY'].includes(record.state)" size="small" :disabled="busy" @click="submit('retry',{messageId:record.id,expectedAttempts:record.attempts})">重试</a-button>
    </template>
   </a-table>
  </a-space>
 </a-drawer>
</template>

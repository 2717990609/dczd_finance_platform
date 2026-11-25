<script setup>
import { computed, ref, watch } from 'vue'
import { Drawer } from 'ant-design-vue'
import { syncReasons } from './organization-sync-reasons'
import { ReloadOutlined } from '@ant-design/icons-vue'
import OrganizationDispatchProfile from './OrganizationDispatchProfile.vue'
import OrganizationDispatchAccounts from './OrganizationDispatchAccounts.vue'
import OrganizationSourceRelations from './OrganizationSourceRelations.vue'
const props=defineProps({api:Function,company:Object,personId:Number,personName:String,canWrite:Boolean,formatTime:Function,embedded:Boolean})
const emit=defineEmits(['close','changed'])
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const label=key=>systems.find(o=>o.value===key)?.label || key
const states={APPLIED:'已送达',PENDING:'待发送',PROCESSING:'发送中',CONFLICT:'需核对',RETRYABLE:'等待重试',STOPPED:'已停止',WAIT_CATEGORY:'等待类别',WAIT_PARENT_COMPANY:'等待上级公司'}
const operations={CREATE:'创建身份',ENABLE:'启用',DISABLE:'停用',SET_OWNER:'设负责人',UNSET_OWNER:'移交负责人资格',TRANSFER:'调动',RENAME:'同步姓名',RECONCILE:'纠正身份'}
const columns=[{title:'目标系统',key:'target',width:105},{title:'业务变更',key:'operation',width:180},{title:'状态 / 原因',key:'state',width:200},{title:'生效时间',key:'effective',width:154},{title:'回执编号',dataIndex:'externalId',width:140},{title:'操作',key:'actions',width:80}]
const stateColors={APPLIED:'green',PROCESSING:'blue',CONFLICT:'red',RETRYABLE:'orange'}
const loading=ref(false)
const targetFilter=ref(''),issuesOnly=ref(false)
const editProfile=ref(false)
const closingUse=ref()
const activeUses=computed(()=>(data.value?.businessUses || []).filter(row=>row.active))
const otherUses=computed(()=>(data.value?.businessUses || []).filter(row=>!row.active))
function confirmCloseUse(row){
 closingUse.value={...row,requestId:crypto.randomUUID(),error:''}
}
async function closeUse(){
 if(!closingUse.value || busy.value)return
 busy.value=true
 const row=closingUse.value
 try{
  await props.api('/organization-dispatch/close-company-use',{method:'POST',body:JSON.stringify({
   requestId:row.requestId,companyId:props.company.id,source:row.source,externalId:row.externalId,expectedFrom:row.validFrom,
  })})
  closingUse.value=undefined
  await refresh()
  notice.value='已结束该系统的公司使用关系'
  emit('changed')
 }catch(e){row.error=e.message}finally{busy.value=false}
}
const data=ref(),targets=ref([]),page=ref(1),error=ref(''),busy=ref(false),notice=ref('')
const title=computed(()=>props.company?.name ? props.company.name+' · 组织同步' : props.personId ? (props.personName ? props.personName+' · 人员同步' : '人员同步') : '组织同步记录')
let seq=0
async function refresh(){
 const req=++seq,params=new URLSearchParams({page:page.value});if(props.company)params.set('companyId',props.company.id);if(props.personId)params.set('personId',props.personId)
 loading.value=true
 if(targetFilter.value)params.set('target',targetFilter.value);if(issuesOnly.value)params.set('issues','true')
 try{error.value='';const value=await props.api('/organization-dispatch?'+params);if(req===seq){data.value=value;targets.value=[...value.targets]}}
 catch(e){if(req===seq)error.value=e.message}
 finally{if(req===seq)loading.value=false}
}
watch([targetFilter,issuesOnly],()=>{if(page.value!==1)page.value=1;else refresh()})
watch(()=>[props.personId,props.company?.id,page.value],refresh,{immediate:true})
async function submit(path,body){
 busy.value=true;error.value=''
 try{await props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(body)});await refresh();notice.value='操作已保存';emit('changed')}
 catch(e){error.value=e.message}finally{busy.value=false}
}
defineExpose({refresh})
</script>
<template>
 <component :is="embedded ? 'section' : Drawer" :class="{'records':embedded}"
   v-bind="embedded ? {} : {open:true,title,width:'min(880px, 100vw)'}" @close="emit('close')">
  <div class="dispatch-content">
   <a-alert v-if="error" :message="error" type="error" show-icon/>
   <a-alert v-if="notice" :message="notice" type="success" show-icon closable @close="notice=''"/>
   <template v-if="company && data">
    <OrganizationSourceRelations :api="api" :company-id="company.id" :format-time="formatTime"/>
    <a-button v-if="canWrite && data.wireId" @click="editProfile=true">维护公司名称、上级和启停状态</a-button>
    <OrganizationDispatchProfile v-if="editProfile" :api="api" :company="company" :profile="data.profile" @close="editProfile=false" @saved="editProfile=false;refresh();emit('changed')"/>
    <div v-if="data.targetStatus?.length"><h3>各系统当前状态</h3>
      <p v-for="status in data.targetStatus" :key="status.target">{{ label(status.target) }} · {{ states[status.state] || '暂无投递' }}<span v-if="status.externalId"> · 编号 {{status.externalId}}</span><span v-if="status.reason"> · {{syncReasons[status.reason] || status.reason}}</span></p>
    </div>
    <label>公司的同步目标</label>
    <a-checkbox-group v-model:value="targets" :options="systems" :disabled="!canWrite || busy"/>
    <a-button v-if="canWrite" :loading="busy" @click="submit('targets',{companyId:company.id,targets,expectedTargets:data.targets})">保存目标系统</a-button>
    <section aria-label="公司业务使用">
     <h3>业务系统使用关系</h3>
     <p v-if="!activeUses.length">暂无正在使用此公司的业务系统。</p>
     <div v-for="row in activeUses" :key="row.source+':'+row.externalId" class="business-use">
      <span>{{label(row.source)}} · 公司编号 {{row.externalId}}</span>
      <a-button v-if="canWrite" size="small" @click="confirmCloseUse(row)">停止使用</a-button>
     </div>
     <details v-if="otherUses.length"><summary>查看其他生效区间（{{otherUses.length}}）</summary>
      <p v-for="row in otherUses" :key="[row.source,row.externalId,row.validFrom,row.validTo].join(':')">
       {{label(row.source)}} · {{row.externalId}} · {{formatTime(row.validFrom)}} 至 {{row.validTo ? formatTime(row.validTo) : '长期有效'}}
      </p>
     </details>
     <a-modal v-if="closingUse" open :title="'停止'+label(closingUse.source)+'使用此公司？'" ok-text="确认停止使用"
      :ok-button-props="{danger:true}" :confirm-loading="busy" :closable="!busy" :keyboard="!busy" :mask-closable="false" @ok="closeUse" @cancel="!busy && (closingUse=undefined)">
      <p>{{company.name}} 在{{label(closingUse.source)}}的公司编号 {{closingUse.externalId}} 将结束当前归属。历史订单按原生效区间核对。</p>
      <a-alert v-if="closingUse.error" type="error" :message="closingUse.error" show-icon/>
     </a-modal>
    </section>
   </template>
   <OrganizationDispatchAccounts v-if="personId && data" :person-id="personId" :person-name="personName" :api="api" :systems="systems" :can-write="canWrite" :bindings="data.bindings" @changed="refresh();emit('changed')"/>
   <div class="dispatch-table-head">
     <div><h3>同步记录</h3><span v-if="data && !error" class="record-total">{{ data.total }} 条</span></div>
     <a-button size="small" :loading="loading" @click="refresh"><template #icon><ReloadOutlined aria-hidden="true"/></template>刷新</a-button>
   </div>
   <a-space wrap>
    <a-select v-model:value="targetFilter" aria-label="筛选目标系统" style="min-width:132px" :options="[{value:'',label:'全部目标系统'},...systems]"/>
    <a-checkbox v-model:checked="issuesOnly">只看异常记录</a-checkbox>
   </a-space>
   <a-table size="small" row-key="id" :loading="loading" :columns="columns" :data-source="data?.items || []" :scroll="{x:860}"
    :pagination="{current:page,total:data?.total || 0,pageSize:25,showSizeChanger:false,hideOnSinglePage:true}" @change="page=$event.current">
    <template #bodyCell="{column,record}">
     <span v-if="column.key==='target'">{{label(record.target)}}</span>
     <span v-else-if="column.key==='operation'">{{record.name}} · {{operations[record.operation] || (record.kind==='COMPANY' ? '公司' : record.kind==='MEMBER' ? '人员' : '类别')}}<small v-if="record.imported">老财务记录</small></span>
     <span v-else-if="column.key==='state'"><a-tag :color="stateColors[record.state]">{{states[record.state] || record.state}}</a-tag><small v-if="record.reason">{{syncReasons[record.reason] || record.reason}}</small></span>
     <span v-else-if="column.key==='effective'">{{formatTime(record.effectiveAt)}}</span>
     <a-button v-else-if="column.key==='actions' && canWrite && !record.imported && record.kind==='MEMBER' && record.operation!=='CREATE' && record.attempts===0 && record.state==='PENDING' && Date.parse(record.effectiveAt)>Date.now()" size="small" @click="submit('cancel',{messageId:record.id})">取消计划</a-button>
     <a-button v-else-if="column.key==='actions' && canWrite && record.canRetry" size="small" :disabled="busy" @click="submit('retry',{messageId:record.id,expectedAttempts:record.attempts})">重试</a-button>
    </template>
    <template #emptyText>{{ error ? '同步记录读取失败' : personId ? '此人暂无同步记录' : company ? '此公司暂无同步记录' : '暂无人员或组织同步记录' }}</template>
   </a-table>
   <a v-if="personId" href="#organization?tab=sync&view=records" @click="emit('close')">查看全部人员组织同步记录</a>
  </div>
 </component>
</template>

<style scoped>
.business-use {display:flex;align-items:center;gap:12px;margin:8px 0}
.dispatch-content { display: grid; grid-template-columns: minmax(0,1fr); gap: 14px; min-width: 0; }
.records > .dispatch-content { padding: 16px; }
.dispatch-table-head, .dispatch-table-head > div { display: flex; align-items: center; gap: 8px; }
.dispatch-table-head { justify-content: space-between; }
.dispatch-table-head h3 { margin: 0; font-size: 15px; }
.record-total { color: var(--muted); font-size: 13px; }
.dispatch-content > .ant-btn { justify-self: start; }
</style>

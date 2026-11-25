<script setup>
import { computed, nextTick, ref, useId, watch } from 'vue'
import { Drawer } from 'ant-design-vue'
import { syncReasons } from './organization-sync-reasons'
import { ReloadOutlined } from '@ant-design/icons-vue'
import OrganizationDispatchProfile from './OrganizationDispatchProfile.vue'
import OrganizationPersonSourceAccounts from './OrganizationPersonSourceAccounts.vue'
import OrganizationDispatchAccounts from './OrganizationDispatchAccounts.vue'
import OrganizationSourceRelations from './OrganizationSourceRelations.vue'
const props=defineProps({api:Function,company:Object,memberCompany:Object,personId:Number,personName:String,canWrite:Boolean,formatTime:Function,embedded:Boolean,inAccountWorkspace:Boolean})
const emit=defineEmits(['close','changed'])
const targetFilterID=useId()
const companyView=ref('settings')
const companyViews=[{value:'settings',label:'同步设置'},{value:'history',label:'下发记录'},{value:'profile',label:'公司资料'}]
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const label=key=>systems.find(o=>o.value===key)?.label || key
const states={APPLIED:'已送达',PENDING:'待发送',PROCESSING:'发送中',CONFLICT:'需核对',RETRYABLE:'等待重试',STOPPED:'已停止',WAIT_CATEGORY:'等待类别',WAIT_PARENT_COMPANY:'等待上级公司'}
const operations={CREATE:'创建身份',ENABLE:'启用',DISABLE:'停用',SET_OWNER:'设负责人',UNSET_OWNER:'移交负责人资格',TRANSFER:'调动',RENAME:'同步姓名',RECONCILE:'纠正身份'}
const columns=[{title:'目标系统',key:'target',width:105},{title:'业务变更',key:'operation',width:180},{title:'状态 / 原因',key:'state',width:200},{title:'生效时间',key:'effective',width:154},{title:'回执编号',dataIndex:'externalId',width:140},{title:'',key:'actions',width:80}]
const historyColumns=computed(()=>{
 if(!props.company)return columns
 return columns.filter(column=>column.key!=='operation' &&
  (column.key!=='actions' || props.canWrite && data.value?.items.some(record=>record.canRetry)))
})
const stateColors={APPLIED:'green',PROCESSING:'blue',CONFLICT:'red',RETRYABLE:'orange'}
const loading=ref(false)
const channels=ref(), channelError=ref(false)
async function readChannels(){
 channelError.value=false
 try{channels.value=await props.api('/organization-dispatch/channels')}
 catch{channelError.value=true}
}
if(props.personId || props.company)readChannels()
const targetsChanged=computed(()=>!!data.value && (targets.value.length!==data.value.targets.length || targets.value.some(target=>!data.value.targets.includes(target))))
const companyChannelsOff=computed(()=>systems.filter(system=>channels.value?.[system.value]===false))
const companyTargets=computed(()=>systems.map(system=>({
 ...system,status:data.value?.targetStatus?.find(status=>status.target===system.value),
})))
function toggleTarget(target,checked){
 targets.value=checked ? [...targets.value,target] : targets.value.filter(value=>value!==target)
}
const historyHeading=ref(), detailsOpen=ref(false)
async function openHistory(){
 detailsOpen.value=true
 await nextTick()
 historyHeading.value?.scrollIntoView({block:'start'})
 historyHeading.value?.focus({preventScroll:true})
}
function resetTargets(){
 targets.value=[...data.value.targets]
}
function showHistory(message){
 notice.value=message
 targetFilter.value=''
 issuesOnly.value=false
 page.value=1
 refresh()
 emit('changed')
}
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
const companyName=computed(()=>data.value?.profile?.name || props.company?.name || '公司')
const title=computed(()=>props.company ? companyName.value+' · 公司同步' : props.personId ? (props.personName ? props.personName+' · 人员同步' : '人员同步') : '组织同步记录')
let seq=0
async function refresh(){
 const req=++seq,params=new URLSearchParams({page:page.value});if(props.company)params.set('companyId',props.company.id);if(props.personId)params.set('personId',props.personId)
 loading.value=true
 if(targetFilter.value)params.set('target',targetFilter.value);if(issuesOnly.value)params.set('issues','true')
 try{error.value='';const value=await props.api('/organization-dispatch?'+params);if(req===seq){
  if(!data.value || !targetsChanged.value)targets.value=[...value.targets]
  else value.targets=data.value.targets
  data.value=value
 }}
 catch(e){if(req===seq)error.value=e.message}
 finally{if(req===seq)loading.value=false}
}
watch([targetFilter,issuesOnly],()=>{if(page.value!==1)page.value=1;else refresh()})
watch(()=>[props.personId,props.company?.id,page.value],refresh,{immediate:true})
async function submit(path,body){
 busy.value=true;error.value=''
 try{
  const saved=await props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(body)})
  if(path==='targets'){
   data.value={...data.value,targets:saved};targets.value=[...saved]
   notice.value='同步目标已更新。'
  }else{
   notice.value=path==='retry' ? '已提交重试，请查看下发状态。' : '已取消计划。'
  }
  await refresh()
  emit('changed')
 }
 catch(e){error.value=e.message}finally{busy.value=false}
}
defineExpose({refresh})
</script>
<template>
 <component :is="embedded ? 'section' : Drawer" :class="{'records':embedded && !inAccountWorkspace}"
   v-bind="embedded ? {} : {open:true,title,width:company ? 'min(760px, 100vw)' : 'min(880px, 100vw)'}" @close="emit('close')">
  <div class="dispatch-content">
   <a-alert v-if="error" :message="error" type="error" show-icon>
    <template v-if="company && !data" #action><a-button size="small" @click="refresh">重试</a-button></template>
   </a-alert>
   <a-alert v-if="notice" :message="notice" type="success" show-icon closable @close="notice=''">
    <template v-if="personId" #action>
     <a-button size="small" @click="openHistory">查看记录</a-button>
    </template>
   </a-alert>
   <a-alert v-if="channelError" type="warning" message="暂时无法读取下发通道">
    <template #action><a-button size="small" @click="readChannels">重试</a-button></template>
   </a-alert>
   <a-spin v-if="company && !data && loading" tip="正在读取公司同步资料"/>
   <template v-if="company && data">
    <a-radio-group v-model:value="companyView" class="company-sync-nav" :options="companyViews"
     option-type="button" button-style="solid" role="radiogroup" aria-label="公司同步视图"/>
    <section v-if="companyView==='settings'" class="company-sync" aria-label="公司同步目标">
     <table class="company-targets">
      <thead><tr><th scope="col">同步目标</th><th scope="col">最近同步结果</th></tr></thead>
      <tbody>
       <tr v-for="{value,label,status} in companyTargets" :key="value">
        <td>
         <a-checkbox :checked="targets.includes(value)" :disabled="!canWrite || busy"
          @change="toggleTarget(value,$event.target.checked)">{{label}}</a-checkbox>
        </td>
        <td>
         <template v-if="status">
          <div class="target-result">
           <a-tag :color="stateColors[status.state]">{{states[status.state] || '待同步'}}</a-tag>
           <small v-if="status.externalId">回执 {{status.externalId}}</small>
          </div>
          <small v-if="status.reason" class="target-reason">{{syncReasons[status.reason] || status.reason}}</small>
         </template>
         <span v-else class="target-reason">—</span>
        </td>
       </tr>
      </tbody>
     </table>
     <p v-if="companyChannelsOff.length" class="channel-note"><strong>下发通道未开启</strong>
      <span>{{companyChannelsOff.length===systems.length ? '全部业务系统' : companyChannelsOff.map(system=>system.label).join('、')}}</span>
      <span v-if="targets.some(target=>channels?.[target]===false && !data.targets.includes(target))">新增目标将等待发送。</span>
     </p>
     <a-space v-if="canWrite && targetsChanged" class="company-sync-actions" wrap>
      <a-button :disabled="busy" @click="resetTargets">撤销修改</a-button>
      <a-button type="primary" :loading="busy" @click="submit('targets',{companyId:company.id,targets,expectedTargets:data.targets})">保存同步目标</a-button>
     </a-space>
    </section>
    <template v-else-if="companyView==='profile'">
     <section class="company-facts" aria-label="公司资料">
      <dl>
       <div><dt>上级销售公司</dt><dd>{{data.profile?.parentName || '—'}}</dd></div>
       <div v-if="data.profile?.status"><dt>公司状态</dt><dd>{{data.profile.status==='ACTIVE' ? '启用' : '停用'}}</dd></div>
      </dl>
      <a-button v-if="canWrite && data.wireId" @click="editProfile=true">编辑公司资料</a-button>
     </section>
     <OrganizationDispatchProfile v-if="editProfile" :api="api" :company="company" :profile="data.profile" @close="editProfile=false" @saved="editProfile=false;refresh();emit('changed')"/>
     <section class="company-uses" aria-label="业务系统使用关系">
      <h3>业务系统使用关系</h3>
      <p v-if="!activeUses.length" class="target-reason">暂无正在使用此公司的业务系统。</p>
      <table v-else class="business-use">
       <thead><tr><th scope="col">业务系统</th><th scope="col">公司编号</th><th v-if="canWrite" scope="col"></th></tr></thead>
       <tbody>
        <tr v-for="row in activeUses" :key="row.source+':'+row.externalId">
         <td>{{label(row.source)}}</td><td class="numeric">{{row.externalId}}</td>
         <td v-if="canWrite"><a-button type="text" size="small" @click="confirmCloseUse(row)">停止使用</a-button></td>
        </tr>
       </tbody>
      </table>
      <details v-if="otherUses.length" class="company-use-history">
       <summary>其他使用区间（{{otherUses.length}}）</summary>
       <p v-for="row in otherUses" :key="[row.source,row.externalId,row.validFrom,row.validTo].join(':')">
        {{label(row.source)}} · {{row.externalId}} · {{formatTime(row.validFrom)}} 至 {{row.validTo ? formatTime(row.validTo) : '长期有效'}}
       </p>
      </details>
      <a-modal v-if="closingUse" open :title="'停止'+label(closingUse.source)+'使用此公司？'" ok-text="确认停止使用"
       :ok-button-props="{danger:true}" :confirm-loading="busy" :closable="!busy" :keyboard="!busy" :mask-closable="false" @ok="closeUse" @cancel="!busy && (closingUse=undefined)">
       <p>{{companyName}} 在{{label(closingUse.source)}}的公司编号 {{closingUse.externalId}} 将结束当前归属。历史订单按原生效区间核对。</p>
       <a-alert v-if="closingUse.error" type="error" :message="closingUse.error" show-icon/>
      </a-modal>
     </section>
     <OrganizationSourceRelations :api="api" :company-id="company.id" :format-time="formatTime"/>
    </template>
   </template>
   <OrganizationDispatchAccounts v-if="personId && data" :person-id="personId" :person-name="personName" :company="memberCompany"
    :api="api" :systems="systems" :can-write="canWrite" :bindings="data.bindings" :channels="channels" :compact="inAccountWorkspace" @changed="showHistory"/>
   <a-button v-if="inAccountWorkspace && (data?.total || detailsOpen)" type="link" size="small"
     class="dispatch-history-link" :aria-expanded="detailsOpen" @click="detailsOpen=!detailsOpen">下发记录</a-button>
   <component v-if="company ? companyView==='history' : !inAccountWorkspace || detailsOpen"
     :is="!inAccountWorkspace && personId ? 'details' : 'div'" class="dispatch-details"
     :open="!inAccountWorkspace && personId ? detailsOpen : undefined" @toggle="detailsOpen=$event.target.open">
    <summary v-if="!inAccountWorkspace && personId">
     账号与下发记录<span v-if="data?.total && !targetFilter && !issuesOnly" class="record-total">{{data.total}} 条下发记录</span>
    </summary>
    <div v-if="detailsOpen || !personId" class="dispatch-details-body">
     <OrganizationPersonSourceAccounts v-if="personId && detailsOpen && !inAccountWorkspace" :key="personId" :api="api" :person-id="personId" :systems="systems" :format-time="formatTime"/>
    <div class="dispatch-table-head">
      <div><h3 ref="historyHeading" tabindex="-1">{{personId ? '销售身份下发记录' : embedded ? '下发记录' : '同步记录'}}</h3><span v-if="data && !error" class="record-total">{{ data.total }} 条</span></div>
      <a-space>
        <a v-if="embedded && !personId" class="context-link" href="#organization?tab=health">AI 回传冲突</a>
        <a-button size="small" :loading="loading" @click="refresh"><template #icon><ReloadOutlined aria-hidden="true"/></template>刷新</a-button>
      </a-space>
    </div>
    <a-space v-if="!personId || data?.total || targetFilter || issuesOnly" wrap>
     <label class="sr-only" :for="targetFilterID">筛选目标系统</label>
     <a-select :id="targetFilterID" v-model:value="targetFilter" style="min-width:132px" :options="[{value:'',label:'全部目标系统'},...systems]"/>
     <a-checkbox v-model:checked="issuesOnly">只看异常记录</a-checkbox>
    </a-space>
    <p v-if="personId && data?.total===0 && !loading && !error" class="dispatch-empty" role="status">
     {{targetFilter || issuesOnly ? '当前筛选条件下没有下发记录。' : '暂无下发记录。'}}
    </p>
    <a-table v-else size="small" row-key="id" :loading="loading" :columns="historyColumns" :data-source="data?.items || []"
     :class="{'company-history-table':company}" :scroll="company ? undefined : {x:860}"
     :pagination="{current:page,total:data?.total || 0,pageSize:25,showSizeChanger:false,hideOnSinglePage:true}" @change="page=$event.current">
     <template #bodyCell="{column,record}">
      <span v-if="column.key==='target'">{{label(record.target)}}</span>
      <span v-else-if="column.key==='operation'">{{record.name}} · {{operations[record.operation] || (record.kind==='COMPANY' ? '公司' : record.kind==='MEMBER' ? '人员' : '类别')}}</span>
      <span v-else-if="column.key==='state'"><a-tag :color="stateColors[record.state]">{{states[record.state] || record.state}}</a-tag><small v-if="record.reason">{{syncReasons[record.reason] || record.reason}}</small></span>
      <span v-else-if="column.key==='effective'">{{formatTime(record.effectiveAt)}}</span>
      <a-button v-else-if="column.key==='actions' && canWrite && !record.imported && record.kind==='MEMBER' && record.operation!=='CREATE' && record.attempts===0 && record.state==='PENDING' && Date.parse(record.effectiveAt)>Date.now()" size="small" @click="submit('cancel',{messageId:record.id})">取消计划</a-button>
      <a-button v-else-if="column.key==='actions' && canWrite && record.canRetry" size="small" :disabled="busy" @click="submit('retry',{messageId:record.id,expectedAttempts:record.attempts})">重试</a-button>
     </template>
      <template #emptyText>{{ error ? '同步记录读取失败' : company && (targetFilter || issuesOnly) ? '当前筛选条件下没有下发记录' : personId ? '此人暂无同步记录' : company ? '此公司暂无同步记录' : '暂无人员或组织同步记录' }}</template>
    </a-table>
    </div>
   </component>
  </div>
 </component>
</template>

<style scoped>
.company-sync-nav {justify-self:start}
.company-sync {display:grid;gap:12px}
.company-targets {width:100%;table-layout:fixed;border:1px solid var(--line);border-spacing:0;border-radius:7px;overflow:hidden}
.company-targets th,.company-targets td {padding:10px 14px;text-align:left;vertical-align:middle}
.company-targets th {background:var(--surface,#f5f7fb);color:var(--muted);font-size:13px;font-weight:600}
.company-targets th:first-child {width:36%}
.company-targets td {border-top:1px solid var(--line);height:52px}
.target-result {display:flex;align-items:center;gap:6px 10px;flex-wrap:wrap;color:var(--muted)}
.target-result :deep(.ant-tag) {margin-inline-end:0}
.target-reason {display:block;color:var(--muted)}
.target-result + .target-reason {margin-top:3px}
.channel-note {display:flex;align-items:baseline;gap:2px 10px;flex-wrap:wrap;margin:0;padding:9px 12px;border-radius:6px;background:var(--surface,#f5f7fb);color:var(--muted);font-size:13px}
.channel-note strong {color:var(--ink)}
.company-sync-actions {justify-self:end}
.company-facts {display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:12px;padding:12px 14px;background:var(--surface,#f5f7fb);border-radius:7px}
.company-facts dl {display:flex;flex-wrap:wrap;gap:8px 32px;margin:0}
.company-facts dl > div {display:grid;gap:3px}
.company-facts dt {color:var(--muted);font-size:13px}
.company-facts dd {margin:0;font-weight:600;overflow-wrap:anywhere}
.company-uses {border:1px solid var(--line);border-radius:7px;overflow:hidden}
.company-uses h3 {margin:0;padding:10px 14px;background:var(--surface,#f5f7fb);font-size:14px}
.company-uses > p,.company-use-history {margin:0;padding:10px 14px}
.company-use-history {border-top:1px solid var(--line);color:var(--muted)}
.company-use-history summary {cursor:pointer}
.company-use-history p {margin-block:6px 0}
.business-use {width:100%;border-spacing:0;table-layout:fixed;text-align:left}
.business-use th,.business-use td {padding:8px 14px;border-top:1px solid var(--line)}
.business-use th {color:var(--muted);font-size:13px;font-weight:600}
.business-use th:last-child:empty {width:100px}
.business-use td:has(.ant-btn) {text-align:right}
.business-use .numeric {overflow-wrap:anywhere}
.company-history-table :deep(.ant-table-cell) {overflow-wrap:anywhere}
.company-history-table :deep(.ant-table-cell small) {display:block;margin-top:3px;color:var(--muted)}
.dispatch-details:is(details) {border:1px solid var(--line);padding:10px 12px;border-radius:6px}
.dispatch-details summary {cursor:pointer;font-weight:600}
.dispatch-details summary .record-total {margin-left:8px;font-weight:400}
.dispatch-details[open] summary {margin-bottom:12px}
.dispatch-details-body {display:grid;grid-template-columns:minmax(0,1fr);gap:12px;min-width:0}
.dispatch-details-body > .ant-btn {justify-self:start}
.dispatch-content { display: grid; grid-template-columns: minmax(0,1fr); gap: 14px; min-width: 0; }
.records > .dispatch-content { padding: 16px; }
.dispatch-history-link {justify-self:start;padding-inline:0}
.dispatch-table-head, .dispatch-table-head > div { display: flex; align-items: center; gap: 8px; }
.dispatch-table-head { justify-content: space-between; }
.dispatch-table-head h3 { margin: 0; font-size: 15px; }
.dispatch-empty {margin:0;padding:12px 14px;background:var(--surface,#f7f8fa);color:var(--muted);border-radius:6px}
.record-total { color: var(--muted); font-size: 13px; }
.dispatch-content > .ant-btn { justify-self: start; }
@media(max-width:520px){
 .company-targets th,.company-targets td,.business-use th,.business-use td {padding-inline:8px}
 .company-history-table :deep(.ant-table-cell) {padding-inline:5px !important}
 .company-history-table :deep(col) {width:auto !important}
}
</style>

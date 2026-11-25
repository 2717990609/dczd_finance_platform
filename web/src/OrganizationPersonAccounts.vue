<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import OrganizationSourceRelations from './OrganizationSourceRelations.vue'

const props=defineProps({api:Function,person:Object,canWrite:Boolean,formatTime:Function,formatIdentityTime:Function})
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const systemName=source=>systems.find(system=>system.value===source)?.label || source
const canEdit=computed(()=>props.canWrite && props.person.sourceStatus!=='INACTIVE')
const rows=ref({total:0,items:[]}),page=ref(1),salesOpen=ref(false),recordsOpen=ref(false)
const loading=ref(false),loadError=ref(''),notice=ref('')
const editor=ref(),formError=ref(''),checking=ref(false),saving=ref(false)
const history=ref(),historyError=ref(''),historyLoading=ref(false)
const records=ref({total:0,items:[]}),recordsPage=ref(1),recordsLoading=ref(false),recordsError=ref('')
const scheduledChange=ref(false)
let listRequest=0,lookupRequest=0,historyRequest=0,recordsRequest=0
const financeStatus={ACTIVE:'已启用',DISABLED:'已停用',UNCONFIGURED:'未开通'}
const latest=computed(()=>editor.value?.matches?.find(row=>!row.validTo))
const lastKnown=computed(()=>latest.value || editor.value?.matches?.[0])
const ready=computed(()=>editor.value?.matches && editor.value.targetPersonId && editor.value.effective
 && latest.value?.personId!==editor.value.targetPersonId && !checking.value)
function beijingTime(value){return (value.length===16 ? value+':00' : value)+'+08:00'}
function status(row){
 if(Date.parse(row.validFrom)>Date.now())return '待生效'
 return row.validTo && Date.parse(row.validTo)<=Date.now() ? '已结束' : '有效'
}
async function refresh(){
 const request=++listRequest
 loading.value=true
 loadError.value=''
 try{
  const result=await readAccounts('current',page.value)
  if(request===listRequest)rows.value=result
 }catch(e){if(request===listRequest)loadError.value=e.message}
 finally{if(request===listRequest)loading.value=false}
}
function readAccounts(scope,page){
 return props.api('/person-source-accounts?'+new URLSearchParams({personId:props.person.id,page,[scope]:'true'}))
}
watch(page,refresh,{immediate:true})
async function loadRecords(){
 const request=++recordsRequest
 recordsLoading.value=true
 recordsError.value=''
 try{
  const result=await readAccounts('history',recordsPage.value)
  if(request===recordsRequest)records.value=result
 }catch(e){if(request===recordsRequest)recordsError.value=e.message}
 finally{if(request===recordsRequest)recordsLoading.value=false}
}
watch([recordsOpen,recordsPage],()=>{if(recordsOpen.value)loadRecords()})
function openRecords(){
 recordsPage.value=1
 recordsOpen.value=true
}
async function accountHistory(source,externalId){
 return props.api('/identities?'+new URLSearchParams({source,externalId}))
}
function resetLookup(){
 lookupRequest++
 checking.value=false
 formError.value=''
 editor.value.matches=undefined
}
async function checkAccount(){
 const form=editor.value
 if(!form.source || !form.externalId.trim() || checking.value)return
 const request=++lookupRequest
 checking.value=true
 formError.value=''
 form.matches=undefined
 try{const matches=await accountHistory(form.source,form.externalId.trim());if(request===lookupRequest)form.matches=matches}
 catch(e){if(request===lookupRequest)formError.value=e.message}
 finally{if(request===lookupRequest)checking.value=false}
}
function openEditor(row){
 notice.value=''
 formError.value=''
 editor.value={source:row?.source,externalId:row?.externalId || '',targetPersonId:row ? undefined : props.person.id,
  effective:'',transfer:!!row}
 if(row)checkAccount()
}
function closeEditor(){
 if(saving.value)return
 lookupRequest++
 checking.value=false
 editor.value=undefined
}
async function save(){
 if(!ready.value || saving.value)return
 const form=editor.value
 if(form.transfer && latest.value?.personId!==props.person.id)return
 formError.value=''
 saving.value=true
 try{
  await props.api('/person-source-accounts',{method:'POST',body:JSON.stringify({
   personId:form.targetPersonId,source:form.source,externalId:form.externalId.trim(),
   validFrom:beijingTime(form.effective),expectedMappingId:latest.value?.id || 0,
  })})
  const transferred=form.targetPersonId!==props.person.id
  editor.value=undefined
  const scheduled=Date.parse(beijingTime(form.effective))>Date.now()
  scheduledChange.value=scheduled && !transferred
  notice.value=scheduled
   ? (transferred ? '已安排账号归属变更。' : '账号归属已保存，等待生效。')
   : (transferred ? '账号归属已变更。' : '账号归属已保存。')
  if(page.value===1)refresh()
  else page.value=1
 }catch(e){formError.value=e.message}
 finally{saving.value=false}
}
async function openHistory(row){
 const request=++historyRequest
 history.value={...row,items:[]}
 historyError.value=''
 historyLoading.value=true
 try{
  const items=await accountHistory(row.source,row.externalId)
  if(request===historyRequest)history.value.items=items
 }catch(e){if(request===historyRequest)historyError.value=e.message}
 finally{if(request===historyRequest)historyLoading.value=false}
}
function closeHistory(){historyRequest++;history.value=undefined}
onBeforeUnmount(()=>{listRequest++;lookupRequest++;historyRequest++;recordsRequest++})
defineExpose({refresh})
</script>

<template>
 <section class="records account-workspace" aria-label="业务账号归属">
  <header class="account-header">
   <h2>业务账号</h2>
   <a-space wrap>
    <a-button :aria-expanded="salesOpen" aria-controls="person-sales-identity" @click="salesOpen=!salesOpen">销售身份</a-button>
    <a-button v-if="canEdit" type="primary" @click="openEditor()">添加账号</a-button>
   </a-space>
  </header>
  <a-alert v-if="notice" type="success" show-icon :message="notice" closable @close="notice=''">
   <template v-if="scheduledChange" #action><a-button size="small" @click="openRecords">查看记录</a-button></template>
  </a-alert>
  <a-alert v-if="loadError" type="error" :message="loadError" show-icon>
   <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <a-spin v-else :spinning="loading">
   <template v-if="rows.items.length">
    <div class="account-table-head" aria-hidden="true"><span>业务系统</span><span>用户 ID</span><span>生效时间</span><span></span></div>
    <ul class="account-list">
     <li v-for="row in rows.items" :key="row.origin+':'+row.id" class="account-row">
      <strong class="account-system">{{systemName(row.source)}}</strong>
      <span class="account-number numeric">{{row.externalId}}</span>
      <small class="account-period">{{formatTime(row.validFrom)}} 起<template v-if="row.validTo"> · {{formatTime(row.validTo)}} 止</template></small>
      <a-space v-if="row.origin==='DIRECTORY'" class="account-row-links" wrap :size="4">
       <a-button v-if="canEdit && !row.validTo" type="link" size="small" :aria-label="'变更账号 '+row.externalId+' 的归属'" @click="openEditor(row)">变更归属</a-button>
       <a-button type="text" size="small" :aria-label="'账号 '+row.externalId+' 的归属历史'" @click="openHistory(row)">历史</a-button>
      </a-space>
      <span v-else class="account-readonly">业务系统维护</span>
     </li>
    </ul>
   </template>
   <p v-else-if="!loading" class="account-empty">暂无有效业务账号</p>
   <a-pagination v-if="rows.total>25" :current="page" :total="rows.total" :page-size="25" :show-size-changer="false" @change="page=$event"/>
  </a-spin>
  <div v-show="salesOpen" id="person-sales-identity" class="account-sales">
   <OrganizationDispatch v-if="salesOpen" embedded in-account-workspace :person-id="person.id" :person-name="person.name"
     :api="api" :can-write="canEdit" :format-time="formatTime" @changed="refresh"/>
  </div>
  <footer class="account-footer">
   <a-button type="link" size="small" @click="openRecords">历史与编号</a-button>
  </footer>
 </section>

 <a-modal v-if="recordsOpen" open title="账号历史与编号" :width="640" :footer="null" @cancel="recordsOpen=false">
  <section class="account-records" aria-label="历史或待生效归属">
   <h3>历史或待生效归属</h3>
   <a-alert v-if="recordsError" type="error" :message="recordsError" show-icon>
    <template #action><a-button size="small" @click="loadRecords">重试</a-button></template>
   </a-alert>
   <a-spin v-else :spinning="recordsLoading">
    <ul v-if="records.items.length" class="account-list">
     <li v-for="row in records.items" :key="row.origin+':'+row.id" class="record-row">
      <div>
       <strong>{{systemName(row.source)}} · {{row.externalId}}</strong>
       <small>{{formatIdentityTime(row.validFrom)}}<template v-if="row.validTo"> 至 {{formatIdentityTime(row.validTo)}}</template></small>
      </div>
      <a-tag>{{status(row)}}</a-tag>
     </li>
    </ul>
    <p v-else-if="!recordsLoading" class="account-empty">暂无记录</p>
    <a-pagination v-if="records.total>25" v-model:current="recordsPage" :total="records.total" :page-size="25" :show-size-changer="false"/>
   </a-spin>
  </section>
  <OrganizationSourceRelations account-records :api="api" :person-id="person.id" :format-time="formatTime"/>
  <p v-if="financeStatus[person.accountStatus]" class="finance-login">
   财务登录账号 · {{financeStatus[person.accountStatus]}}<template v-if="person.ownLogin"> · {{person.ownLogin}}</template>
  </p>
 </a-modal>

 <a-modal v-if="editor" open :title="editor.transfer ? '变更账号归属' : '添加账号'" :width="520"
   :ok-text="latest && latest.personId!==editor.targetPersonId ? '确认变更归属' : '保存归属'"
   :ok-button-props="{disabled:!ready || editor.transfer && latest?.personId!==person.id}"
   :confirm-loading="saving" :cancel-button-props="{disabled:saving}" :closable="!saving" :keyboard="!saving" :mask-closable="false"
   @ok="save" @cancel="closeEditor">
  <a-alert v-if="formError" type="error" :message="formError" show-icon/>
  <a-form layout="vertical" :disabled="saving">
   <div v-if="editor.transfer" class="account-header">
    <p>{{systemName(editor.source)}} · {{editor.externalId}}</p>
    <a-button v-if="formError" type="link" size="small" @click="checkAccount">重新核对</a-button>
   </div>
   <template v-else>
    <a-form-item label="业务系统" html-for="account-source" required>
     <a-select id="account-source" v-model:value="editor.source" :options="systems" placeholder="选择系统" @change="resetLookup"/>
    </a-form-item>
    <a-form-item label="来源用户 ID" html-for="account-external-id" required>
     <a-space-compact block>
      <a-input id="account-external-id" v-model:value="editor.externalId" :maxlength="128" @update:value="resetLookup" @press-enter="checkAccount"/>
      <a-button :loading="checking" :disabled="!editor.source || !editor.externalId.trim()" @click="checkAccount">核对</a-button>
     </a-space-compact>
    </a-form-item>
   </template>
   <a-spin :spinning="checking">
    <template v-if="editor.matches">
     <a-alert v-if="editor.transfer && latest?.personId!==person.id" type="warning" message="该账号的最新归属已变化，请关闭后刷新核对。"/>
     <a-alert v-else-if="latest?.personId===editor.targetPersonId" type="info" message="此账号已归属当前人员。"/>
     <template v-else>
      <p v-if="lastKnown">最近归属：{{lastKnown.personName}} · {{formatIdentityTime(lastKnown.validFrom)}} 起
       <template v-if="lastKnown.validTo"> · {{formatIdentityTime(lastKnown.validTo)}} 结束</template>
      </p>
      <p v-else>无财务归属记录。</p>
      <a-form-item v-if="editor.transfer" label="新归属人员" html-for="next-account-owner" required>
       <AppointmentPersonSelect id="next-account-owner" v-model:value="editor.targetPersonId" :api="api"/>
      </a-form-item>
      <p v-else>归属人员：{{person.name}}</p>
      <a-form-item label="生效时间（北京时间）" html-for="source-account-effective" required>
       <a-input id="source-account-effective" type="datetime-local" v-model:value="editor.effective"/>
      </a-form-item>
     </template>
    </template>
   </a-spin>
  </a-form>
 </a-modal>

 <a-modal v-if="history" open :title="systemName(history.source)+' · '+history.externalId+' 的归属历史'" :footer="null" @cancel="closeHistory">
  <a-alert v-if="historyError" type="error" :message="historyError" show-icon>
   <template #action><a-button size="small" @click="openHistory(history)">重试</a-button></template>
  </a-alert>
  <a-spin v-else :spinning="historyLoading">
   <ul class="account-list">
    <li v-for="row in history.items" :key="row.id" class="record-row">
     <div><strong>{{row.personName}}</strong><small>{{formatIdentityTime(row.validFrom)}} 至 {{row.validTo?formatIdentityTime(row.validTo):'长期有效'}}</small></div>
     <a-tag>{{status(row)}}</a-tag>
    </li>
   </ul>
  </a-spin>
 </a-modal>
</template>

<style scoped>
.account-workspace {padding:14px 16px 8px}
.account-header,.record-row {display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px 12px}
.account-header h2,.account-list,.account-records h3 {margin:0}
.account-header h2,.account-records h3 {font-size:16px}
.account-workspace > .ant-alert {margin-top:12px}
.account-table-head,.account-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr) minmax(0,1.5fr) 142px;gap:8px 14px;align-items:center;padding:10px 12px}
.account-table-head {margin-top:12px;background:var(--surface,#f5f7fb);border-radius:4px;color:var(--muted);font-size:13px}
.account-table-head > :last-child,.account-row > :last-child {justify-self:end}
.account-list {list-style:none;padding:0}
.account-row,.record-row {border-bottom:1px solid var(--line);overflow-wrap:anywhere}
.account-list small,.account-empty,.account-readonly,.finance-login {color:var(--muted)}
.account-list small {display:block}
.account-row-links :deep(.ant-btn) {padding-inline:4px}
.account-readonly {font-size:12px}
.account-empty {margin:12px 0}
.account-footer {padding-top:6px}
.account-footer .ant-btn {padding-inline:0}
.account-records {margin-bottom:16px}
.record-row {padding:9px 0}
.account-list :deep(.ant-tag) {margin-inline-end:0}
.account-sales {margin-top:10px;padding:12px;border-top:1px solid var(--line);background:var(--surface,#f5f7fb)}
.finance-login {margin:12px 0 0}
@media (max-width:590px) {
 .account-workspace {padding:10px 12px 6px}
 .account-table-head {display:none}
 .account-row {grid-template-columns:minmax(0,1fr) minmax(0,1fr) auto;gap:4px 8px;padding:10px 0}
 .account-row:first-child {margin-top:8px;border-top:1px solid var(--line)}
 .account-period {grid-column:1/3;grid-row:2}
 .account-row > :last-child {grid-column:3;grid-row:1/3}
 .account-sales {padding:10px 0}
}
</style>

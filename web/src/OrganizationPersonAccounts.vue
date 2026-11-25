<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import OrganizationSourceRelations from './OrganizationSourceRelations.vue'

const props=defineProps({api:Function,person:Object,canWrite:Boolean,formatTime:Function,formatIdentityTime:Function})
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const systemName=source=>systems.find(system=>system.value===source)?.label || source
const canEdit=computed(()=>props.canWrite && props.person.sourceStatus!=='INACTIVE')
const rows=ref({total:0,items:[]}),page=ref(1),showAll=ref(false),salesOpen=ref(false)
const loading=ref(false),loadError=ref(''),notice=ref('')
const editor=ref(),formError=ref(''),checking=ref(false),saving=ref(false)
const history=ref(),historyError=ref(''),historyLoading=ref(false)
let listRequest=0,lookupRequest=0,historyRequest=0
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
 const params=new URLSearchParams({personId:props.person.id,page:page.value})
 if(!showAll.value)params.set('current','true')
 try{
  const result=await props.api('/person-source-accounts?'+params)
  if(request===listRequest)rows.value=result
 }catch(e){if(request===listRequest)loadError.value=e.message}
 finally{if(request===listRequest)loading.value=false}
}
watch([page,showAll],refresh,{immediate:true})
function setHistory(event){
 page.value=1
 showAll.value=event.target.checked
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
  notice.value=scheduled
   ? (transferred ? '已安排账号归属变更。' : '账号归属已保存，等待生效。')
   : (transferred ? '账号归属已变更。' : '账号归属已保存。')
  if(page.value===1 && (!scheduled || showAll.value))refresh()
  else {
   page.value=1
   if(scheduled)showAll.value=true
  }
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
onBeforeUnmount(()=>{listRequest++;lookupRequest++;historyRequest++})
defineExpose({refresh})
</script>

<template>
 <section class="records account-workspace" aria-label="业务账号归属">
  <header class="account-header">
   <h2>业务账号归属</h2>
   <a-button v-if="canEdit" type="primary" @click="openEditor()">添加账号归属</a-button>
  </header>
  <p>按业务系统的用户 ID 关联财务人员，用于匹配来源订单。</p>
  <a-alert v-if="notice" type="success" show-icon :message="notice" closable @close="notice=''"/>
  <a-alert v-if="loadError" type="error" :message="loadError" show-icon>
   <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <a-spin v-else :spinning="loading">
   <ul v-if="rows.items.length" class="account-list">
    <li v-for="row in rows.items" :key="row.origin+':'+row.id">
     <div>
      <div class="account-name">
       <strong>{{systemName(row.source)}} · <span class="numeric">{{row.externalId}}</span></strong>
       <a-tag v-if="showAll" :color="status(row)==='有效'?'green':undefined">{{status(row)}}</a-tag>
      </div>
      <small>{{formatIdentityTime(row.validFrom)}} 起<template v-if="row.validTo"> · {{formatIdentityTime(row.validTo)}} 止</template></small>
     </div>
     <a-space v-if="row.origin==='DIRECTORY'" wrap>
      <a-button type="link" size="small" :aria-label="'查看账号 '+row.externalId+' 的归属历史'" @click="openHistory(row)">归属历史</a-button>
      <a-button v-if="canEdit && !row.validTo && status(row)==='有效'" size="small"
        :aria-label="'变更账号 '+row.externalId+' 的归属'" @click="openEditor(row)">变更归属</a-button>
     </a-space>
    </li>
   </ul>
   <p v-else-if="!loading">{{showAll ? '暂无账号归属记录。' : '暂无当前有效的账号归属。'}}</p>
   <a-pagination v-if="rows.total>25" :current="page" :total="rows.total" :page-size="25" :show-size-changer="false" @change="page=$event"/>
  </a-spin>
  <a-checkbox :checked="showAll" @change="setHistory">查看全部记录</a-checkbox>
 </section>

 <details class="records account-disclosure" @toggle="salesOpen=$event.target.open">
  <summary>子系统销售身份</summary>
  <OrganizationDispatch v-if="salesOpen" embedded source-accounts-in-profile :person-id="person.id" :person-name="person.name"
    :api="api" :can-write="canEdit" :format-time="formatTime" @changed="refresh"/>
 </details>
 <div v-if="financeStatus[person.accountStatus]" class="finance-login">
  财务登录账号 · {{financeStatus[person.accountStatus]}}<template v-if="person.ownLogin"> · {{person.ownLogin}}</template>
 </div>
 <OrganizationSourceRelations :api="api" :person-id="person.id" :format-time="formatTime"/>

 <a-modal v-if="editor" open :title="editor.transfer ? '变更账号归属' : '添加账号归属'" :width="520"
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
    <li v-for="row in history.items" :key="row.id">
     <div><strong>{{row.personName}}</strong><small>{{formatIdentityTime(row.validFrom)}} 至 {{row.validTo?formatIdentityTime(row.validTo):'长期有效'}}</small></div>
     <a-tag>{{status(row)}}</a-tag>
    </li>
   </ul>
  </a-spin>
 </a-modal>
</template>

<style scoped>
.account-workspace,.account-disclosure {margin-bottom:12px;padding:14px 16px}
.account-workspace {display:grid;gap:10px}
.account-header,.account-list li,.account-name {display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:6px 12px}
.account-header h2,.account-workspace p,.account-list {margin:0}
.account-header h2 {font-size:16px}
.account-workspace p,.account-list small {color:var(--muted)}
.account-list {list-style:none;padding:0}
.account-list li {border-top:1px solid var(--line);padding:10px 0;overflow-wrap:anywhere}
.account-list small {display:block}
.account-list :deep(.ant-tag) {margin-inline-end:0}
.account-disclosure summary {cursor:pointer;font-weight:600}
.account-disclosure[open] summary {margin-bottom:10px}
.account-disclosure :deep(.records) {border:0;background:transparent}
.account-disclosure :deep(.dispatch-content) {padding:0}
.finance-login {padding:0 2px;color:var(--muted)}
</style>

<script setup>
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import { computed, ref, watch } from 'vue'
const props=defineProps({api:Function,personId:Number,personName:String,company:Object,existing:Boolean,channelStatus:Object,existingBindings:Array})
const emit=defineEmits(['close','saved'])
const existingId=ref(),chosenCompany=ref(props.company?.id),companyOptions=ref([]),role=ref('MEMBER'),start=ref('')
const membership=computed(()=>!props.personId && chosenCompany.value)
let companySeq=0
async function findCompany(name=''){
 const seq=++companySeq
 try{const data=await props.api('/organization-sales-companies?'+new URLSearchParams({name}));if(seq===companySeq)companyOptions.value=data.items.filter(c=>c.status==='ACTIVE').map(c=>({value:c.id,label:c.name}))}
 catch(e){if(seq===companySeq)error.value=e.message}
}
const name=ref(''),rows=ref([]),busy=ref(false),error=ref(''),requestId=ref(crypto.randomUUID())
const systems=[{value:'DCZD',label:'大春之道'},{value:'DCJK',label:'大春见康'},{value:'DCAI',label:'大春 AI'}]
const states={ACTIVE:'已启用',INACTIVE:'已停用',NONE:'暂无销售身份'}
const standaloneChannels=ref()
const channels=computed(()=>props.channelStatus ?? standaloneChannels.value)
const systemOptions=computed(()=>systems.map(system=>({
 ...system,disabled:channels.value?.[system.value]===false,
 label:channels.value?.[system.value]===false ? system.label+'（通道未开启）' : system.label,
})))
const systemName=target=>systems.find(system=>system.value===target)?.label || target
const accountAction=row=>{
 if(row.account.identity_state==='NONE') return row.owner ? '下发创建销售身份并设为负责人' : '下发创建销售身份'
 if(row.owner) return '绑定现有身份并下发负责人变更'
 return '绑定现有销售身份'
}
async function loadChannels(){
 if(props.channelStatus || standaloneChannels.value)return
 try{standaloneChannels.value=await props.api('/organization-dispatch/channels')}
 catch{ /* Account lookup also reports connection errors. */ }
}
const errors={NOT_FOUND:'该系统未查到已注册账号',CONFLICT:'该手机号匹配多个账号，请先核对',UNAVAILABLE:'来源账号暂不可用'}
const canSave=computed(()=>(props.personId ? rows.value.length>0 : props.existing ? !!existingId.value : !!name.value.trim()) &&
 rows.value.every(row=>row.account && !row.loading && (!needsCompany(row) || row.companyId)))
function add(){
 const available=systemOptions.value.filter(system=>!system.disabled)
 const target=available.length===1 ? available[0].value : undefined
 rows.value.push({key:crypto.randomUUID(),target,countryCode:'+86',phone:'',lookup:0,companySearch:0,companies:[]})
 loadChannels()
}
function needsCompany(row){return row.account.identity_state==='NONE' || row.owner}
function lookupAvailable(row){return !!row.target && channels.value?.[row.target]!==false && !!row.phone.trim() && !row.loading}
function reset(row){row.lookup++;row.companySearch++;row.account=undefined;row.owner=false;row.companyId=membership.value || undefined;row.companies=[];row.error='';row.loading=false}
async function lookup(row) {
 if(!lookupAvailable(row))return
 reset(row)
 const seq=row.lookup
 row.loading=true
 try{
  const result=await props.api('/organization-dispatch/lookup',{method:'POST',body:JSON.stringify({target:row.target,phone:row.phone,countryCode:row.countryCode})})
  if(seq!==row.lookup)return
  if(result.status==='FOUND'){row.account=result;if(membership.value&&role.value==='OWNER'&&result.identity_state==='NONE')row.owner=true}
  else row.error=errors[result.status] || '未能核验此账号'
 }catch(e){if(seq===row.lookup)row.error=e.message}
 finally{if(seq===row.lookup)row.loading=false}
}
async function searchCompany(row,name=''){
 const seq=++row.companySearch,sys=row.target
 try{
  const list=await props.api('/organization-dispatch/available-companies?'+new URLSearchParams({target:sys,name}))
  if(seq===row.companySearch&&sys===row.target)row.companies=list
 }catch(e){if(seq===row.companySearch)row.error=e.message}
}
function checkDuplicates(){
 const checked=new Set((props.existingBindings || []).map(binding=>binding.target+':'+binding.userId))
 for(const row of rows.value){
  const key=row.target+':'+row.account.user_id
  if(checked.has(key)){
   row.error='此账号已绑定或已添加，请核对账号。'
   return false
  }
  checked.add(key)
 }
 return true
}
async function save(){
 if(!canSave.value||busy.value||!checkDuplicates())return
 busy.value=true;error.value=''
 try{
  const body={requestId:requestId.value,personId:props.personId || existingId.value,name:props.personId || props.existing?'':name.value.trim(),membershipCompanyId:membership.value || undefined,memberRole:membership.value ? role.value : undefined,startsAt:membership.value && start.value ? start.value+':00+08:00' : undefined,
   bindings:rows.value.map(r=>({target:r.target,countryCode:r.countryCode,phone:r.phone,userId:r.account.user_id,
   companyId:r.companyId,owner:!!r.owner,confirmed:r.account}))}
  const result=await props.api('/organization-dispatch/person-intake',{method:'POST',body:JSON.stringify(body)})
  const sendsChange=rows.value.some(row=>row.account.identity_state==='NONE'||row.owner)
  emit('saved',result.personId,sendsChange ? '绑定已保存，请在下发记录查看身份变更。' : '已绑定现有销售身份。')
 }catch(e){error.value=e.message}finally{busy.value=false}
}
watch([chosenCompany,role],()=>{for(const row of rows.value){row.companyId=chosenCompany.value;row.owner=!!chosenCompany.value&&role.value==='OWNER'&&row.account?.identity_state==='NONE'}})
watch(()=>[name.value,existingId.value,chosenCompany.value,role.value,start.value,rows.value.map(r=>[r.key,r.target,r.phone,r.countryCode,r.companyId,r.owner,r.account])],()=>{requestId.value=crypto.randomUUID()},{deep:true})
if(props.personId)add()
</script>
<template>
 <a-modal open :width="640" :title="personId ? (personName || '人员')+' · 绑定业务账号' : existing ? '加入已有人员' : '新建人员'" :closable="!busy" :keyboard="!busy" :mask-closable="false" @cancel="emit('close')">
  <a-alert v-if="error" :message="error" type="error" show-icon/>
  <fieldset :disabled="busy" class="intake-fields">
   <a-form layout="vertical" :disabled="busy">
    <a-form-item v-if="existing" label="选择已有人员" html-for="intake-existing" required><AppointmentPersonSelect id="intake-existing" v-model:value="existingId" :api="api"/></a-form-item>
    <a-form-item v-else-if="!personId" label="人员姓名" html-for="intake-name" required><a-input id="intake-name" v-model:value="name" :maxlength="25"/></a-form-item>
    <p v-if="company">销售公司 · {{company.name}}</p>
    <a-form-item v-else-if="!personId" label="销售公司（可选）">
      <a-select v-model:value="chosenCompany" placeholder="选择财务归属公司" allow-clear show-search :options="companyOptions" :filter-option="false" @focus="findCompany()" @search="findCompany"/>
    </a-form-item>
    <a-space v-if="membership" wrap>
      <a-form-item label="公司内角色"><a-select v-model:value="role" :options="[{value:'MEMBER',label:'成员'},{value:'MANAGER',label:'管理员'},{value:'OWNER',label:'负责人'}]"/></a-form-item>
      <a-form-item label="生效时间（北京时间，留空立即生效）"><a-input v-model:value="start" type="datetime-local"/></a-form-item>
    </a-space>
    <div v-if="!personId" class="intake-heading"><h3>业务账号（可选）</h3>
      <a-button @click="add">添加账号</a-button>
    </div>
    <section v-for="(row,index) in rows" :key="row.key" class="intake-row">
     <div v-if="rows.length>1 || !personId" class="intake-heading"><strong>业务账号 {{index+1}}</strong>
      <a-button type="link" size="small" @click="rows.splice(index,1)">移除</a-button>
     </div>
     <div class="lookup-fields">
      <a-form-item label="业务系统" :html-for="row.key+'-system'">
       <a-select :id="row.key+'-system'" v-model:value="row.target" :options="systemOptions" placeholder="选择系统" @change="reset(row)"/>
      </a-form-item>
      <a-form-item label="区号">
       <a-input v-model:value="row.countryCode" aria-label="区号" @update:value="reset(row)"/>
      </a-form-item>
      <a-form-item label="注册手机号">
       <a-input v-model:value="row.phone" type="tel" aria-label="注册手机号" placeholder="输入在该系统注册的手机号"
        @update:value="reset(row)" @press-enter="lookup(row)"/>
      </a-form-item>
      <a-button :loading="row.loading" :disabled="!lookupAvailable(row)" @click="lookup(row)">核验账号</a-button>
     </div>
     <p v-if="row.target && channels?.[row.target]===false" class="intake-hint">该系统的通道未开启，暂时无法核验账号。</p>
     <a-alert v-if="row.error" :message="row.error" type="warning" show-icon/>
     <section v-if="row.account" class="lookup-result">
      <div class="intake-heading">
       <strong>{{row.account.display_name || '已找到账号'}} · {{row.account.user_id}}</strong>
       <a-tag :color="row.account.identity_state==='ACTIVE'?'green':undefined">{{states[row.account.identity_state]}}</a-tag>
      </div>
      <p v-if="row.account.company_id">当前公司编号 {{row.account.company_id}}</p>
      <p v-if="row.account.identity_state==='NONE'" class="intake-hint">选择已送达{{systemName(row.target)}}的销售公司，提交后将创建销售身份。</p>
       <a-checkbox v-if="row.account.identity_state!=='INACTIVE' && row.account.role!=='OWNER'" v-model:checked="row.owner">设为公司负责人</a-checkbox>
       <a-form-item v-if="!membership && needsCompany(row)" label="目标销售公司" :html-for="row.key+'-company'" required>
        <a-select :id="row.key+'-company'" v-model:value="row.companyId" show-search :filter-option="false" :options="row.companies"
         placeholder="搜索已送达该系统的公司" not-found-content="暂无可选公司，请先在销售公司与类别完成公司同步"
         @focus="searchCompany(row)" @search="q=>searchCompany(row,q)"/>
       </a-form-item>
       <p v-if="!needsCompany(row) || row.companyId" class="intake-hint">{{accountAction(row)}}</p>
     </section>
    </section>
   </a-form>
  </fieldset>
  <template #footer>
   <a-button v-if="personId && canSave" type="link" :disabled="busy" @click="add">再添加账号</a-button>
   <a-button :disabled="busy" @click="emit('close')">取消</a-button><a-button type="primary" :loading="busy" :disabled="!canSave" @click="save">{{personId ? (rows.some(r=>r.account && needsCompany(r)) ? '绑定并下发' : '确认绑定') : existing?'确认加入':rows.length?'建档并绑定':'保存人员'}}</a-button></template>
 </a-modal>
</template>
<style scoped>
.intake-fields {border:0;padding:0;min-width:0;margin:12px 0 0}
.intake-heading {display:flex;align-items:center;justify-content:space-between;gap:12px}.intake-heading h3 {margin:0}
.intake-row {border-top:1px solid var(--line);margin-top:12px;padding:12px 0;display:grid;gap:10px}
.intake-row p,.intake-row .ant-form-item {margin:0}
.lookup-fields {display:grid;grid-template-columns:minmax(120px,1fr) 66px minmax(150px,1.5fr) auto;gap:8px;align-items:end}
.lookup-result {display:grid;justify-items:start;gap:9px;padding:12px;background:#f7f9fc;border:1px solid var(--line);border-radius:8px}
.lookup-result .intake-heading,.lookup-result .ant-form-item {width:100%}
.intake-hint {color:var(--muted)}
@media(max-width:590px){.lookup-fields {grid-template-columns:1fr 78px}.lookup-fields .ant-form-item:nth-child(3){grid-column:1/-1}}
</style>

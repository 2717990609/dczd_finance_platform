<script setup>
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import { computed, ref, watch } from 'vue'
const props=defineProps({api:Function,personId:Number,personName:String,company:Object,existing:Boolean})
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
const errors={NOT_FOUND:'该系统未查到已注册账号',CONFLICT:'该手机号匹配多个账号，请先核对',UNAVAILABLE:'来源账号暂不可用'}
const canSave=computed(()=>(props.personId ? rows.value.length>0 : props.existing ? !!existingId.value : !!name.value.trim()) &&
 rows.value.every(row=>row.confirmed && !row.loading))
function add(){rows.value.push({key:crypto.randomUUID(),target:'DCZD',countryCode:'+86',phone:'',lookup:0,companySearch:0,companies:[]})}
function reset(row){row.lookup++;row.account=undefined;row.confirmed=false;row.owner=false;row.companyId=membership.value || undefined;row.companies=[];row.error='';row.loading=false}
async function lookup(row) {
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
function confirm(row){
 if(rows.value.some(other=>other.key!==row.key&&other.confirmed&&other.target===row.target&&other.account?.user_id===row.account.user_id)){
  row.error='此账号已经添加';return
 }
 row.error='';row.confirmed=true
}
async function save(){
 if(!canSave.value||busy.value)return
 busy.value=true;error.value=''
 try{
  const body={requestId:requestId.value,personId:props.personId || existingId.value,name:props.personId || props.existing?'':name.value.trim(),membershipCompanyId:membership.value || undefined,memberRole:membership.value ? role.value : undefined,startsAt:membership.value && start.value ? start.value+':00+08:00' : undefined,
   bindings:rows.value.map(r=>({target:r.target,countryCode:r.countryCode,phone:r.phone,userId:r.account.user_id,
   companyId:r.companyId,owner:!!r.owner,confirmed:r.account}))}
  const result=await props.api('/organization-dispatch/person-intake',{method:'POST',body:JSON.stringify(body)})
  emit('saved',result.personId)
 }catch(e){error.value=e.message}finally{busy.value=false}
}
watch([chosenCompany,role],()=>{for(const row of rows.value){row.confirmed=false;row.companyId=chosenCompany.value;row.owner=!!chosenCompany.value&&role.value==='OWNER'&&row.account?.identity_state==='NONE'}})
watch(()=>[name.value,existingId.value,chosenCompany.value,role.value,start.value,rows.value.map(r=>[r.key,r.target,r.phone,r.countryCode,r.companyId,r.owner,r.confirmed])],()=>{requestId.value=crypto.randomUUID()},{deep:true})
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
    <div class="intake-heading"><h3>业务账号</h3><a-button @click="add">添加手机号</a-button></div>
    <section v-for="(row,index) in rows" :key="row.key" class="intake-row">
     <div class="intake-heading"><strong>账号 {{index+1}}</strong><a-space>
      <a-tag v-if="row.confirmed" color="green">已确认</a-tag>
      <a-button type="link" size="small" @click="rows.splice(index,1)">移除</a-button>
     </a-space></div>
     <a-space wrap>
      <a-select v-model:value="row.target" style="width:116px" :options="systems" aria-label="所属系统" @change="reset(row)"/>
      <a-input v-model:value="row.countryCode" aria-label="区号" style="width:68px" @update:value="reset(row)"/>
      <a-input v-model:value="row.phone" type="tel" aria-label="已注册手机号" placeholder="已注册手机号" @update:value="reset(row)"/>
      <a-button :loading="row.loading" :disabled="!row.phone.trim()" @click="lookup(row)">查找账号</a-button>
     </a-space>
     <a-alert v-if="row.error" :message="row.error" type="warning" show-icon/>
     <template v-if="row.account">
      <p>{{row.account.display_name}} · 账号 {{row.account.user_id}} · {{states[row.account.identity_state]}}<span v-if="row.account.company_id"> · 公司编号 {{row.account.company_id}}</span></p>
      <a-checkbox v-model:checked="row.owner" @change="row.confirmed=false">设为公司负责人</a-checkbox>
      <a-form-item v-if="!membership && (row.account.identity_state==='NONE'||row.owner)" label="目标销售公司" required>
       <a-select v-model:value="row.companyId" show-search :filter-option="false" :options="row.companies" placeholder="搜索已同步到该系统的公司"
        @focus="searchCompany(row)" @search="q=>searchCompany(row,q)" @change="row.confirmed=false"/>
      </a-form-item>
      <a-button v-if="!row.confirmed" :disabled="(row.account.identity_state==='NONE'||row.owner)&&!row.companyId" @click="confirm(row)">确认此账号</a-button>
     </template>
    </section>
   </a-form>
  </fieldset>
  <template #footer><a-button :disabled="busy" @click="emit('close')">取消</a-button><a-button type="primary" :loading="busy" :disabled="!canSave" @click="save">{{personId?'保存绑定':existing?'确认加入':rows.length?'建档并绑定':'保存人员'}}</a-button></template>
 </a-modal>
</template>
<style scoped>
.intake-fields {border:0;padding:0;min-width:0;margin:12px 0 0}
.intake-heading {display:flex;align-items:center;justify-content:space-between;gap:12px}.intake-heading h3 {margin:0}
.intake-row {border-top:1px solid var(--line);margin-top:12px;padding:12px 0;display:grid;gap:10px}
.intake-row p,.intake-row .ant-form-item {margin:0}
</style>

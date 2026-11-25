<script setup>
import {ref,watch} from 'vue'
const props=defineProps({api:Function,personId:Number,systems:Array,bindings:Array,canWrite:Boolean})
const emit=defineEmits(['changed'])
const target=ref('DCZD'),phone=ref(''),countryCode=ref('+86'),account=ref(),company=ref(),companies=ref([]),owner=ref(false)
const selection=ref(),destination=ref(),effective=ref(''),error=ref(''),busy=ref(false)
const resultLabels={NOT_FOUND:'该系统没有此账号',CONFLICT:'来源账号存在冲突',UNAVAILABLE:'来源账号暂不可用'}
const stateLabels={NONE:'无销售身份',PENDING:'等待创建',ACTIVE:'启用',INACTIVE:'停用'}
const operations=[['ENABLE','启用身份'],['DISABLE','停用身份'],['TRANSFER','调动公司'],['SET_OWNER','设为负责人'],['UNSET_OWNER','解除负责人'],['RENAME','同步财务姓名']]
const operation=ref(''),requestId=ref('')
watch([operation,destination,effective,selection],()=>{requestId.value=crypto.randomUUID()})
watch([target,phone,countryCode],()=>{account.value=undefined;owner.value=false;company.value=undefined})
const post=(path,data)=>props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(data)})
async function act(fn){if(busy.value)return;busy.value=true;error.value='';try{await fn()}catch(e){error.value=e.message}finally{busy.value=false}}
async function lookup(){await act(async()=>{account.value=await post('lookup',{target:target.value,phone:phone.value,countryCode:countryCode.value})})}
async function bind(){await act(async()=>{await post('bind',{target:target.value,phone:phone.value,countryCode:countryCode.value,userId:account.value.user_id,personId:props.personId,companyId:company.value,owner:owner.value});account.value=undefined;phone.value='';emit('changed')})}
let request=0
async function searchCompanies(term=''){
 const seq=++request
 try {const res=await props.api('/organization-sales-companies?name='+encodeURIComponent(term));if(seq===request)companies.value=res.items.filter(c=>c.status==='ACTIVE').map(c=>({value:c.id,label:c.name}))}
 catch(e){if(seq===request)error.value=e.message}
}
function change(binding){selection.value=binding;operation.value='';destination.value=undefined;effective.value=''}
async function save(){await act(async()=>{await post('member',{requestId:requestId.value,personId:props.personId,bindingId:selection.value.id,operation:operation.value,destinationId:destination.value,expectedVersion:selection.value.version,effectiveAt:effective.value ? new Date(effective.value).toISOString():null});selection.value=undefined;emit('changed')})}
function allowed(b,key){if(b.state==='PENDING')return false;if(key==='ENABLE')return b.state==='INACTIVE';if(b.state!=='ACTIVE')return false;if(key==='UNSET_OWNER')return b.role==='OWNER';if(['SET_OWNER','DISABLE','TRANSFER'].includes(key))return b.role!=='OWNER';return true}
</script>
<template>
 <a-alert v-if="error" :message="error" type="error" show-icon/>
 <h3>已绑定的销售身份</h3>
 <a-list :data-source="bindings" size="small">
  <template #renderItem="{item}"><a-list-item><span>{{systems.find(o=>o.value===item.target)?.label}} · 账号 {{item.userId}} · 公司 {{item.companyId}} · {{item.role==='OWNER'?'负责人':item.role==='MANAGER'?'管理人员':'成员'}} · {{stateLabels[item.state]}}</span>
   <a-button v-if="canWrite" size="small" @click="change(item)">维护身份</a-button></a-list-item></template>
 </a-list>
 <a-form v-if="selection" layout="vertical" @finish="save">
  <a-form-item label="身份操作" required><a-select v-model:value="operation" :options="operations.filter(([key])=>allowed(selection,key)).map(([value,label])=>({value,label}))"/></a-form-item>
  <a-form-item v-if="operation==='TRANSFER'" label="调入公司" required><a-select v-model:value="destination" :options="companies" show-search :filter-option="false" @focus="searchCompanies()" @search="searchCompanies"/></a-form-item>
  <a-form-item label="生效时间（留空立即执行）"><a-input v-model:value="effective" type="datetime-local"/></a-form-item>
  <a-space><a-button @click="selection=undefined">取消</a-button><a-button html-type="submit" type="primary" :disabled="!operation || operation==='TRANSFER' && !destination" :loading="busy">确认{{operations.find(([key])=>key===operation)?.[1]}}</a-button></a-space>
 </a-form>
 <template v-if="canWrite">
  <h3>绑定业务账号</h3>
  <a-space wrap>
   <a-select v-model:value="target" :options="systems" aria-label="账号所属系统"/>
   <a-input v-model:value="countryCode" style="width:75px" aria-label="国家区号"/>
   <a-input v-model:value="phone" type="tel" autocomplete="off" aria-label="账号手机号" placeholder="账号手机号"/>
   <a-button :loading="busy" @click="lookup">查询账号</a-button>
  </a-space>
  <a-alert v-if="account && account.status!=='FOUND'" type="warning" :message="resultLabels[account.status]"/>
  <a-form v-if="account?.status==='FOUND'" layout="vertical" @finish="bind">
   <p>{{account.display_name}} · 账号 {{account.user_id}} · {{stateLabels[account.identity_state]}}</p>
   <a-checkbox v-model:checked="owner">设为公司负责人</a-checkbox>
   <a-form-item v-if="account.identity_state==='NONE'||owner" label="销售公司" required><a-select v-model:value="company" :options="companies" show-search :filter-option="false" @focus="searchCompanies()" @search="searchCompanies"/></a-form-item>
   <a-button html-type="submit" type="primary" :loading="busy" :disabled="(account.identity_state==='NONE'||owner) && !company">确认绑定</a-button>
  </a-form>
 </template>
</template>

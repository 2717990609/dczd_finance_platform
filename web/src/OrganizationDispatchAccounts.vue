<script setup>
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'
import {ref,watch} from 'vue'
const props=defineProps({api:Function,personId:Number,personName:String,systems:Array,bindings:Array,canWrite:Boolean})
const emit=defineEmits(['changed'])
const bindingOpen=ref(false),companies=ref([])
const selection=ref(),destination=ref(),effective=ref(''),reason=ref(''),error=ref(''),busy=ref(false)
const stateLabels={NONE:'无销售身份',PENDING:'等待创建',ACTIVE:'启用',INACTIVE:'停用'}
const operations=[['ENABLE','启用身份'],['DISABLE','停用身份'],['TRANSFER','调动公司'],['SET_OWNER','设为负责人'],['UNSET_OWNER','解除负责人'],['RENAME','同步财务姓名']]
const operation=ref(''),requestId=ref('')
watch([operation,destination,effective,selection,reason],()=>{requestId.value=crypto.randomUUID()})
const post=(path,data)=>props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(data)})
async function act(fn){if(busy.value)return;busy.value=true;error.value='';try{await fn()}catch(e){error.value=e.message}finally{busy.value=false}}
let request=0
async function searchCompanies(term=''){
 const seq=++request
 try {const res=await props.api('/organization-dispatch/available-companies?'+new URLSearchParams({name:term,target:selection.value.target}));if(seq===request)companies.value=res}
 catch(e){if(seq===request)error.value=e.message}
}
function change(binding,key){selection.value=binding;operation.value=key;destination.value=undefined;effective.value='';reason.value='';error.value='';companies.value=[]}
async function save(){await act(async()=>{await post('member',{requestId:requestId.value,personId:props.personId,bindingId:selection.value.id,reason:reason.value.trim(),operation:operation.value,destinationId:destination.value,expectedVersion:selection.value.version,effectiveAt:effective.value ? new Date(effective.value).toISOString():null});selection.value=undefined;emit('changed')})}
function allowed(b,key){if(b.pending || b.state==='PENDING')return false;if(key==='ENABLE')return b.state==='INACTIVE';if(b.state!=='ACTIVE')return false;if(key==='UNSET_OWNER')return b.role==='OWNER';if(['SET_OWNER','DISABLE','TRANSFER'].includes(key))return b.role!=='OWNER';return true}
</script>
<template>
 <a-alert v-if="error" :message="error" type="error" show-icon/>
 <h3>已绑定的销售身份</h3>
 <ul v-if="bindings?.length" class="identity-list">
  <li v-for="item in bindings" :key="item.id"><span>{{systems.find(o=>o.value===item.target)?.label}} · 账号 {{item.userId}} · 公司 {{item.companyName || item.companyId}} · {{item.role==='OWNER'?'负责人':item.role==='MANAGER'?'管理人员':'成员'}} · {{stateLabels[item.state]}}</span>
   <span v-if="item.pending">有在途操作</span><a-space v-else-if="canWrite" wrap :size="2">
     <a-button v-for="([key,title]) in operations.filter(([key])=>allowed(item,key))" :key="key" type="link" size="small" @click="change(item,key)">{{title}}</a-button>
   </a-space></li>
 </ul>
 <p v-else>此人尚未绑定业务系统的销售身份。</p>
 <a-form v-if="selection" layout="vertical" @finish="save">
  <h4>{{ operations.find(([key])=>key===operation)?.[1] }} · {{ selection.userId }}</h4>
  <a-form-item v-if="operation==='TRANSFER'" label="调入公司" required><a-select v-model:value="destination" :options="companies" show-search :filter-option="false" @focus="searchCompanies()" @search="searchCompanies"/></a-form-item>
  <a-form-item label="操作说明" required><a-input v-model:value="reason" :maxlength="256"/></a-form-item>
  <a-form-item label="生效时间（留空立即执行）"><a-input v-model:value="effective" type="datetime-local"/></a-form-item>
  <a-space><a-button @click="selection=undefined">取消</a-button><a-button html-type="submit" type="primary" :disabled="!operation || !reason.trim() || operation==='TRANSFER' && !destination" :loading="busy">确认{{operations.find(([key])=>key===operation)?.[1]}}</a-button></a-space>
 </a-form>
 <a-button v-if="canWrite" @click="bindingOpen=true">绑定业务账号</a-button>
 <OrganizationPersonIntake v-if="bindingOpen" :person-id="personId" :person-name="personName" :api="api"
  @close="bindingOpen=false" @saved="bindingOpen=false;emit('changed')"/>
</template>
<style scoped>
.identity-list {list-style:none;padding:0;margin:0 0 12px}
.identity-list li {display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:4px 12px;padding:10px 0;border-bottom:1px solid var(--line)}
</style>

<script setup>
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'
import {computed,ref,watch} from 'vue'
const props=defineProps({api:Function,personId:Number,personName:String,systems:Array,bindings:Array,channels:Object,canWrite:Boolean,compact:Boolean})
const emit=defineEmits(['changed'])
const bindingOpen=ref(false),companies=ref([])
const selection=ref(),destination=ref(),effective=ref(''),reason=ref(''),error=ref(''),busy=ref(false)
const stateLabels={NONE:'无销售身份',PENDING:'等待创建',ACTIVE:'启用',INACTIVE:'停用'}
const operations=[['ENABLE','启用身份'],['DISABLE','停用身份'],['TRANSFER','调动公司'],['SET_OWNER','设为负责人'],['UNSET_OWNER','解除负责人'],['RENAME','同步财务姓名']]
const operation=ref(''),requestId=ref('')
const systemName=target=>props.systems.find(system=>system.value===target)?.label || target
const canBind=computed(()=>props.systems.some(system=>props.channels?.[system.value]))
const editTitle=()=>operations.find(([key])=>key===operation.value)?.[1]
watch([operation,destination,effective,selection,reason],()=>{requestId.value=crypto.randomUUID()})
const post=(path,data)=>props.api('/organization-dispatch/'+path,{method:'POST',body:JSON.stringify(data)})
async function act(fn){if(busy.value)return;busy.value=true;error.value='';try{await fn()}catch(e){error.value=e.message}finally{busy.value=false}}
let request=0
async function searchCompanies(term=''){
 const seq=++request
 try {const res=await props.api('/organization-dispatch/available-companies?'+new URLSearchParams({name:term,target:selection.value.target}));if(seq===request)companies.value=res}
 catch(e){if(seq===request)error.value=e.message}
}
function change(binding,key){request++;selection.value=binding;operation.value=key;destination.value=undefined;effective.value='';reason.value='';error.value='';companies.value=[]}
async function save(){await act(async()=>{await post('member',{requestId:requestId.value,personId:props.personId,bindingId:selection.value.id,reason:reason.value.trim(),operation:operation.value,destinationId:destination.value,expectedVersion:selection.value.version,effectiveAt:effective.value ? new Date(effective.value+':00+08:00').toISOString():null});selection.value=undefined;emit('changed','已提交身份变更，请在下发记录查看进度。')})}
function allowed(b,key){if(b.pending || b.state==='PENDING')return false;if(key==='ENABLE')return b.state==='INACTIVE';if(b.state!=='ACTIVE')return false;if(key==='UNSET_OWNER')return b.role==='OWNER';if(['SET_OWNER','DISABLE','TRANSFER'].includes(key))return b.role!=='OWNER';return true}
</script>
<template>
 <section class="identity-workspace" aria-label="向业务系统同步销售身份">
  <div v-if="!compact || !bindings?.length || canWrite" class="identity-heading">
   <div>
    <h3 v-if="!compact">销售身份下发</h3>
    <p v-if="compact && canWrite && channels && !canBind">下发通道未开启</p>
    <p v-else-if="!bindings?.length">财务尚未建立下发绑定</p>
   </div>
   <a-button v-if="canWrite && (!compact || canBind)" type="primary" :disabled="!canBind"
     @click="bindingOpen=true">{{compact ? '按手机号绑定' : '绑定业务账号'}}</a-button>
  </div>
  <p v-if="!compact && canWrite && channels && !canBind" class="identity-note">当前环境的下发通道未开启。</p>
  <ul v-if="bindings?.length" class="identity-list">
   <li v-for="item in bindings" :key="item.id">
    <div>
     <div class="identity-account"><strong>{{systemName(item.target)}} · {{item.userId}}</strong><a-tag>{{stateLabels[item.state] || item.state}}</a-tag></div>
     <small v-if="item.companyName || item.companyId">{{item.companyName || item.companyId}} · {{item.role==='OWNER'?'负责人':item.role==='MANAGER'?'管理人员':'成员'}}</small>
    </div>
    <small v-if="item.pending">变更处理中</small>
    <a-dropdown v-else-if="canWrite && operations.some(([key])=>allowed(item,key))" :trigger="['click']">
     <a-button :aria-label="'办理'+systemName(item.target)+'账号 '+item.userId">变更身份</a-button>
     <template #overlay>
      <a-menu @click="({key})=>change(item,key)">
       <a-menu-item v-for="[key,title] in operations.filter(([key])=>allowed(item,key))" :key="key">{{title}}</a-menu-item>
      </a-menu>
     </template>
    </a-dropdown>
   </li>
  </ul>
 </section>
 <a-modal v-if="selection" open :title="editTitle()+' · '+systemName(selection.target)" :ok-text="'提交'+editTitle()"
   :confirm-loading="busy" :cancel-button-props="{disabled:busy}" :closable="!busy" :keyboard="!busy" :mask-closable="false"
   :ok-button-props="{disabled:!reason.trim() || operation==='TRANSFER' && !destination}" @ok="save" @cancel="selection=undefined">
  <a-alert v-if="error" :message="error" type="error" show-icon/>
  <p class="operation-account">{{personName}} · 账号 {{selection.userId}}<template v-if="selection.companyName"> · {{selection.companyName}}</template></p>
  <a-alert v-if="channels && !channels[selection.target]" type="info" show-icon message="该系统的通道未开启，提交后将等待发送。"/>
  <a-form layout="vertical" :disabled="busy" @finish="save">
   <a-form-item v-if="operation==='TRANSFER'" label="调入公司" html-for="dispatch-operation-company" required>
    <a-select id="dispatch-operation-company" v-model:value="destination" placeholder="搜索已送达该系统的销售公司"
      :not-found-content="'暂无可选公司，可先在销售公司与类别检查同步状态'" :options="companies" show-search :filter-option="false"
      @focus="searchCompanies()" @search="searchCompanies"/>
   </a-form-item>
   <a-form-item label="操作说明" html-for="dispatch-operation-reason" required><a-input id="dispatch-operation-reason" v-model:value="reason" :maxlength="256"/></a-form-item>
   <a-form-item label="生效时间（北京时间，留空为立即生效）" html-for="dispatch-operation-time">
    <a-input id="dispatch-operation-time" v-model:value="effective" type="datetime-local"/>
   </a-form-item>
  </a-form>
 </a-modal>
 <OrganizationPersonIntake v-if="bindingOpen" :person-id="personId" :person-name="personName" :api="api"
   :channel-status="channels" :existing-bindings="bindings"
   @close="bindingOpen=false" @saved="(_, message)=>{bindingOpen=false;emit('changed',message)}"/>
</template>
<style scoped>
.identity-workspace {display:grid;gap:8px}
.identity-heading,.identity-list li {display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:8px}
.identity-heading h3,.identity-heading p,.identity-note {margin:0}
.identity-heading p,.identity-note,.identity-list small {color:var(--muted)}
.identity-account {display:flex;align-items:center;flex-wrap:wrap;gap:6px}
.identity-list {list-style:none;padding:0;margin:0;border-top:1px solid var(--line)}
.identity-list li {padding:10px 0;border-bottom:1px solid var(--line)}
.operation-account {margin:12px 0}
</style>

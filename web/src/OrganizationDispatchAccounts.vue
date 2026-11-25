<script setup>
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'
import {ref,watch} from 'vue'
const props=defineProps({api:Function,personId:Number,personName:String,systems:Array,bindings:Array,channels:Object,canWrite:Boolean})
const emit=defineEmits(['changed'])
const bindingTarget=ref(),companies=ref([])
const selection=ref(),destination=ref(),effective=ref(''),reason=ref(''),error=ref(''),busy=ref(false)
const stateLabels={NONE:'无销售身份',PENDING:'等待创建',ACTIVE:'启用',INACTIVE:'停用'}
const operations=[['ENABLE','启用身份'],['DISABLE','停用身份'],['TRANSFER','调动公司'],['SET_OWNER','设为负责人'],['UNSET_OWNER','解除负责人'],['RENAME','同步财务姓名']]
const operation=ref(''),requestId=ref('')
const systemName=target=>props.systems.find(system=>system.value===target)?.label || target
const targetBindings=target=>(props.bindings || []).filter(binding=>binding.target===target)
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
  <div class="identity-heading">
   <h3>向业务系统同步销售身份</h3>
   <p v-if="canWrite">选一个业务系统，绑定已注册账号或办理已有销售身份。</p>
   <p v-else>可查看此人在各业务系统的销售身份。</p>
  </div>
  <a-alert v-if="channels && systems.every(system=>!channels[system.value])" type="info" show-icon
    message="当前环境的下发通道均未开启，暂时无法核验新账号。"/>
  <div class="identity-targets">
   <section v-for="system in systems" :key="system.value" class="identity-target" :aria-label="system.label">
    <header>
     <strong>{{system.label}}</strong>
     <a-tag :color="channels?.[system.value] ? 'green' : undefined">{{!channels ? '通道状态未知' : channels[system.value] ? '通道已开启' : '通道未开启'}}</a-tag>
    </header>
    <ul v-if="targetBindings(system.value).length" class="identity-list">
     <li v-for="item in targetBindings(system.value)" :key="item.id">
      <div class="identity-account"><strong>账号 {{item.userId}}</strong><a-tag>{{stateLabels[item.state] || item.state}}</a-tag></div>
      <div v-if="item.sourceName">{{item.sourceName}}</div>
      <div v-if="item.companyName || item.companyId">{{item.companyName || item.companyId}} · {{item.role==='OWNER'?'负责人':item.role==='MANAGER'?'管理人员':'成员'}}</div>
      <span v-if="item.pending">变更处理中，可查看下发记录</span>
      <a-dropdown v-else-if="canWrite && operations.some(([key])=>allowed(item,key))" :trigger="['click']">
       <a-button :aria-label="'办理'+system.label+'账号 '+item.userId">办理身份</a-button>
       <template #overlay>
        <a-menu @click="({key})=>change(item,key)">
         <a-menu-item v-for="[key,title] in operations.filter(([key])=>allowed(item,key))" :key="key">{{title}}</a-menu-item>
        </a-menu>
       </template>
      </a-dropdown>
     </li>
    </ul>
    <p v-else class="identity-empty">暂无已绑定的销售身份</p>
    <a-button v-if="canWrite" :type="targetBindings(system.value).length ? 'default' : 'primary'"
      :disabled="!channels?.[system.value]" :aria-label="'绑定'+system.label+'账号'" @click="bindingTarget=system.value">
      {{targetBindings(system.value).length?'绑定其他账号':'绑定账号'}}
    </a-button>
   </section>
  </div>
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
 <OrganizationPersonIntake v-if="bindingTarget" :person-id="personId" :person-name="personName" :api="api"
   :initial-target="bindingTarget" :channel-status="channels" :existing-bindings="bindings"
   @close="bindingTarget=undefined" @saved="(_, message)=>{bindingTarget=undefined;emit('changed',message)}"/>
</template>
<style scoped>
.identity-workspace {display:grid;gap:12px}
.identity-heading h3,.identity-heading p,.identity-empty {margin:0}
.identity-heading p,.identity-empty {color:var(--muted)}
.identity-targets {display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,215px),1fr));gap:10px}
.identity-target {min-width:0;display:flex;flex-direction:column;align-items:flex-start;gap:10px;border:1px solid var(--line);border-radius:8px;padding:12px}
.identity-target header,.identity-account {display:flex;align-items:center;flex-wrap:wrap;gap:6px}
.identity-target header {width:100%;justify-content:space-between}
.identity-target :deep(.ant-tag) {margin-inline-end:0}
.identity-target > .ant-btn {margin-top:auto}
.identity-list {list-style:none;width:100%;padding:0;margin:0}
.identity-list li {display:grid;justify-items:start;gap:5px;padding:8px 0;border-bottom:1px solid var(--line)}
.operation-account {margin:12px 0}
</style>

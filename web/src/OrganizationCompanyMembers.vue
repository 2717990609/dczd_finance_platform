<script setup>
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import { appointmentDateError, beijingTime } from './appointment-time'
const props=defineProps({api:Function,company:Object,formatTime:Function,canWrite:Boolean})
const emit=defineEmits(['close','changed'])
const date=ref(''),term=ref(''),search=ref(''),page=ref(1),rows=ref({items:[],total:0}),loadError=ref(''),loading=ref(false)
const intake=ref(''),person=ref(),edit=ref(),target=ref(),role=ref('MEMBER'),at=ref(''),choices=ref([]),error=ref(''),busy=ref(false),notice=ref('')
const targetID=useId(),effectiveID=useId()
const dateError=computed(()=>edit.value ? appointmentDateError({
 mode:edit.value.end?'end':'transfer',record:edit.value.record,effective:at.value,
}) : '')
const canSave=computed(()=>!!at.value && !dateError.value && (edit.value?.end || !!target.value))
const roles={MEMBER:'成员',OWNER:'负责人',MANAGER:'管理员'}
const columns=[{title:'人员',dataIndex:'personName',width:180},{title:'公司内角色',key:'role',width:90},{title:'生效时间',key:'from',width:145},{title:'结束时间',key:'to',width:145},{title:'',key:'actions',width:186}]
const pagination=computed(()=>({current:page.value,pageSize:25,total:rows.value.total,hideOnSinglePage:true,showSizeChanger:false}))
let seq=0,companySeq=0
let searchTimer
async function load(){
 const id=++seq;loading.value=true;loadError.value=''
 try{const res=await props.api('/organization-dispatch/company-members?'+new URLSearchParams({companyId:props.company.id,on:date.value,name:search.value,page:page.value}));if(id===seq)rows.value=res}
 catch(e){if(id===seq){loadError.value=e.message;rows.value={items:[],total:0}}}finally{if(id===seq)loading.value=false}
}
watch([date,search],()=>{page.value=1;load()})
watch(()=>props.company.id,load,{immediate:true})
function searchMembers(){
 clearTimeout(searchTimer)
 search.value=term.value.trim()
}
watch(term,value=>{
 clearTimeout(searchTimer)
 if(!value.trim())searchMembers()
 else searchTimer=setTimeout(searchMembers,220)
})
onBeforeUnmount(()=>{clearTimeout(searchTimer);seq++;companySeq++})
function changed(message){notice.value=message;load();emit('changed')}
function begin(record,end){edit.value={record,end};target.value=props.company.id;role.value=record.kind;at.value='';error.value='';choices.value=[{value:props.company.id,label:props.company.name}]}
async function companies(name=''){
 const id=++companySeq
 try{const res=await props.api('/organization-sales-companies?'+new URLSearchParams({name}));if(id===companySeq)choices.value=res.items.filter(c=>c.status==='ACTIVE').map(c=>({value:c.id,label:c.name}))}
 catch(e){if(id===companySeq)error.value=e.message}
}
async function save(){
 if(!canSave.value||busy.value)return
 const {record,end}=edit.value
 busy.value=true;error.value=''
 try{
  const body={assignmentId:record.id,expectedThroughAt:record.validTo,at:beijingTime(at.value)}
  if(!end)Object.assign(body,{unitId:target.value,kind:role.value,position:record.position})
  await props.api('/organization-assignments/'+(end?'end':'transfer'),{method:'POST',body:JSON.stringify(body)})
  edit.value=undefined;changed('财务归属已更新')
 }catch(e){error.value=e.message}finally{busy.value=false}
}
</script>
<template>
 <a-drawer open :width="'min(900px, 100vw)'" :title="company.name+' · 公司成员'" @close="emit('close')">
  <slot name="company-tools"/>
  <a-space wrap><a-button v-if="canWrite && company.status==='ACTIVE'" type="primary" @click="intake='existing'">加入已有人员</a-button>
   <a-button v-if="canWrite && company.status==='ACTIVE'" @click="intake='new'">新建人员</a-button>
   <a-input-search v-model:value="term" allow-clear placeholder="搜索公司成员" aria-label="搜索公司成员" @search="searchMembers"/>
   <label>归属日期 <a-input v-model:value="date" type="date" style="width:160px"/></label>
  </a-space>
  <p class="member-note">留空查看当前成员，选择日期可查看当天北京时间零点的财务归属。</p>
  <a-alert v-if="notice" :message="notice" type="success" show-icon closable @close="notice=''"/>
  <a-alert v-if="loadError" :message="loadError" type="error" show-icon><template #action><a-button @click="load">重试</a-button></template></a-alert>
  <a-table size="small" :loading="loading" :data-source="rows.items" :columns="columns" :pagination="pagination" row-key="id" :scroll="{x:746}" @change="v=>{page=v.current;load()}">
   <template #bodyCell="{column,record}">
    <span v-if="column.key==='role'">{{roles[record.kind]}}</span>
    <span v-else-if="column.key==='from'">{{formatTime(record.validFrom)}}</span>
    <span v-else-if="column.key==='to'">{{record.validTo?formatTime(record.validTo):'长期有效'}}</span>
    <a-space v-else-if="column.key==='actions'" wrap :size="0">
     <a-button type="link" size="small" @click="person=record">销售身份同步</a-button>
     <template v-if="canWrite"><a-button type="link" size="small" @click="begin(record,false)">调整归属</a-button><a-button type="link" size="small" @click="begin(record,true)">结束归属</a-button></template>
    </a-space>
   </template><template #emptyText>暂无符合条件的公司成员</template>
  </a-table>
  <OrganizationPersonIntake v-if="intake" :api="api" :company="company" :existing="intake==='existing'" @close="intake=''" @saved="intake='';changed('人员与归属已保存')"/>
  <OrganizationDispatch v-if="person" :person-id="person.personId" :person-name="person.personName" :api="api" :can-write="canWrite" :format-time="formatTime" @close="person=undefined" @changed="load"/>
  <a-modal v-if="edit" open :title="edit.record.personName+' · '+(edit.end?'结束财务归属':'调整财务归属')"
   :ok-text="edit.end?'确认结束':'保存归属'" cancel-text="取消" :confirm-loading="busy"
   :ok-button-props="{disabled:!canSave,danger:edit.end}" :cancel-button-props="{disabled:busy}"
   :closable="!busy" :keyboard="!busy" :mask-closable="!busy" @ok="save" @cancel="!busy && (edit=undefined)">
   <a-alert v-if="error" :message="error" type="error" show-icon/>
   <a-form layout="vertical">
    <template v-if="!edit.end"><a-form-item label="销售公司" :html-for="targetID"><a-select :id="targetID" v-model:value="target" show-search :options="choices" :filter-option="false" @search="companies" @focus="companies()"/></a-form-item>
     <a-form-item label="公司内角色"><a-radio-group v-model:value="role" :options="Object.entries(roles).map(([value,label])=>({value,label}))"/></a-form-item></template>
    <a-form-item :label="edit.end?'结束时间（北京时间）':'生效时间（北京时间）'" :html-for="effectiveID" required
     :help="dateError || undefined" :validate-status="dateError?'error':undefined">
     <a-input :id="effectiveID" v-model:value="at" type="datetime-local"/>
    </a-form-item>
   </a-form>
  </a-modal>
 </a-drawer>
</template>
<style scoped>.member-note{color:var(--muted);font-size:13px;margin:12px 0}</style>

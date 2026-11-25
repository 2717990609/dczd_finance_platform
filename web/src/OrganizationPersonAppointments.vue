<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'
import PersonAppointmentActions from './PersonAppointmentActions.vue'

const props=defineProps({api:Function,personId:Number,canWrite:Boolean,appointmentLoading:Boolean,
 initialHistory:Boolean,optionLabel:Function,formatTime:Function})
const emit=defineEmits(['appointment'])
const history=ref(props.initialHistory),page=ref(1)
const appointments=ref({total:0,items:[]}),error=ref(''),loading=ref(false)
let generation=0
async function refresh(){
 const request=++generation
 const params=new URLSearchParams({personId:props.personId,page:page.value})
 if(history.value)params.set('history','true')
 loading.value=true
 error.value=''
 try{
  const result=await props.api('/organization-assignments?'+params)
  if(request===generation)appointments.value=result
 }catch(e){
  if(request===generation)error.value=e.message
 }finally{
  if(request===generation)loading.value=false
 }
}
function showHistory(value){
 page.value=1
 history.value=value
}
function ended(row){return row.validTo && Date.parse(row.validTo)<=Date.now()}
watch(()=>props.initialHistory,showHistory)
watch([page,history],refresh,{immediate:true})
onBeforeUnmount(()=>generation++)
defineExpose({refresh})
</script>

<template>
 <section class="records person-appointments" aria-label="人员任职">
  <header>
   <h2>{{history ? '任职记录' : '当前任职'}}</h2>
   <a-space wrap>
    <a-button type="text" size="small" @click="showHistory(!history)">{{history ? '只看当前' : '查看历史'}}</a-button>
    <a-button v-if="canWrite" type="primary" :loading="appointmentLoading" @click="emit('appointment','add')">登记任职</a-button>
   </a-space>
  </header>
  <a-alert v-if="error" type="error" :message="error"><template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert>
  <a-spin v-else :spinning="loading">
   <ul v-if="appointments.items.length">
    <li v-for="row in appointments.items" :key="row.id">
     <div>
      <div class="appointment-name">
       <strong>{{row.unitName}}</strong>
       <span>{{row.salesCompany ? '销售公司' : (optionLabel('organizationKind',row.unitKind) || row.unitKind)}}</span>
      </div>
      <small>
       {{optionLabel('appointmentKind',row.kind) || row.kind}}<template v-if="row.position"> · {{row.position}}</template>
        · {{formatTime(row.validFrom)}} 起<template v-if="row.validTo"> · {{formatTime(row.validTo)}} 止</template>
      </small>
     </div>
     <span v-if="ended(row)" class="appointment-status">已结束</span>
     <PersonAppointmentActions v-else-if="canWrite" :label="row.unitName" @select="emit('appointment',$event,row)"/>
    </li>
   </ul>
   <p v-else-if="!loading" class="appointment-empty">{{history ? '暂无任职记录' : '暂无当前任职'}}</p>
   <a-pagination v-if="appointments.total>25" v-model:current="page" :total="appointments.total" :page-size="25" :show-size-changer="false"/>
  </a-spin>
 </section>
</template>

<style scoped>
.person-appointments {padding:14px 16px 6px;min-width:0}
.person-appointments header,.person-appointments li {display:flex;align-items:center;justify-content:space-between;gap:8px 12px;flex-wrap:wrap}
.person-appointments h2,.person-appointments p,.person-appointments ul {margin:0}
.person-appointments h2 {font-size:16px}
.person-appointments ul {list-style:none;padding:0;margin-top:8px}
.person-appointments li {border-top:1px solid var(--line);padding:9px 0;overflow-wrap:anywhere}
.appointment-name {display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}
.person-appointments small,.appointment-name span,.appointment-status,.appointment-empty {color:var(--muted);font-size:13px}
.person-appointments small {display:block}
.appointment-empty {padding:12px 0}
@media(max-width:590px){.person-appointments {padding:10px 12px 6px}}
</style>

<script setup>
import { onBeforeUnmount, ref } from 'vue'
import PersonAppointmentActions from './PersonAppointmentActions.vue'

const props=defineProps({api:Function,personId:Number,canWrite:Boolean,appointmentLoading:Boolean,personLink:Function,optionLabel:Function,formatTime:Function})
const emit=defineEmits(['appointment'])
const appointments=ref(),accounts=ref(),errors=ref({}),loading=ref(true)
let generation=0
async function refresh(){
 const request=++generation
 const subject=new URLSearchParams({personId:props.personId})
 loading.value=true
 errors.value={}
 await Promise.all([
  [appointments,'/organization-assignments?'+subject,'appointments'],
  [accounts,'/person-source-accounts?'+subject+'&current=true','accounts'],
 ].map(async ([target,url,key])=>{
  try{
   const result=await props.api(url)
   if(request===generation)target.value=result
  }catch(e){
   if(request===generation){
    target.value=undefined
    errors.value[key]=e.message
   }
  }
 }))
 if(request===generation)loading.value=false
}
refresh()
onBeforeUnmount(()=>generation++)
defineExpose({refresh})
</script>

<template>
 <div class="person-overview">
  <section class="records overview-card" aria-label="当前财务任职">
   <header>
    <h2>当前财务任职</h2>
    <a-button v-if="canWrite" type="primary" :loading="appointmentLoading" @click="emit('appointment','add')">登记任职</a-button>
   </header>
   <a-alert v-if="errors.appointments" type="error" :message="errors.appointments"><template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert>
   <a-spin v-else :spinning="loading">
    <ul v-if="appointments?.items.length">
     <li v-for="row in appointments.items.slice(0,5)" :key="row.id">
      <div>
       <strong>{{row.unitName}}</strong>
       <small>{{optionLabel('appointmentKind',row.kind) || row.kind}}<template v-if="row.position"> · {{row.position}}</template> · {{formatTime(row.validFrom)}} 起</small>
      </div>
      <PersonAppointmentActions v-if="canWrite" :label="row.unitName" @select="emit('appointment',$event,row)"/>
     </li>
    </ul>
    <p v-else-if="appointments">暂无有效任职</p>
   </a-spin>
   <a :href="personLink(personId,'appointments')">查看全部任职记录<template v-if="appointments?.total>5">（{{appointments.total}} 条当前任职）</template></a>
  </section>
  <section class="records overview-card" aria-label="有效业务账号">
   <header><h2>有效业务账号</h2><a :href="personLink(personId,'accounts')">管理账号与身份</a></header>
   <a-alert v-if="errors.accounts" type="error" :message="errors.accounts"><template #action><a-button size="small" @click="refresh">重试</a-button></template></a-alert>
   <a-spin v-else :spinning="loading">
    <ul v-if="accounts?.items.length">
     <li v-for="row in accounts.items.slice(0,5)" :key="row.origin+':'+row.id">
      <strong>{{optionLabel('source',row.source)?.split(' · ')[0] || row.source}}</strong>
      <span class="numeric">{{row.externalId}}</span>
     </li>
    </ul>
    <p v-else-if="accounts">暂无有效业务账号</p>
   </a-spin>
   <small v-if="accounts?.total>5">共 {{accounts.total}} 条有效账号记录</small>
  </section>
 </div>
</template>

<style scoped>
.person-overview {display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,355px),1fr));gap:14px;align-items:start}
.overview-card {display:grid;gap:10px;padding:14px 16px;min-width:0}
.overview-card header,.overview-card li {display:flex;align-items:center;justify-content:space-between;gap:8px 12px;flex-wrap:wrap}
.overview-card h2,.overview-card p,.overview-card ul {margin:0}
.overview-card h2 {font-size:16px}
.overview-card ul {list-style:none;padding:0}
.overview-card li {border-top:1px solid var(--line);padding:9px 0;overflow-wrap:anywhere}
.overview-card small {display:block;color:var(--muted)}
.overview-card > a {justify-self:start}
</style>

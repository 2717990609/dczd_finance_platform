<script setup>
import {ref,watch} from 'vue'
const props=defineProps({api:Function,personId:Number,companyId:String,group:String,formatTime:Function})
const data=ref(),error=ref('')
const sources={DCZD:'大春之道',DCJK:'大春见康',DCAI:'大春 AI',PLATFORM:'财务平台',MANUAL:'人工登记'}
const types={SOURCE_USER_ID:'用户编号',SOURCE_ACCOUNT_ID:'账号编号',SOURCE_WRITER_ID:'作者编号',SALES_ORGANIZATION_ID:'销售组织编号',SOURCE_COMPANY_ID:'来源公司编号',COMPANY_ID:'公司编号',MANUAL_RECORD_ID:'人工档案编号',JULY_CONFIRMED_SOURCE_USER_ID:'已核实的来源用户',POLICY_TEAM_CODE:'团队代码',TEAM_POOL_DISPLAY_UNIT_ID:'团队池展示组织',WORKBOOK_RECIPIENT:'表内收款方'}
let sequence=0
async function load(){
 const version=++sequence
 error.value='';data.value=undefined
 try{
  const args=props.personId?{personId:props.personId}:{companyId:props.companyId}
  const value=await props.api('/organization-dispatch/source-relations?'+new URLSearchParams(args))
  if(version===sequence)data.value=value
 }catch(e){if(version===sequence)error.value=e.message}
}
watch(()=>[props.personId,props.companyId],load,{immediate:true})
</script>
<template>
 <a-alert v-if="error" type="error" :message="error"><template #action><a-button @click="load">重试</a-button></template></a-alert>
 <section v-if="data" class="source-relations">
  <details v-if="group!=='duties' && data.identifiers.length">
   <summary>第三方关联编号（{{data.identifiers.length}}）</summary>
   <ul><li v-for="(r,i) in data.identifiers" :key="i">{{sources[r.source]||r.source}} · {{types[r.kind]||r.kind}} {{r.externalId}}
    <small>{{formatTime(r.from)}} 至 {{r.to?formatTime(r.to):'长期有效'}}</small></li></ul>
  </details>
  <details v-if="group!=='accounts' && data.roles.length">
   <summary>老财务任职与职责（{{data.roles.length}}）</summary>
   <ul><li v-for="(r,i) in data.roles" :key="i">{{r.scope}} · {{r.role}}
    <small>{{formatTime(r.from)}} 至 {{r.to?formatTime(r.to):'长期有效'}}</small></li></ul>
  </details>
 </section>
</template>
<style scoped>
.source-relations details {background:white;padding:10px 14px;border:1px solid var(--line);border-radius:6px;margin-bottom:6px}
.source-relations summary {cursor:pointer;font-weight:600}
.source-relations ul {padding-left:20px;max-height:320px;overflow:auto}
.source-relations small {color:var(--muted);margin-left:8px}
</style>

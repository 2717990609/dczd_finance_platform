<script setup>
import {ref, watch} from 'vue'

const props=defineProps({api:Function,personId:Number,systems:Array,formatTime:Function})
const page=ref(1),result=ref(),error=ref(''),loading=ref(false)
let sequence=0
async function load(){
  const request=++sequence
  loading.value=true
  error.value=''
  try{
    const data=await props.api('/person-source-accounts?'+new URLSearchParams({personId:props.personId,page:page.value}))
    if(request===sequence) result.value=data
  }catch(e){
    if(request===sequence) error.value=e.message
  }finally{
    if(request===sequence) loading.value=false
  }
}
watch(()=>props.personId,()=>{
  result.value=undefined
  if(page.value===1) load()
  else page.value=1
},{immediate:true})
watch(page,load)
function state(row){
  const now=Date.now()
  if(Date.parse(row.validFrom)>now) return '未生效'
  if(row.validTo && Date.parse(row.validTo)<=now) return '已结束'
  return '有效'
}
</script>

<template>
  <section class="source-accounts" aria-label="业务来源账号">
    <h3>业务来源账号</h3>
    <a-alert v-if="error" type="error" :message="error"><template #action><a-button @click="load">重试</a-button></template></a-alert>
    <a-spin v-else :spinning="loading">
      <ul v-if="result?.items.length">
        <li v-for="row in result.items" :key="row.origin+':'+row.id">
          <span>{{systems.find(s=>s.value===row.source)?.label || row.source}} · {{row.externalId}}</span>
          <a-tag :color="state(row)==='有效'?'green':undefined">{{state(row)}}</a-tag>
          <small>{{formatTime(row.validFrom)}} 至 {{row.validTo?formatTime(row.validTo):'长期有效'}}</small>
        </li>
      </ul>
      <p v-else-if="result">暂无业务来源账号。</p>
      <a-pagination v-if="result?.total>25" v-model:current="page" size="small" :page-size="25" :total="result.total" :show-size-changer="false"/>
    </a-spin>
  </section>
</template>

<style scoped>
.source-accounts ul{list-style:none;padding:0;margin:0}
.source-accounts li{display:flex;align-items:baseline;flex-wrap:wrap;gap:4px 10px;padding:6px 0;border-bottom:1px solid var(--line)}
.source-accounts small{color:var(--muted)}
</style>

<script setup>
import {computed,ref,watch} from 'vue'
const props=defineProps({api:Function,period:Number,kind:String,company:String,value:String,name:String,disabled:Boolean})
const emit=defineEmits(['change'])
const rows=ref([]),loading=ref(false),error=ref('')
let seq=0
const options=computed(()=>{
  const items=rows.value.map(row=>({value:row.id,label:row.name+(row.sourceId?' · '+row.sourceId:'')}))
  if(props.value && !items.some(row=>row.value===props.value)) items.unshift({value:props.value,label:props.name||props.value})
  return items
})
async function search(name=''){
  const req=++seq
  error.value=''
  if(!props.period || (props.kind==='person' && !props.company)){ rows.value=[];loading.value=false;return }
  loading.value=true
  try{
    const query=new URLSearchParams({periodId:props.period,kind:props.kind,name})
    if(props.company) query.set('companyId',props.company)
    const result=await props.api('/organization-directors/options?'+query)
    if(req===seq) rows.value=result
  }catch(e){if(req===seq){error.value=e.message;rows.value=[]}}
  finally{if(req===seq)loading.value=false}
}
function choose(id){emit('change',id||'',rows.value.find(row=>row.id===id)?.name||'')}
watch(()=>[props.period,props.company],()=>{seq++;rows.value=[];error.value='';loading.value=false})
</script>
<template>
  <a-select :value="value||undefined" :disabled="disabled||!period||(kind==='person'&&!company)" :options="options" allow-clear
    :placeholder="kind==='person'?'选择公司成员':'搜索销售公司'" :loading="loading" show-search :filter-option="false"
    @search="search" @dropdownVisibleChange="open=>open && search()" @change="choose"/>
  <small v-if="error" role="alert">{{error}}</small>
</template>

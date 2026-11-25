<script setup>
import {computed,onMounted,ref,watch} from 'vue'
import DirectorPartySelect from './DirectorPartySelect.vue'
const props=defineProps({api:Function,formatTime:Function,canWrite:Boolean,company:Object})
const emit=defineEmits(['clear-company'])
const base='/organization-directors'
const versions=ref([]),periods=ref([]),selected=ref(),detail=ref(),error=ref(''),notice=ref('')
const mode=ref(''),period=ref(),company=ref(''),companyName=ref(''),person=ref(''),department=ref(''),directorId=ref()
const file=ref(),bindings=ref({}),checked=ref(),valid=ref(false),editRow=ref(),busy=ref(false),loading=ref(false),requestId=ref('')
let loadSeq=0,detailSeq=0
const periodsOptions=computed(()=>periods.value.map(p=>({value:p.id,label:p.name+' · '+props.formatTime(p.startsAt)})))
const currentProduct=computed(()=>periods.value.find(p=>p.id===period.value)?.productId || periods.value[0]?.productId)
const latest=computed(()=>versions.value.find(v=>v.productId===currentProduct.value))
const directors=computed(()=>{
  const leaders=new Map()
  for(const r of detail.value?.document?.rows||[]) leaders.set(r.leader_company_id,r.leader_display_name+' · '+r.leader_company_display_name)
  return [...leaders].map(([value,label])=>({value,label}))
})
const membership=computed(()=>detail.value?.document?.rows?.find(r=>r.member_company_id===company.value || r.pending_company_id===company.value))
const columns=[
 {title:'序号',dataIndex:'number',width:70},{title:'部门',dataIndex:'department',width:110},
 {title:'总监及所属销售公司',key:'leader',width:230},{title:'组员及下属公司',key:'member',width:245},
 {title:'收款方',key:'recipient',width:135},
]
const previewColumns=computed(()=>mode.value==='import'?[...columns,{title:'匹配',key:'edit',width:82}]:columns)
const changes=[{title:'公司',dataIndex:'company'},{title:'变动',dataIndex:'kind'},{title:'原总监',dataIndex:'before'},{title:'新总监',dataIndex:'after'}]
function resetCheck(){valid.value=false;requestId.value=''}
function setCompany(id,name){ company.value=id;companyName.value=name;person.value='';directorId.value=undefined }
function resetMode(){
 mode.value='';file.value=undefined;bindings.value={};checked.value=undefined;editRow.value=undefined;resetCheck();setCompany('','');department.value=''
}
async function readDetail(id){
 const n=++detailSeq;detail.value=undefined;selected.value=id
 if(!id)return
 try{const row=await props.api(base+'/detail?id='+id);if(n===detailSeq)detail.value=row}
 catch(e){if(n===detailSeq)error.value=e.message}
}
async function refresh(){
 const n=++loadSeq;loading.value=true;error.value=''
 try{
  const [v,p]=await Promise.all([props.api(base),props.api(base+'/periods')]);if(n!==loadSeq)return
  versions.value=v;periods.value=p
  await readDetail(mode.value?latest.value?.id:v[0]?.id)
 }catch(e){if(n===loadSeq)error.value=e.message}
 finally{if(n===loadSeq)loading.value=false}
}
async function start(kind,subject){
 resetMode();mode.value=kind;error.value='';notice.value=''
 period.value=periods.value[0]?.id
 if(selected.value!==latest.value?.id)await readDetail(latest.value?.id)
 if(subject)setCompany(subject.id,subject.name)
}
function openBinding(row){
 const b=bindings.value[row.number]
 editRow.value=b?{...b}:{number:row.number,leaderId:row.leader_id,companyId:row.leader_company_id,memberId:row.member_company_id,
  pending:!!row.pending_review,leaderLabel:row.leader_display_name,companyLabel:row.leader_company_display_name,memberLabel:row.member_company_display_name}
}
function acceptBinding(){
 const b=editRow.value;bindings.value={...bindings.value,[b.number]:{...b,memberId:b.pending?'':b.memberId}}
 editRow.value=undefined;resetCheck()
}
function chooseFile(event){
 file.value=event.target.files[0];checked.value=undefined;bindings.value={};resetCheck()
}
function fileData(blob){
 return new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result).split(',')[1]);reader.onerror=()=>reject(new Error('无法读取原表'));reader.readAsDataURL(blob)})
}
async function request(save){
 if(busy.value || !period.value || !mode.value || (save&&!valid.value))return
 busy.value=true;error.value='';notice.value=''
 try{
  let edit={kind:mode.value}
  if(mode.value==='import'||mode.value==='legacy'){
   if(!file.value||file.value.size>10*1024*1024)throw new Error('请选择 10 MB 以内的文件')
   edit={...edit,fileName:file.value.name,file:await fileData(file.value),bindings:Object.values(bindings.value).map(({number,leaderId,companyId,memberId,pending})=>({number,leaderId,companyId,memberId,pending}))}
  }else if(mode.value==='director'){
   edit={...edit,companyId:company.value,personId:person.value,department:department.value.trim()}
  }else{edit={...edit,companyId:company.value,directorCompanyId:directorId.value||''}}
  const args={periodId:period.value,baseId:latest.value?.id||0,edit}
  if(save){args.expected=checked.value.hash;args.requestId=requestId.value}
  const result=await props.api(base+(save?'':'/preview'),{method:'POST',body:JSON.stringify(args)})
  if(save){resetMode();await refresh();notice.value='总监归属版本已保存。'}
  else{checked.value=result;valid.value=!result.issues?.length;requestId.value=crypto.randomUUID()}
 }catch(e){error.value=e.message;if(!save || e.status===409)resetCheck()}
 finally{busy.value=false}
}
watch([period,company,person,department,directorId],resetCheck)
watch(()=>props.company,subject=>{if(subject && props.canWrite){start('member',subject);emit('clear-company')}})
onMounted(async()=>{await refresh();if(props.company && props.canWrite){await start('member',props.company);emit('clear-company')}})
defineExpose({refresh})
</script>
<template>
 <section class="records director-page">
  <div class="records-head">
   <h2>家庭教育实战班总监归属</h2>
   <a-space v-if="canWrite" wrap>
    <a-button :disabled="busy||loading" @click="start('director')">登记总监</a-button>
    <a-button :disabled="busy||loading" @click="start('member')">调整公司归属</a-button>
    <a-button :disabled="busy||loading" @click="start('legacy')">导入已核定版本</a-button>
    <a-button type="primary" :disabled="busy||loading" @click="start('import')">导入原表</a-button>
   </a-space>
  </div>
  <a-alert v-if="error" type="error" :message="error" show-icon>
   <template #action><a-button size="small" :disabled="busy||loading" @click="refresh">刷新版本</a-button></template>
  </a-alert>
  <a-alert v-if="notice" type="success" :message="notice" show-icon/>
  <p>每期按开班时间使用一版归属，此后沿用至下一版。已确认结算保留原版本。</p>
  <template v-if="mode && canWrite">
   <h3>{{ ({legacy:'导入已核定版本',import:'导入原表',director:'登记总监',member:'调整公司归属'})[mode] }}</h3>
   <a-form layout="vertical" :disabled="busy">
    <div class="director-fields">
     <a-form-item label="起始期次" required><a-select v-model:value="period" :options="periodsOptions" placeholder="选择已同步的家庭教育期次"/></a-form-item>
     <a-form-item v-if="mode==='import'" label="六列原表（xlsx）" required>
      <input type="file" accept=".xlsx" :disabled="busy" @change="chooseFile"/>
     </a-form-item>
     <a-form-item v-if="mode==='legacy'" label="原财务核定版本（JSON）" required>
      <input type="file" accept=".json" :disabled="busy" @change="chooseFile"/>
     </a-form-item>
     <a-form-item v-if="mode==='member'||mode==='director'" label="销售公司" required>
      <DirectorPartySelect :api="api" :period="period" kind="company" :value="company" :name="companyName" @change="setCompany"/>
     </a-form-item>
     <template v-if="mode==='director'">
      <a-form-item label="总监本人" required><DirectorPartySelect :api="api" :period="period" kind="person" :company="company" :value="person" @change="person=$event"/></a-form-item>
      <a-form-item label="表内部门" required><a-input v-model:value="department" :maxlength="100"/></a-form-item>
     </template>
     <a-form-item v-if="mode==='member'" label="新归属总监">
      <a-select v-model:value="directorId" :options="directors" allow-clear show-search option-filter-prop="label" placeholder="清空归属"/>
      <small v-if="company">当前归属 · {{membership?.leader_display_name || '未设置'}}</small>
     </a-form-item>
    </div>
   </a-form>
   <p v-if="mode==='import'">表头依次为序号、部门、组长/总监、组长销售公司、组员姓名、下属公司名称。可以逐行修正匹配，或将尚未查明的下属公司标为待核对。</p>
   <p v-if="mode==='legacy'">按原版本的起始期次和开班时间核对，保留已核定的本人、下属公司和收款方。多个版本请按起始期次先后逐一导入。</p>
   <p v-if="mode==='member'">幸福驿站类别的销售公司可指定总监。清空最后一家下属公司后，总监本人归属继续生效。</p>
   <a-space wrap>
    <a-button :disabled="busy" @click="resetMode">取消</a-button>
    <a-button :disabled="!period||busy" :loading="busy&&!valid" @click="request(false)">核对归属</a-button>
    <a-button type="primary" :disabled="!valid||busy" @click="request(true)">保存版本</a-button>
   </a-space>
   <template v-if="checked">
    <h3>核对结果 · {{checked.document.rows.length}} 条</h3>
    <a-alert v-if="!valid&&!checked.issues?.length" type="warning" message="匹配已调整，请重新核对。"/>
    <details v-if="checked.issues?.length" open><summary>有 {{checked.issues.length}} 项需要核对</summary>
     <ul><li v-for="(i,index) in checked.issues" :key="index">序号 {{i.number}} · {{i.detail}}</li></ul>
    </details>
    <details v-if="checked.changes?.length"><summary>归属变动（{{checked.changes.length}}）</summary>
     <a-table :columns="changes" :data-source="checked.changes" :row-key="r=>r.company+r.kind" size="small" :pagination="{pageSize:15}"/>
    </details>
   </template>
  </template>
  <template v-else>
   <label for="director-version">归属版本</label>
   <a-select id="director-version" :value="selected" :loading="loading" :options="versions.map(v=>({value:v.id,label:'V'+v.version+' · '+v.periodName}))"
     placeholder="暂无归属版本" @change="readDetail"/>
   <p v-if="detail">开班 {{formatTime(detail.startsAt)}} · {{detail.createdBy}} 于 {{formatTime(detail.createdAt)}} 保存 · {{detail.fileName}}<template v-if="detail.document?.source_version"> · 原财务 V{{detail.document.source_version}}</template></p>
   <details v-if="detail"><summary>版本依据及归属变动</summary>
    <p class="director-digest">文件 SHA-256 · {{detail.fileHash}}</p>
    <a-table :columns="changes" :data-source="detail.changes" :row-key="r=>r.company+r.kind" size="small" :pagination="{pageSize:15}"/>
   </details>
  </template>
  <a-table v-if="mode?checked:detail" :data-source="mode?checked.document.rows:detail.document.rows"
   :columns="mode?previewColumns:columns" row-key="number" :pagination="{pageSize:20,showSizeChanger:false,hideOnSinglePage:true}" :scroll="{x:820}" size="middle">
   <template #bodyCell="{column,record:r}">
    <template v-if="column.key==='leader'">
     {{r.leader_name}} · {{r.leader_company_name}}<small>匹配 · {{r.leader_display_name||'未匹配'}}（{{r.leader_source_user_id||'—'}}） · {{r.leader_company_display_name||'未匹配'}}（{{r.leader_source_company_id||'—'}}）</small>
    </template>
    <template v-else-if="column.key==='member'">
     <span v-if="r.director_only">独立总监</span>
     <template v-else>{{r.member_name}} {{r.member_company_name}}<small>{{r.pending_review?'待核对':r.member_company_display_name||'未匹配'}} {{r.member_source_company_id}}</small></template>
    </template>
    <template v-else-if="column.key==='recipient'">{{r.recipient_display_name||'待核对'}}</template>
    <a-button v-else-if="column.key==='edit'" type="link" size="small" :disabled="busy" @click="openBinding(r)">修正</a-button>
   </template>
  </a-table>
 </section>
 <a-drawer v-if="editRow" :open="true" width="min(450px,100vw)" :title="'修正匹配 · 序号 '+editRow.number" @close="editRow=undefined">
  <a-form layout="vertical">
   <a-form-item label="总监销售公司"><DirectorPartySelect :api="api" :period="period" kind="company" :value="editRow.companyId" :name="editRow.companyLabel"
     @change="(id,name)=>{editRow.companyId=id;editRow.companyLabel=name;editRow.leaderId='';editRow.leaderLabel=''}"/></a-form-item>
   <a-form-item label="总监本人"><DirectorPartySelect :api="api" :period="period" kind="person" :company="editRow.companyId" :value="editRow.leaderId" :name="editRow.leaderLabel"
     @change="(id,name)=>{editRow.leaderId=id;editRow.leaderLabel=name}"/></a-form-item>
   <a-form-item label="下属公司"><DirectorPartySelect :api="api" :period="period" kind="company" :value="editRow.memberId" :name="editRow.memberLabel" :disabled="editRow.pending"
     @change="(id,name)=>{editRow.memberId=id;editRow.memberLabel=name}"/></a-form-item>
   <a-checkbox v-model:checked="editRow.pending">下属公司待核对，本版暂不匹配该公司业绩</a-checkbox>
  </a-form>
  <template #footer><a-button type="primary" @click="acceptBinding">应用匹配</a-button></template>
 </a-drawer>
</template>
<style scoped>
.director-page { padding: 16px; }
.director-page h3 { margin-top: 20px; }
.director-page p, small { color: #495665; }
small { display: block; }
.director-fields { display: grid; grid-template-columns: repeat(auto-fit,minmax(min(220px,100%),1fr)); gap: 12px; }
.director-digest { overflow-wrap:anywhere; }
.director-page details { margin: 12px 0; }
.director-page details ul { max-height: 14rem; overflow: auto; }
.director-page > .ant-select { min-width: min(300px,100%); display:block; margin-bottom:12px; max-width:480px; }
</style>

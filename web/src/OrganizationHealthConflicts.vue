<script setup>
import { onMounted, ref } from 'vue'
const props = defineProps({api: Function, canWrite: Boolean})
const rows = ref([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const selected = ref()
const decision = ref('')
const evidence = ref('')
const path = '/organization-health-conflicts'
const states = {OPEN:'待处理', RESOLVING:'等待 AI 确认', RESOLVED:'已处理'}
const decisions = {ACCEPT_AI:'采纳 AI 资料', KEEP_FINANCE:'保留财务资料并纠正 AI', SUPERSEDED:'已采用后续 AI 资料'}
const reasons = {DIVERGENT_EDITS:'两端修改所依据的版本不一致', SOURCE_INVALID_FIELDS:'AI 资料缺少有效字段',SOURCE_STATE_CONFLICT:'组织类别、状态或人员映射冲突'}
const fields = {name:'名称',status:'状态',role:'角色',category_id:'类别编号',category_name:'类别',company_id:'公司编号',expected_company_external_id:'来源公司编号',
  external_member_id:'来源成员编号',external_user_id:'来源账号',user_id:'账号',company_type:'公司类型',parent_company_id:'上级公司编号',operation:'操作',
  desired_role:'目标角色',desired_state:'目标状态',is_owner:'负责人',destination_company_id:'调入公司编号',destination_company_external_id:'调入来源公司编号'}
const values = {ACTIVE:'启用',INACTIVE:'停用',MEMBER:'成员',OWNER:'负责人',EXTERNAL:'外部',INTERNAL:'内部',SET_OWNER:'设置负责人',
  UNSET_OWNER:'取消负责人',TRANSFER:'调动',DISABLE:'停用',ENABLE:'启用',RECONCILE:'纠正身份',CREATE:'创建'}
const columns = [{title:'AI 事件',dataIndex:'id'}, {title:'组织或成员',key:'entity'},{title:'冲突原因',key:'reason'}, {title:'状态',key:'state'}, {title:'',key:'action'}]
async function refresh(){
  loading.value=true
  error.value=''
  try { rows.value=await props.api(path) } catch(e){error.value=e.message}
  finally{loading.value=false}
}
function details(row){
  selected.value=row
  decision.value=''
  evidence.value=''
  error.value=''
}
function facts(data){
  return Object.entries(fields).filter(([key])=>data?.[key]!=null && data[key]!=='').map(([key,label])=>{
    const val=data[key]
    const text=key==='is_owner' ? (val===1?'是':'否') : key==='status' && typeof val==='number' ? (val===1?'启用':'停用') : values[val]||String(val)
    return [label,text]
  })
}
async function resolve(){
  const row=selected.value
  if(!row || !evidence.value.trim() || !decision.value) return
  saving.value=true
  error.value=''
  try {
    await props.api(path+'/resolve',{method:'POST',body:JSON.stringify({id:row.id,version:row.version,decision:decision.value,evidence:evidence.value.trim()})})
    notice.value=decision.value==='KEEP_FINANCE'?'纠正消息已提交，等待 AI 确认。':'已采纳 AI 资料。'
    selected.value=undefined
    await refresh()
  } catch(e){error.value=e.message}
  finally{saving.value=false}
}
onMounted(refresh)
defineExpose({refresh})
</script>

<template>
  <section class="records">
    <div class="records-head"><h2>AI 组织冲突</h2><a-button :loading="loading" @click="refresh">刷新</a-button></div>
    <a-alert v-if="error && !selected" type="error" :message="error" show-icon/>
    <a-alert v-if="notice" type="success" :message="notice" show-icon closable @close="notice=''"/>
    <a-table :columns="columns" :data-source="rows" row-key="id" :loading="loading" :pagination="{pageSize:15,hideOnSinglePage:true}" :scroll="{x:640}">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='entity'">{{ record.kind==='COMPANY'?'公司':'成员' }} · {{ record.ai.name || record.externalId }}</template>
        <template v-if="column.key==='reason'">{{ reasons[record.reason] || record.reason }}</template>
        <a-tag v-if="column.key==='state'">{{ states[record.state] }}</a-tag>
        <a-button v-if="column.key==='action'" size="small" type="link" @click="details(record)">{{ record.state==='OPEN' && canWrite?'核对处理':'查看详情' }}</a-button>
      </template>
      <template #emptyText>暂无 AI 组织冲突</template>
    </a-table>
    <a-drawer :open="!!selected" title="核对 AI 组织变更" :width="640" :closable="!saving" :mask-closable="!saving" @close="selected=undefined">
      <template v-if="selected">
        <a-alert v-if="error" type="error" :message="error" show-icon/>
        <p>AI 事件 {{ selected.id }} · 来源编号 {{ selected.externalId }} · {{ states[selected.state] }}</p>
        <p>财务当前版本 {{ selected.version }}，已收到 AI 回执的财务版本 {{ selected.appliedVersion }}，本次 AI 变更依据的财务版本 {{ selected.ai.finance_base_version<0?'未知':selected.ai.finance_base_version }}。</p>
        <template v-for="section in [['财务资料',selected.finance.observed],['最近财务操作',selected.finance.intent],['AI 原生资料',selected.ai]]" :key="section[0]">
          <a-descriptions v-if="facts(section[1]).length" :title="section[0]" bordered size="small" :column="1" style="margin:16px 0">
            <a-descriptions-item v-for="[label,value] in facts(section[1])" :key="label" :label="label">{{ value }}</a-descriptions-item>
          </a-descriptions>
        </template>
        <p v-if="selected.decision">处理记录 · {{ decisions[selected.decision] }} · {{ selected.evidence }}</p>
        <a-form v-if="canWrite && selected.state==='OPEN'" layout="vertical">
          <a-form-item label="处理方式">
            <a-radio-group v-model:value="decision"><a-radio value="ACCEPT_AI">采纳 AI 资料</a-radio><a-radio value="KEEP_FINANCE" :disabled="!selected.entityId">保留财务资料并纠正 AI</a-radio></a-radio-group>
          </a-form-item>
          <a-form-item label="处理依据"><a-textarea v-model:value="evidence" :maxlength="256" :rows="3"/></a-form-item>
          <a-button type="primary" :disabled="!decision || !evidence.trim()" :loading="saving" @click="resolve">确认处理</a-button>
        </a-form>
      </template>
    </a-drawer>
  </section>
</template>

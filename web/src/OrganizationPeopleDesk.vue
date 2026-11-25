<script setup>
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { PlusOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { Switch } from 'ant-design-vue'
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'

const props = defineProps({api:Function,canWrite:Boolean,personLink:Function,optionLabel:Function})
const creating = ref(false)
const scopeID = useId()
const term = ref('')
const scope = ref('')
const page = ref(1)
const people = ref({total:0,items:[]})
const loading = ref(false)
const error = ref('')
const statusPerson = ref()
const savingStatus = ref(false)
const statusError = ref('')
const statusNotice = ref('')
const enabling = computed(()=>statusPerson.value?.sourceStatus==='INACTIVE')
const scopes = [
  {label:'在用人员',value:''}, {label:'已停用人员',value:'archived'}, {label:'未归属销售公司',value:'unassigned'},
  {label:'公司归属冲突',value:'conflicts'}, {label:'已关联多个账号',value:'multiple-accounts'},
]
const sources = {DCZD:'大春之道',DCJK:'大春见康',DCAI:'大春 AI'}
let generation = 0
let timer
async function refresh() {
  clearTimeout(timer)
  const request = ++generation
  loading.value = true
  error.value = ''
  const params = new URLSearchParams({name:term.value.trim(),scope:scope.value,page:page.value})
  try {
    const value = await props.api('/organization-dispatch/people?'+params)
    if (generation===request) people.value = value
  } catch (e) {
    if (generation===request) {error.value=e.message;people.value={total:0,items:[]}}
  } finally {
    if (generation===request) loading.value=false
  }
}
watch(term,()=>{
  clearTimeout(timer)
  generation++
  page.value=1
  loading.value=true
  timer=setTimeout(refresh,220)
})
function setScope(value){scope.value=value;page.value=1;refresh()}
function setPage(value){page.value=value;refresh()}
function openNewPerson(id){
 creating.value=false
 refresh()
 location.hash=props.personLink(id)
}
function openStatus(record) {
  statusError.value=''
  statusNotice.value=''
  statusPerson.value=record
}
async function saveStatus() {
  if (savingStatus.value || !statusPerson.value) return
  const {id, name, statusVersion} = statusPerson.value
  const status = enabling.value ? 'ACTIVE' : 'INACTIVE'
  savingStatus.value=true
  statusError.value=''
  try {
    await props.api('/organization-dispatch/person-status', {
      method:'POST',
      body:JSON.stringify({personId:id,status,expectedVersion:statusVersion}),
    })
    statusPerson.value=undefined
    statusNotice.value=`${name}已${status==='ACTIVE' ? '启用' : '停用'}`
    if (page.value>1 && people.value.items.length===1) page.value--
    await refresh()
  } catch (e) {
    statusError.value=e.message
  } finally {
    savingStatus.value=false
  }
}
onBeforeUnmount(()=>{generation++;clearTimeout(timer)})
refresh()
defineExpose({refresh})
</script>
<template>
  <section class="records people-workspace" aria-label="人员办理">
    <div class="records-head people-heading">
      <div class="people-title"><h2>人员档案</h2><span v-if="!loading && !error" role="status">{{people.total}} 人</span></div>
      <a-button v-if="canWrite" type="primary" @click="creating=true"><template #icon><PlusOutlined aria-hidden="true"/></template>新建人员</a-button>
    </div>
    <div class="people-filters">
      <a-input v-model:value="term" placeholder="搜索姓名、账号 ID、组织或职责" aria-label="搜索人员" :maxlength="100" allow-clear @press-enter="refresh">
        <template #prefix><SearchOutlined aria-hidden="true"/></template>
      </a-input>
      <label class="sr-only" :for="scopeID">筛选人员</label>
      <a-select :id="scopeID" :value="scope" :options="scopes" style="width:164px" @change="setScope"/>
    </div>
    <a-alert v-if="statusNotice" class="people-status-notice" :message="statusNotice" type="success" show-icon closable @close="statusNotice=''"/>
    <a-alert v-if="error" :message="error" type="error" show-icon><template #action><a-button @click="refresh">重试</a-button></template></a-alert>
    <a-spin v-else :spinning="loading">
      <div class="people-list-heading">
        <div class="people-fields"><span>人员</span><span>当前任职</span><span>来源账号</span></div>
        <span>状态</span>
      </div>
      <ul v-if="people.items.length" class="people-list">
        <li v-for="record in people.items" :key="record.id" class="people-row">
          <a :href="personLink(record.id)" class="people-fields" :aria-labelledby="'people-name-'+record.id">
            <span class="people-row-name">
              <strong :id="'people-name-'+record.id">{{record.name}}</strong><small>ID {{record.id}}</small>
            </span>
            <span class="people-row-appointments">
              <template v-if="record.appointments.length">
                <span v-for="a in record.appointments.slice(0,2)" :key="[a.name,a.kind,a.position].join(':')">
                  {{a.name}}<small> · {{a.position || optionLabel('appointmentKind',a.kind) || a.kind}}</small>
                </span>
                <small v-if="record.appointments.length>2">另有 {{record.appointments.length-2}} 条任职</small>
              </template>
              <small v-else>暂无有效任职</small>
            </span>
            <span class="people-row-sources">
              <a-tag v-for="source in new Set(record.accounts.map(account=>account.source))" :key="source">{{sources[source] || source}}</a-tag>
              <small v-if="!record.accounts.length">暂无来源账号</small>
            </span>
          </a>
          <Switch :checked="record.sourceStatus!=='INACTIVE'" :disabled="!canWrite || !record.canChangeStatus"
            checked-children="启用" un-checked-children="停用" :aria-label="record.name+'的启用状态'" @change="openStatus(record)"/>
        </li>
      </ul>
      <a-empty v-else-if="!loading" :description="term ? '没有符合条件的人员' : '暂无人员记录'"/>
      <div v-if="people.total>20" class="people-pagination">
        <a-pagination :current="page" :total="people.total" :page-size="20" :show-size-changer="false" @change="setPage"/>
      </div>
    </a-spin>
  </section>
  <a-modal v-if="statusPerson" open :title="enabling ? '启用人员' : '停用人员'" :ok-text="enabling ? '确认启用' : '确认停用'"
    cancel-text="取消" :ok-button-props="{danger:!enabling}" :confirm-loading="savingStatus"
    :closable="!savingStatus" :keyboard="!savingStatus" :mask-closable="false" @ok="saveStatus" @cancel="!savingStatus && (statusPerson=undefined)">
    <p><strong>{{statusPerson.name}}</strong>将移入「{{enabling ? '在用人员' : '已停用人员'}}」。</p>
    <a-alert v-if="statusError" type="error" :message="statusError" show-icon/>
  </a-modal>
  <OrganizationPersonIntake v-if="creating" :api="api" @close="creating=false" @saved="openNewPerson"/>
</template>
<style scoped>
.people-workspace {max-width:1280px;margin-inline:auto}
.people-heading {display:flex;align-items:center;justify-content:space-between;padding:14px 16px 9px}
.people-title {display:flex;align-items:center;gap:10px}
.people-title h2 {font-size:18px}
.people-title span {border-radius:12px;padding:2px 8px;color:var(--muted);background:var(--surface,#f5f7fb);font-size:12px}
.people-filters {display:flex;align-items:center;flex-wrap:wrap;gap:8px 12px;padding:5px 16px 14px}
.people-filters .ant-input-affix-wrapper {max-width:360px;min-width:min(100%,220px);flex:1}
.people-list {list-style:none;margin:0;padding:0}
.people-status-notice {margin:0 16px 12px}
.people-list-heading,.people-row {display:grid;grid-template-columns:minmax(0,1fr) 92px;align-items:center}
.people-fields {display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.5fr) minmax(0,1fr);gap:12px 20px;padding:10px 16px;align-items:center}
.people-list-heading {background:var(--surface,#f5f7fb);color:var(--muted);font-weight:600;border-block:1px solid var(--line)}
.people-row {border-bottom:1px solid var(--line)}
.people-row > .people-fields {color:var(--ink);overflow-wrap:anywhere;align-self:stretch}
.people-row > .people-fields:hover,.people-row > .people-fields:focus-visible {background:var(--accent-soft)}
.people-row > .ant-switch {justify-self:start}
.people-row-name,.people-row-appointments {display:grid;justify-items:start;gap:2px}
.people-row-name strong {color:var(--accent)}
.people-row small {color:var(--muted)}
.people-row-sources {display:flex;flex-wrap:wrap;gap:4px}
.people-row-sources :deep(.ant-tag) {margin:0}
.people-pagination {display:flex;justify-content:flex-end;padding:12px 16px}
@media (max-width:680px){
 .people-list-heading {display:none}
 .people-row {grid-template-columns:minmax(0,1fr) 76px}
 .people-fields {grid-template-columns:1fr;gap:4px;padding:10px 14px}
 .people-row-name {display:flex;align-items:baseline;flex-wrap:wrap;gap:8px}
 .people-pagination :deep(.ant-pagination) {text-align:right}
}
</style>

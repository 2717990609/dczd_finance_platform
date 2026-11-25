<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'
import { PlusOutlined } from '@ant-design/icons-vue'
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'

const props = defineProps({api:Function,canWrite:Boolean,personLink:Function,optionLabel:Function})
const creating = ref(false)
const term = ref('')
const scope = ref('')
const page = ref(1)
const people = ref({total:0,items:[]})
const loading = ref(false)
const error = ref('')
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
onBeforeUnmount(()=>{generation++;clearTimeout(timer)})
refresh()
defineExpose({refresh})
</script>
<template>
  <section class="records" aria-label="人员办理">
    <div class="records-head people-heading">
      <div><h2>人员档案</h2><span class="order-version">共 {{ people.total }} 人</span></div>
      <a-button v-if="canWrite" type="primary" @click="creating=true"><template #icon><PlusOutlined aria-hidden="true"/></template>新建人员</a-button>
    </div>
    <div class="people-filters">
      <a-select :value="scope" :options="scopes" aria-label="筛选人员" style="width:164px" @change="setScope"/>
      <a-input-search v-model:value="term" placeholder="搜索姓名、账号 ID、组织或职责" aria-label="搜索人员" :maxlength="100" allow-clear @search="refresh"/>
    </div>
    <a-alert v-if="error" :message="error" type="error" show-icon><template #action><a-button @click="refresh">重试</a-button></template></a-alert>
    <a-spin v-else :spinning="loading">
      <div class="people-list-heading"><span>人员</span><span>当前任职</span><span>来源账号</span></div>
      <ul v-if="people.items.length" class="people-list">
        <li v-for="record in people.items" :key="record.id">
          <a :href="personLink(record.id)" class="people-row" :aria-labelledby="'people-name-'+record.id">
            <span class="people-row-name">
              <strong :id="'people-name-'+record.id">{{record.name}}</strong><small>ID {{record.id}}</small>
              <a-tag v-if="record.sourceStatus==='INACTIVE'">已停用</a-tag>
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
        </li>
      </ul>
      <a-empty v-else-if="!loading" :description="term ? '没有符合条件的人员' : '暂无人员记录'"/>
      <div v-if="people.total>20" class="people-pagination">
        <a-pagination :current="page" :total="people.total" :page-size="20" :show-size-changer="false" @change="setPage"/>
      </div>
    </a-spin>
  </section>
  <OrganizationPersonIntake v-if="creating" :api="api" @close="creating=false" @saved="openNewPerson"/>
</template>
<style scoped>
.people-heading {display:flex;align-items:center;justify-content:space-between}
.people-filters {display:flex;align-items:center;flex-wrap:wrap;gap:10px 18px;padding:12px 16px}
.people-filters .ant-input-search {max-width:360px;min-width:min(100%,220px);flex:1}
.people-list {list-style:none;margin:0;padding:0}
.people-list-heading,.people-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.5fr) minmax(0,1fr);gap:12px 20px;padding:10px 16px;align-items:center}
.people-list-heading {background:var(--accent-soft);color:var(--muted);font-weight:600}
.people-row {border-bottom:1px solid var(--line);color:var(--ink);overflow-wrap:anywhere}
.people-row:hover,.people-row:focus-visible {background:var(--accent-soft)}
.people-row-name,.people-row-appointments {display:grid;justify-items:start;gap:2px}
.people-row-name strong {color:var(--accent)}
.people-row small {color:var(--muted)}
.people-row-sources {display:flex;flex-wrap:wrap;gap:4px}
.people-row-sources :deep(.ant-tag) {margin:0}
.people-pagination {display:flex;justify-content:flex-end;padding:12px 16px}
@media (max-width:680px){
 .people-list-heading {display:none}
 .people-row {grid-template-columns:1fr;gap:4px;padding:10px 14px}
 .people-row-name {display:flex;align-items:baseline;flex-wrap:wrap;gap:8px}
 .people-pagination :deep(.ant-pagination) {text-align:right}
}
</style>

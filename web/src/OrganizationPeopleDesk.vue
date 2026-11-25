<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { PlusOutlined } from '@ant-design/icons-vue'
import OrganizationPersonIntake from './OrganizationPersonIntake.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'

const props = defineProps({api:Function,canWrite:Boolean,formatTime:Function,personLink:Function,optionLabel:Function})
const creating = ref(false)
const notice = ref('')
const term = ref('')
const scope = ref('')
const page = ref(1)
const people = ref({total:0,items:[]})
const loading = ref(false)
const error = ref('')
const person = ref()
const scopes = [
  {label:'在用人员',value:''}, {label:'已停用人员',value:'archived'}, {label:'未归属销售公司',value:'unassigned'},
  {label:'公司归属冲突',value:'conflicts'}, {label:'已关联多个账号',value:'multiple-accounts'},
]
const sources = {DCZD:'大春之道',DCJK:'大春见康',DCAI:'大春 AI'}
const columns = [
  {title:'人员',key:'person',width:190},
  {title:'组织 / 销售公司',key:'appointments',width:210},
  {title:'业务来源账号',key:'accounts',width:175},
  {title:'业务职责',key:'duties',width:180},
  {title:'操作',key:'action',width:124,fixed:'right'},
]
const pagination = computed(()=>({current:page.value,total:people.value.total,pageSize:20,hideOnSinglePage:true,showSizeChanger:false}))
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
function setPage(value){page.value=value.current;refresh()}
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
    <a-alert v-if="notice" :message="notice" type="success" show-icon closable @close="notice=''"/>
    <div class="people-filters">
      <a-select :value="scope" :options="scopes" aria-label="筛选人员" style="width:164px" @change="setScope"/>
      <a-input-search v-model:value="term" placeholder="搜索姓名、账号 ID、组织或职责" aria-label="搜索人员" :maxlength="100" allow-clear @search="refresh"/>
    </div>
    <a-alert v-if="error" :message="error" type="error" show-icon><template #action><a-button @click="refresh">重试</a-button></template></a-alert>
    <a-table v-else size="small" row-key="id" :columns="columns" :data-source="people.items" :loading="loading" :pagination="pagination" :scroll="{x:855}" @change="setPage">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='person'">
          <a class="person-name-link" :href="personLink(record.id)">{{ record.name }}</a><small class="order-version">ID {{ record.id }}</small>
        </template>
        <template v-else-if="column.key==='appointments'">
          <div v-for="a in record.appointments" :key="[a.name,a.kind,a.position].join(':')">{{ a.name }} · {{ a.position || optionLabel('appointmentKind',a.kind) || a.kind }}</div>
          <span v-if="!record.appointments.length">—</span>
        </template>
        <template v-else-if="column.key==='accounts'">
          <div v-for="a in record.accounts" :key="a.source+':'+a.id">{{ sources[a.source] || a.source }} · {{ a.id }}</div><span v-if="!record.accounts.length">—</span>
        </template>
        <template v-else-if="column.key==='duties'">{{ record.duties.join('、') || '—' }}</template>
        <a-button v-else-if="column.key==='action'" type="link" size="small" @click="person=record">{{ canWrite && record.sourceStatus!=='INACTIVE' ? '同步销售身份' : '查看同步' }}</a-button>
      </template>
      <template #emptyText>{{ term ? '没有符合条件的人员' : '暂无人员记录' }}</template>
    </a-table>
  </section>
  <OrganizationPersonIntake v-if="creating" :api="api" @close="creating=false" @saved="id=>{creating=false;term='';scope='';page=1;notice='人员已保存 · ID '+id;refresh()}"/>
  <OrganizationDispatch v-if="person" :key="person.id" :api="api" :person-id="person.id" :person-name="person.name"
    :can-write="canWrite && person.sourceStatus!=='INACTIVE'" :format-time="formatTime" @changed="refresh" @close="person=undefined"/>
</template>
<style scoped>
.people-heading {display:flex;align-items:center;justify-content:space-between}
.people-filters {display:flex;align-items:center;flex-wrap:wrap;gap:10px 18px;padding:12px 16px}
.people-filters .ant-input-search {max-width:360px;min-width:180px;flex:1}
</style>

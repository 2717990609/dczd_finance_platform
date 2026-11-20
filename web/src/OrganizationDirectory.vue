<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Checkbox, TreeSelect } from 'ant-design-vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import CatalogAppointments from './CatalogAppointments.vue'
import { organizationTree } from './organization-tree'

const props = defineProps({api:Function, optionLabel:Function, formatTime:Function, canWrite:Boolean, appointmentKinds:Array,
  catalogRoles:Array,catalogScopes:Array})
const units = ref([])
const unitId = ref()
const name = ref('')
const search = ref('')
const on = ref('')
const history = ref(false)
const page = ref(1)
const entries = ref({total:0,items:[]})
const unitsError = ref('')
const entriesError = ref('')
const loading = ref(false)
const editor = ref()
const notice = ref('')
const panel = ref('organization')
const catalog = ref()
let request = 0
let unitRequest = 0

const kindLabel = kind => props.optionLabel('organizationKind',kind)
const tree = computed(() => organizationTree(units.value,kindLabel))
const appointmentTree = computed(() => organizationTree(units.value,kindLabel,true))
const columns = computed(() => [
  {title:'人员',dataIndex:'personName',key:'personName',width:130},
  {title:'组织',dataIndex:'unitName',key:'unitName',width:180},
  {title:'任职',dataIndex:'kind',key:'kind',width:135},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:145},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:145},
  ...(props.canWrite ? [{title:'操作',key:'actions',width:124}] : []),
])
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false}))

async function loadUnits() {
  const version = ++unitRequest
  unitsError.value = ''
  try {
    const rows = await props.api('/organization-units')
    if (version === unitRequest) units.value = rows
  } catch (error) {
    if (version === unitRequest) unitsError.value = error.message
  }
}

async function loadEntries() {
  const version = ++request
  loading.value = true
  entriesError.value = ''
  const params = new URLSearchParams({page:page.value})
  if (unitId.value) params.set('unitId',unitId.value)
  if (search.value) params.set('name',search.value)
  if (history.value) params.set('history','true')
  else if (on.value) params.set('on',on.value)
  try {
    const result = await props.api('/organization-assignments?'+params)
    if (version === request) entries.value = result
  } catch (error) {
    if (version === request) {
      entries.value = {total:0,items:[]}
      entriesError.value = error.message
    }
  } finally {
    if (version === request) loading.value = false
  }
}

function refresh() {
  return panel.value==='catalog' ? catalog.value?.refresh() : Promise.all([loadUnits(),loadEntries()])
}
function filterEntries() {
  page.value = 1
  loadEntries()
}
function searchPeople() {
  search.value = name.value.trim()
}
function changePage(pagination) {
  page.value = pagination.current
  loadEntries()
}
function openEditor(mode, record) {
  notice.value = ''
  editor.value = {mode,record}
}
function saved(message) {
  editor.value = undefined
  notice.value = message
  loadEntries()
}
watch([unitId,on,history,search],filterEntries)
onMounted(refresh)
onBeforeUnmount(() => { request++; unitRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-tabs v-model:active-key="panel" class="workbench-tabs">
    <a-tab-pane key="organization" tab="组织任职"/>
    <a-tab-pane key="catalog" tab="计提任职"/>
  </a-tabs>
  <CatalogAppointments v-if="panel==='catalog'" ref="catalog" :api="api" :can-write="canWrite"
    :format-time="formatTime" :scopes="catalogScopes" :roles="catalogRoles"/>
  <template v-else>
  <a-alert v-if="unitsError || entriesError" class="feedback" type="error" :message="unitsError || entriesError" show-icon/>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
  <section class="records organization-directory">
    <div class="organization-filters">
      <div>
        <label for="organization-unit">组织</label>
        <TreeSelect id="organization-unit" aria-label="组织" v-model:value="unitId" :tree-data="tree" tree-node-filter-prop="title" placeholder="全部组织" show-search allow-clear/>
      </div>
      <div>
        <label for="organization-date">任职日期</label>
        <input id="organization-date" v-model="on" type="date" class="ant-input organization-date" :disabled="history"/>
      </div>
      <div>
        <label for="organization-name">人员</label>
        <a-input-search id="organization-name" v-model:value="name" placeholder="输入姓名" allow-clear @search="searchPeople" @change="!name && searchPeople()"/>
      </div>
      <Checkbox v-model:checked="history">显示全部任职记录</Checkbox>
    </div>
    <div class="records-head">
      <h2>人员任职</h2>
      <span class="organization-context">{{ history ? '全部有效区间' : on ? on+' 00:00（北京时间）' : '当前归属' }}</span>
      <a-button v-if="canWrite" type="primary" @click="openEditor('add')">登记任职</a-button>
    </div>
    <a-table :columns="columns" :data-source="entries.items" :loading="loading" row-key="id" :pagination="pagination"
      :scroll="{x:canWrite?859:735}" @change="changePage">
      <template #bodyCell="{column,record,text}">
        <template v-if="column.key==='kind'">
          {{ optionLabel('appointmentKind',text) }}<small v-if="record.position" class="order-version">{{ record.position }}</small>
        </template>
        <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
        <a-space v-else-if="column.key==='actions'" :size="1">
          <a-button type="link" size="small" @click="openEditor('transfer',record)">调动</a-button>
          <a-button type="link" size="small" @click="openEditor('end',record)">结束</a-button>
        </a-space>
      </template>
      <template #emptyText>当前条件下没有任职记录</template>
    </a-table>
  </section>
  <OrganizationAppointmentForm v-if="canWrite && editor" :api="api" :mode="editor.mode" :record="editor.record"
    :tree="appointmentTree" :kinds="appointmentKinds" :unit-id="units.some(unit=>unit.id===unitId && unit.status==='ACTIVE')?unitId:undefined"
    @saved="saved" @close="editor=undefined"/>
  </template>
</template>

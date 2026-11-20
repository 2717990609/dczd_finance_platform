<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import CatalogAppointments from './CatalogAppointments.vue'
import OrganizationCompanies from './OrganizationCompanies.vue'
import OrganizationDuties from './OrganizationDuties.vue'
import WorkspaceOrganizationScope from './WorkspaceOrganizationScope.vue'
import { organizationTree } from './organization-tree'

const props = defineProps({api:Function, optionLabel:Function, formatTime:Function, canWrite:Boolean, appointmentKinds:Array,
  catalogRoles:Array,catalogScopes:Array})
const units = ref([])
const unitId = ref()
const name = ref('')
const search = ref('')
const on = ref('')
const dateMode = ref('current')
const page = ref(1)
const entries = ref({total:0,items:[]})
const unitsError = ref('')
const entriesError = ref('')
const loading = ref(false)
const unitsLoading = ref(false)
const editor = ref()
const notice = ref('')
const panel = ref('organization')
const catalog = ref()
const companies = ref()
const duties = ref()
const dutySubject = ref()
const person = ref()
const personPage = ref(1)
const personEntries = ref({total:0,items:[]})
const personError = ref('')
const personLoading = ref(false)
let request = 0
let unitRequest = 0
let personRequest = 0

const kindLabel = kind => props.optionLabel('organizationKind',kind)
const tree = computed(() => organizationTree(units.value,kindLabel))
const appointmentTree = computed(() => organizationTree(units.value,kindLabel,true))
const selectedUnit = computed(() => units.value.find(unit=>unit.id===unitId.value))
const waitingForDate = computed(() => dateMode.value==='date' && !on.value)
const emptyMessage = computed(() => waitingForDate.value
  ? '请选择任职日期'
  : selectedUnit.value ? '所选组织在该条件下没有直接任职' : '当前条件下没有任职记录')
const fields = [
  {title:'人员',dataIndex:'personName',key:'personName',width:130},
  {title:'组织',dataIndex:'unitName',key:'unitName',width:180},
  {title:'任职',dataIndex:'kind',key:'kind',width:135},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:145},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:145},
]
const columns = computed(() => [
  ...fields,
  ...(props.canWrite ? [{title:'操作',key:'actions',width:124,fixed:'right'}] : []),
])
const personColumns = fields.filter(column=>column.key!=='personName')
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false,hideOnSinglePage:true}))
const personPagination = computed(() => ({current:personPage.value,pageSize:25,total:personEntries.value.total,showSizeChanger:false,hideOnSinglePage:true}))

async function loadUnits() {
  const version = ++unitRequest
  unitsError.value = ''
  unitsLoading.value = true
  try {
    const rows = await props.api('/organization-units')
    if (version === unitRequest) units.value = rows
  } catch (error) {
    if (version === unitRequest) unitsError.value = error.message
  } finally {
    if (version === unitRequest) unitsLoading.value = false
  }
}

async function loadEntries() {
  const version = ++request
  entriesError.value = ''
  if (waitingForDate.value) {
    entries.value = {total:0,items:[]}
    loading.value = false
    return
  }
  loading.value = true
  const params = new URLSearchParams({page:page.value})
  if (unitId.value) params.set('unitId',unitId.value)
  if (search.value) params.set('name',search.value)
  if (dateMode.value==='history') params.set('history','true')
  if (dateMode.value==='date') params.set('on',on.value)
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

async function loadPersonHistory() {
  const version = ++personRequest
  personLoading.value = true
  personError.value = ''
  const params = new URLSearchParams({history:'true',personId:person.value.personId,page:personPage.value})
  try {
    const result = await props.api('/organization-assignments?'+params)
    if (version===personRequest) personEntries.value = result
  } catch (error) {
    if (version===personRequest) personError.value = error.message
  } finally {
    if (version===personRequest) personLoading.value = false
  }
}
function openPersonHistory(record) {
  person.value = record
  personPage.value = 1
  personEntries.value = {total:0,items:[]}
  loadPersonHistory()
}
function closePersonHistory() {
  personRequest++
  person.value = undefined
}
function changePersonPage(pagination) {
  personPage.value = pagination.current
  loadPersonHistory()
}

function refresh() {
  if (panel.value==='catalog') return catalog.value?.refresh()
  if (panel.value==='companies') return companies.value?.refresh()
  if (panel.value==='duties') return duties.value?.refresh()
  return Promise.all([loadUnits(),loadEntries()])
}
function showCompanyAppointments(id) {
  unitId.value = id
  panel.value = 'organization'
}
function showDuties(subject) {
  closePersonHistory()
  dutySubject.value = subject
  panel.value = 'duties'
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
watch([unitId,on,dateMode,search],filterEntries)
onMounted(refresh)
onBeforeUnmount(() => { request++; unitRequest++; personRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-tabs v-model:active-key="panel" class="workbench-tabs">
    <a-tab-pane key="organization" tab="人员归属"/>
    <a-tab-pane key="companies" tab="销售公司与类别"/>
    <a-tab-pane key="duties" tab="业务职责"/>
    <a-tab-pane key="catalog" tab="分润岗位"/>
  </a-tabs>
  <KeepAlive>
    <CatalogAppointments v-if="panel==='catalog'" ref="catalog" :api="api" :can-write="canWrite"
      :format-time="formatTime" :scopes="catalogScopes" :roles="catalogRoles"/>
  </KeepAlive>
  <KeepAlive>
    <OrganizationCompanies v-if="panel==='companies'" ref="companies" :api="api" :format-time="formatTime" :can-write="canWrite"
      @show-appointments="showCompanyAppointments" @show-duties="showDuties"/>
  </KeepAlive>
  <KeepAlive>
    <OrganizationDuties v-if="panel==='duties'" ref="duties" :api="api" :format-time="formatTime" :can-write="canWrite"
      :units="units" :subject="dutySubject" @clear-subject="dutySubject=undefined"/>
  </KeepAlive>
  <div v-show="panel==='organization'">
    <a-alert v-if="unitsError || entriesError" class="feedback" type="error" :message="unitsError || entriesError" show-icon/>
    <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
    <div class="records organization-browser">
      <div class="scope-panel organization-scope">
        <WorkspaceOrganizationScope :nodes="tree" :selected="unitId" :loading="unitsLoading" @select="unitId=$event"/>
      </div>
      <section class="organization-directory">
        <div class="records-head">
          <div class="organization-heading">
            <h2>人员任职</h2>
            <span class="organization-context">{{ selectedUnit ? selectedUnit.name+' · 直接任职' : '全部组织' }}</span>
          </div>
          <a-button v-if="canWrite" type="primary" @click="openEditor('add')">登记任职</a-button>
        </div>
        <div class="organization-filters organization-person-filters">
          <div>
            <label for="organization-name">人员姓名</label>
            <a-input-search id="organization-name" v-model:value="name" placeholder="输入姓名" allow-clear
              @search="searchPeople" @change="!name && searchPeople()"/>
          </div>
          <AppointmentDateFilter v-model:mode="dateMode" v-model:date="on"/>
        </div>
        <a-table size="middle" :columns="columns" :data-source="entries.items" :loading="loading" row-key="id" :pagination="pagination"
          :scroll="{x:canWrite?859:735}" @change="changePage">
          <template #bodyCell="{column,record,text}">
            <a-button v-if="column.key==='personName'" class="organization-person-link" type="link"
              :aria-label="'查看'+text+'的任职历史'" @click="openPersonHistory(record)">{{ text }}</a-button>
            <template v-else-if="column.key==='kind'">
              {{ optionLabel('appointmentKind',text) }}
              <a-tag v-if="record.appointmentType && record.appointmentType!==text">{{ optionLabel('appointmentKind',record.appointmentType) }}</a-tag><small v-if="record.position" class="order-version">{{ record.position }}</small>
            </template>
            <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
            <a-space v-else-if="column.key==='actions'" :size="1">
              <a-button type="link" size="small" @click="openEditor('transfer',record)">调动</a-button>
              <a-button type="link" size="small" @click="openEditor('end',record)">结束</a-button>
            </a-space>
          </template>
          <template #emptyText>{{ emptyMessage }}</template>
        </a-table>
      </section>
    </div>
  </div>
  <OrganizationAppointmentForm v-if="canWrite && editor" :api="api" :mode="editor.mode" :record="editor.record"
    :tree="appointmentTree" :kinds="appointmentKinds" :unit-id="selectedUnit?.status==='ACTIVE' ? unitId : undefined"
    @saved="saved" @close="editor=undefined"/>
  <a-drawer v-if="person" :title="person.personName+' · 组织任职历史'" :open="true" width="min(720px, 100vw)" @close="closePersonHistory">
    <a-button class="feedback" @click="showDuties({personId:person.personId,name:person.personName})">查看业务职责</a-button>
    <a-alert v-if="personError" class="feedback" type="error" :message="personError" show-icon>
      <template #action><a-button size="small" @click="loadPersonHistory">重试</a-button></template>
    </a-alert>
    <a-table v-else size="middle" :columns="personColumns" :data-source="personEntries.items" :loading="personLoading" row-key="id"
      :pagination="personPagination" :scroll="{x:605}" @change="changePersonPage">
      <template #bodyCell="{column,record,text}">
        <template v-if="column.key==='kind'">
          {{ optionLabel('appointmentKind',text) }}
              <a-tag v-if="record.appointmentType && record.appointmentType!==text">{{ optionLabel('appointmentKind',record.appointmentType) }}</a-tag><small v-if="record.position" class="order-version">{{ record.position }}</small>
        </template>
        <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
      </template>
      <template #emptyText>暂无任职历史</template>
    </a-table>
  </a-drawer>
</template>

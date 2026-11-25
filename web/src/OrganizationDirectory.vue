<script setup>
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import OrganizationDispatchCreateUnit from './OrganizationDispatchCreateUnit.vue'
import OrganizationDispatchUnit from './OrganizationDispatchUnit.vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import PersonAppointmentActions from './PersonAppointmentActions.vue'
import CatalogAppointments from './CatalogAppointments.vue'
import OrganizationDirectors from './OrganizationDirectors.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import OrganizationCompanies from './OrganizationCompanies.vue'
import OrganizationHealthConflicts from './OrganizationHealthConflicts.vue'
import OrganizationDuties from './OrganizationDuties.vue'
import OrganizationPositions from './OrganizationPositions.vue'
import WorkspaceOrganizationScope from './WorkspaceOrganizationScope.vue'
import { organizationTree } from './organization-tree'

const props = defineProps({api:Function, optionLabel:Function, formatTime:Function, canWrite:Boolean, appointmentKinds:Array,
  catalogRoles:Array,catalogScopes:Array,personLink:Function,panel:String})
const emit = defineEmits(['update:panel','position-changed'])
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
const dispatchUnit=ref(),createUnit=ref(false)
async function savedUnit(id){createUnit.value=false;await loadUnits();unitId.value=id}
onDeactivated(()=>{dispatchUnit.value=undefined;createUnit.value=false})
watch(()=>props.panel,()=>{dispatchUnit.value=undefined;createUnit.value=false})
const positionsOpen = ref(false)
const notice = ref('')
const catalog = ref()
const companies = ref()
const sync = ref()
const directorPanel = ref()
const directorCompany = ref()
const health = ref()
const duties = ref()
const dutySubject = ref()
let request = 0
let unitRequest = 0

const kindLabel = kind => props.optionLabel('organizationKind',kind)
const tree = computed(() => organizationTree(units.value,kindLabel))
const appointmentTree = computed(() => organizationTree(units.value,kindLabel,true))
const includeChildren = ref(true)
const selectedUnit = computed(() => units.value.find(unit=>unit.id===unitId.value))
const hasChildren = computed(() => !!unitId.value && units.value.some(unit=>unit.parentId===unitId.value))
const waitingForDate = computed(() => dateMode.value==='date' && !on.value)
const emptyMessage = computed(() => waitingForDate.value
  ? '请选择任职日期'
  : '当前条件下没有任职记录')
const fields = [
  {title:'人员',dataIndex:'personName',key:'personName',width:130},
  {title:'组织',dataIndex:'unitName',key:'unitName',width:180},
  {title:'任职',dataIndex:'kind',key:'kind',width:135},
  {title:'任职时间',key:'period',width:205},
]
const columns = computed(() => [
  ...fields,
  ...(props.canWrite ? [{title:'',key:'actions',width:120}] : []),
])
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false,hideOnSinglePage:true}))

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
  if (unitId.value) {
    params.set('unitId',unitId.value)
    if (hasChildren.value && includeChildren.value) params.set('includeChildren','true')
  }
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

function refresh() {
  if (props.panel==='catalog') return catalog.value?.refresh()
  if (props.panel==='sync') return sync.value?.refresh()
  if (props.panel==='companies') return companies.value?.refresh()
  if (props.panel==='directors') return directorPanel.value?.refresh()
  if (props.panel==='health') return health.value?.refresh()
  if (props.panel==='duties') return duties.value?.refresh()
  return Promise.all([loadUnits(),loadEntries()])
}
function showCompanyAppointments(id) {
  unitId.value = id
  emit('update:panel','organization')
}
function showDuties(subject) {
  dutySubject.value = subject
  emit('update:panel','duties')
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
watch([unitId,hasChildren,includeChildren,on,dateMode,search],filterEntries)
onMounted(() => { loadUnits(); loadEntries() })
onBeforeUnmount(() => { request++; unitRequest++ })
defineExpose({refresh})
</script>

<template>
  <KeepAlive>
    <CatalogAppointments v-if="panel==='catalog'" ref="catalog" :api="api" :can-write="canWrite"
      :format-time="formatTime" :scopes="catalogScopes" :roles="catalogRoles" :person-link="personLink"/>
  </KeepAlive>
  <KeepAlive>
    <OrganizationCompanies v-if="panel==='companies'" ref="companies" :api="api" :format-time="formatTime" :can-write="canWrite" :units="units" @directory-changed="loadUnits"
      @show-director="company=>{directorCompany=company.companyType==='UNKNOWN' ? undefined : company;emit('update:panel','directors')}" @show-appointments="showCompanyAppointments" @show-duties="showDuties"/>
  </KeepAlive>
  <KeepAlive>
    <OrganizationDuties v-if="panel==='duties'" ref="duties" :api="api" :format-time="formatTime" :can-write="canWrite"
      :units="units" :subject="dutySubject" :person-link="personLink" @clear-subject="dutySubject=undefined"/>
  </KeepAlive>
  <OrganizationDirectors v-if="panel==='directors'" ref="directorPanel" :api="api" :can-write="canWrite" :format-time="formatTime"
    :company="directorCompany" @clear-company="directorCompany=undefined"/>
  <OrganizationDispatch v-if="panel==='sync'" ref="sync" embedded :api="api" :can-write="canWrite" :format-time="formatTime"/>
  <OrganizationHealthConflicts v-if="panel==='health'" ref="health" :api="api" :can-write="canWrite"/>
  <div v-show="panel==='organization'" class="organization-workspace">
    <a-alert v-if="unitsError || entriesError" class="feedback" type="error" :message="unitsError || entriesError" show-icon/>
    <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
    <div class="records organization-browser">
      <div class="scope-panel organization-scope">
        <div class="organization-tree-actions">
          <a-button v-if="canWrite" size="small" @click="createUnit=true">新建组织</a-button>
          <a-button size="small" type="text" @click="positionsOpen=true">岗位目录</a-button>
        </div>
        <WorkspaceOrganizationScope :nodes="tree" :selected="unitId" :loading="unitsLoading" fill-height @select="unitId=$event"/>
      </div>
      <section class="organization-directory">
        <div class="records-head">
          <div class="organization-heading">
            <h2>人员任职</h2>
            <span class="organization-context">{{ selectedUnit?.name || '全部组织' }}</span>
          </div>
          <a-space wrap>
            <a-button v-if="selectedUnit" type="text" @click="dispatchUnit=selectedUnit">关联销售公司</a-button>
            <a-button v-if="canWrite" type="primary" @click="openEditor('add')">登记任职</a-button>
          </a-space>
        </div>
        <div class="organization-filters organization-person-filters">
          <div>
            <label for="organization-name">人员姓名</label>
            <a-input-search id="organization-name" v-model:value="name" placeholder="输入姓名" allow-clear
              @search="searchPeople" @change="!name && searchPeople()"/>
          </div>
          <div class="organization-date-options">
            <AppointmentDateFilter v-model:mode="dateMode" v-model:date="on"/>
            <a-checkbox v-if="hasChildren" v-model:checked="includeChildren">包含下级组织</a-checkbox>
          </div>
        </div>
        <a-table size="middle" :columns="columns" :data-source="entries.items" :loading="loading" row-key="id" :pagination="pagination"
          :scroll="{x:canWrite?770:650}" @change="changePage">
          <template #bodyCell="{column,record,text}">
            <a v-if="column.key==='personName'" class="person-name-link" :href="personLink(record.personId)">{{ text }}</a>
            <template v-else-if="column.key==='kind'">
              {{ optionLabel('appointmentKind',text) }}
              <a-tag v-if="record.appointmentType && record.appointmentType!==text">{{ optionLabel('appointmentKind',record.appointmentType) }}</a-tag><small v-if="record.position" class="order-version">{{ record.position }}</small>
            </template>
            <template v-else-if="column.key==='period'">
              <span class="numeric">{{formatTime(record.validFrom)}} 起</span>
              <small v-if="record.validTo" class="order-version numeric">{{formatTime(record.validTo)}} 止</small>
            </template>
            <PersonAppointmentActions v-else-if="column.key==='actions'" :label="record.unitName" @select="mode=>openEditor(mode,record)"/>
          </template>
          <template #emptyText>{{ emptyMessage }}</template>
        </a-table>
      </section>
    </div>
  </div>
  <OrganizationDispatchCreateUnit v-if="createUnit" :api="api" :units="units" :parent-id="selectedUnit?.id" @close="createUnit=false" @saved="savedUnit"/>
  <OrganizationDispatchUnit v-if="dispatchUnit" :key="dispatchUnit.id" :unit="dispatchUnit" :api="api" :can-write="canWrite" :format-time="formatTime" @close="dispatchUnit=undefined"/>
  <OrganizationPositions v-if="positionsOpen && panel==='organization'" :api="api" :can-write="canWrite" :scopes="catalogScopes"
    @close="positionsOpen=false" @changed="emit('position-changed')"/>
  <OrganizationAppointmentForm v-if="canWrite && editor" :api="api" :mode="editor.mode" :record="editor.record"
    :tree="appointmentTree" :kinds="appointmentKinds" :unit-id="selectedUnit?.status==='ACTIVE' ? unitId : undefined"
    @saved="saved" @close="editor=undefined"/>
</template>

<style scoped>
.organization-workspace {max-width:1280px;margin-inline:auto}
@media(width>680px){.organization-browser .organization-scope {grid-template-rows:auto minmax(0,1fr)}}
.organization-tree-actions {display:flex;align-items:center;flex-wrap:wrap;gap:4px;margin-bottom:10px}
.organization-date-options {display:flex;flex-wrap:wrap;align-items:end;gap:8px 14px}
.organization-date-options :deep(.ant-checkbox-wrapper) {display:inline-flex;align-items:center;margin:0}
</style>

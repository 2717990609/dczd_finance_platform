<script setup>
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import OrganizationDispatchCreateUnit from './OrganizationDispatchCreateUnit.vue'
import OrganizationDispatchUnit from './OrganizationDispatchUnit.vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import OrganizationAssignmentList from './OrganizationAssignmentList.vue'
import { ApartmentOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons-vue'
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
let searchTimer

const kindLabel = kind => props.optionLabel('organizationKind',kind)
const tree = computed(() => organizationTree(units.value,kindLabel))
const appointmentTree = computed(() => organizationTree(units.value,kindLabel,true))
const includeChildren = ref(true)
const selectedUnit = computed(() => units.value.find(unit=>unit.id===unitId.value))
const hasChildren = computed(() => !!unitId.value && units.value.some(unit=>unit.parentId===unitId.value))
const waitingForDate = computed(() => dateMode.value==='date' && !on.value)
const emptyMessage = computed(() => {
  if (entriesError.value) return ''
  if (waitingForDate.value) return '请选择任职日期'
  if (search.value) return '未找到匹配的任职'
  if (dateMode.value==='date') return '所选日期暂无任职'
  return dateMode.value==='current' ? '暂无当前任职' : '暂无任职记录'
})
const filtersActive = computed(() => !!name.value.trim() || dateMode.value!=='current' || hasChildren.value && !includeChildren.value)
function clearFilters() {
  name.value = ''
  searchPeople()
  dateMode.value = 'current'
  on.value = ''
  includeChildren.value = true
}

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
function selectOrganization(id) {
  if(unitId.value!==id)notice.value=''
  unitId.value=id
}
function showCompanyAppointments(id) {
  selectOrganization(id)
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
  clearTimeout(searchTimer)
  search.value = name.value.trim()
}
function changePage(nextPage) {
  page.value = nextPage
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
watch(name,value=>{
  clearTimeout(searchTimer)
  if(!value.trim())searchPeople()
  else searchTimer=setTimeout(searchPeople,220)
})
watch([unitId,hasChildren,includeChildren,on,dateMode,search],filterEntries)
onMounted(() => { loadUnits(); loadEntries() })
onBeforeUnmount(() => { clearTimeout(searchTimer); request++; unitRequest++ })
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
    <div class="organization-browser">
      <aside class="records scope-panel organization-scope" aria-label="组织目录">
        <div class="organization-tree-actions">
          <span><ApartmentOutlined aria-hidden="true"/> 组织目录</span>
          <a-space :size="2">
            <a-button class="organization-positions-link" type="text" size="small" @click="positionsOpen=true">岗位目录</a-button>
            <a-button v-if="canWrite" size="small" type="text" aria-label="新建组织" @click="createUnit=true">
              <template #icon><PlusOutlined aria-hidden="true"/></template>新建
            </a-button>
          </a-space>
        </div>
        <WorkspaceOrganizationScope :nodes="tree" :selected="unitId" :loading="unitsLoading" fill-height
          hide-search-label @select="selectOrganization"/>
      </aside>
      <section class="records organization-directory" aria-label="人员任职">
        <header class="organization-directory-header">
          <div class="organization-heading">
            <h2>{{selectedUnit?.name || '全部组织'}}</h2>
            <a-tag v-if="selectedUnit?.status==='INACTIVE'" class="organization-inactive">已停用</a-tag>
            <span v-if="!loading && !entriesError && !waitingForDate" class="organization-count" role="status">{{entries.total}} 条任职</span>
          </div>
          <a-space wrap>
            <a-button v-if="selectedUnit" type="text" @click="dispatchUnit=selectedUnit">{{selectedUnit.companyType ? '公司同步' : '关联销售公司'}}</a-button>
            <a-button v-if="canWrite" type="primary" @click="openEditor('add')">
              <template #icon><PlusOutlined aria-hidden="true"/></template>登记任职
            </a-button>
          </a-space>
        </header>
        <div class="organization-filterbar">
          <a-input id="organization-name" v-model:value="name" placeholder="搜索姓名或人员 ID" aria-label="搜索姓名或人员 ID"
            allow-clear :maxlength="100" @press-enter="searchPeople">
            <template #prefix><SearchOutlined aria-hidden="true"/></template>
          </a-input>
          <AppointmentDateFilter v-model:mode="dateMode" v-model:date="on" compact/>
          <a-checkbox v-if="hasChildren" v-model:checked="includeChildren">包含下级组织</a-checkbox>
          <a-button v-if="filtersActive" type="link" size="small" @click="clearFilters">清除筛选</a-button>
        </div>
        <OrganizationAssignmentList :rows="entries" :loading="loading" :page="page" :can-write="canWrite"
          :empty-message="emptyMessage" :option-label="optionLabel" :person-link="personLink" :format-time="formatTime"
          @page="changePage" @edit="openEditor"/>
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
.organization-browser {display:grid;grid-template-columns:clamp(260px,25%,320px) minmax(0,1fr);align-items:start;gap:12px}
.organization-directory {min-width:0}
.organization-scope {border:1px solid var(--line)}
.organization-tree-actions {display:flex;align-items:center;justify-content:space-between;gap:6px;margin-bottom:12px}
.organization-tree-actions > span {color:var(--ink);font-weight:650;white-space:nowrap}
.organization-tree-actions .ant-btn {color:var(--accent);padding-inline:4px}
.organization-tree-actions .organization-positions-link {color:var(--muted)}
.organization-directory-header {display:flex;justify-content:space-between;align-items:center;gap:10px 14px;flex-wrap:wrap;padding:14px 16px 9px}
.organization-directory-header h2 {font-size:18px;margin:0;overflow-wrap:anywhere}
.organization-inactive {margin-inline-end:0}
.organization-count {border-radius:12px;padding:2px 8px;color:var(--muted);background:var(--surface,#f5f7fb);font-size:12px;white-space:nowrap}
.organization-filterbar {display:flex;gap:8px 12px;align-items:center;flex-wrap:wrap;padding:5px 16px 14px}
.organization-filterbar > .ant-input-affix-wrapper {width:min(260px,100%);flex:0 1 260px}
.organization-filterbar :deep(.ant-checkbox-wrapper) {min-height:34px;align-items:center}
.organization-filterbar > .ant-btn-link {padding-inline:0}
@media(width>820px){
 .organization-scope {position:sticky;top:12px;display:grid;grid-template-rows:auto minmax(0,1fr);height:max(340px,calc(100dvh - 202px))}
 .organization-scope :deep(.desktop-scope) {display:flex;flex-direction:column;margin-bottom:0;padding-bottom:0;border-bottom:0}
 .organization-scope :deep(.scope-tree-viewport) {flex:1;min-height:0;contain:size}
 .organization-scope :deep(.ant-tree-list-holder) {padding-inline-end:10px}
 .organization-scope :deep(.ant-tree-list-scrollbar-show) {display:block !important}
 .organization-scope :deep(.ant-tree-list-scrollbar-thumb) {background:color-mix(in srgb,var(--muted) 70%,var(--line)) !important}
 .organization-scope :deep(.ant-tree-list-scrollbar:hover .ant-tree-list-scrollbar-thumb),
 .organization-scope :deep(.ant-tree-list-scrollbar-thumb-moving) {background:var(--muted) !important}
}
@media(max-width:820px){
 .organization-browser {grid-template-columns:minmax(0,1fr);gap:8px}
 .organization-scope :deep(.desktop-scope) {display:none}
 .organization-scope :deep(.mobile-scope) {display:flex;align-items:center;gap:9px;margin-bottom:0}
 .organization-scope :deep(.mobile-scope .ant-select) {flex:1;min-width:0}
 .organization-directory-header {padding-inline:12px}
 .organization-filterbar {padding-inline:12px}
}
@media(max-width:680px){
 .organization-scope {padding:10px 12px}
 .organization-tree-actions {margin-bottom:8px}
 .organization-filterbar > .ant-input-affix-wrapper {width:100%;flex-basis:100%}
 .organization-directory-header :deep(.ant-btn) {min-height:38px}
}
</style>

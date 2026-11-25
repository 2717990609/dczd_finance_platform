<script setup>
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import OrganizationDispatchCreateUnit from './OrganizationDispatchCreateUnit.vue'
import OrganizationDispatchUnit from './OrganizationDispatchUnit.vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import CatalogAppointments from './CatalogAppointments.vue'
import OrganizationDirectors from './OrganizationDirectors.vue'
import OrganizationPeopleDesk from './OrganizationPeopleDesk.vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import OrganizationCompanies from './OrganizationCompanies.vue'
import OrganizationHealthConflicts from './OrganizationHealthConflicts.vue'
import OrganizationDuties from './OrganizationDuties.vue'
import OrganizationPositions from './OrganizationPositions.vue'
import WorkspaceOrganizationScope from './WorkspaceOrganizationScope.vue'
import { organizationTree } from './organization-tree'

const props = defineProps({api:Function, optionLabel:Function, formatTime:Function, canWrite:Boolean, appointmentKinds:Array,
  catalogRoles:Array,catalogScopes:Array,personLink:Function,panel:String,syncView:String})
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
const channels = ref()
const channelNames = {DCZD:'大春之道',DCJK:'大春见康',DCAI:'大春 AI'}
const channelError = ref('')
async function readChannels() {
  channelError.value=''
  try { channels.value=await props.api('/organization-dispatch/channels') }
  catch (e) { channels.value=undefined;channelError.value=e.message }
}
watch(()=>props.panel,panel=>{ if (panel==='sync') readChannels() },{immediate:true})
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

function refresh() {
  if (props.panel==='catalog') return catalog.value?.refresh()
  if (props.panel==='sync') {readChannels();return sync.value?.refresh()}
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
watch([unitId,on,dateMode,search],filterEntries)
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
      @show-director="company=>{directorCompany=company;emit('update:panel','directors')}" @show-appointments="showCompanyAppointments" @show-duties="showDuties"/>
  </KeepAlive>
  <KeepAlive>
    <OrganizationDuties v-if="panel==='duties'" ref="duties" :api="api" :format-time="formatTime" :can-write="canWrite"
      :units="units" :subject="dutySubject" :person-link="personLink" @clear-subject="dutySubject=undefined"/>
  </KeepAlive>
  <OrganizationDirectors v-if="panel==='directors'" ref="directorPanel" :api="api" :can-write="canWrite" :format-time="formatTime"
    :company="directorCompany" @clear-company="directorCompany=undefined"/>
  <template v-if="panel==='sync'">
    <section class="records sync-toolbar" aria-label="同步工作台">
      <h2>人员组织同步</h2>
      <nav aria-label="同步办理入口">
        <a href="#organization?tab=sync" :aria-current="syncView!=='records' ? 'page' : undefined">人员销售身份</a>
        <a href="#organization?tab=companies">销售公司同步</a>
        <a href="#organization?tab=directors">销售总监归属</a>
        <a href="#organization?tab=sync&view=records" :aria-current="syncView==='records' ? 'page' : undefined">下发记录</a>
        <a href="#organization?tab=health">AI 冲突处理</a>
      </nav>
      <div class="sync-channels" aria-label="各系统下发开关">
        <span>下发通道</span>
        <span v-for="(name,key) in channelNames" :key="key">{{ name }} · {{ channels ? channels[key] ? '已开启' : '未开启' : '待读取' }}</span>
        <a-button v-if="channelError" size="small" @click="readChannels">重新读取</a-button>
        <span v-if="channels && Object.keys(channelNames).every(key=>!channels[key])">当前环境的下发通道均未开启。</span>
      </div>
    </section>
    <OrganizationDispatch v-if="syncView==='records'" ref="sync" embedded :api="api" :can-write="canWrite" :format-time="formatTime"/>
    <OrganizationPeopleDesk v-else ref="sync" :api="api" :can-write="canWrite" :format-time="formatTime" :option-label="optionLabel" :person-link="personLink"/>
  </template>
  <OrganizationHealthConflicts v-if="panel==='health'" ref="health" :api="api" :can-write="canWrite"/>
  <div v-show="panel==='organization'">
    <a-alert v-if="unitsError || entriesError" class="feedback" type="error" :message="unitsError || entriesError" show-icon/>
    <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
    <div class="records organization-browser">
      <div class="scope-panel organization-scope">
        <WorkspaceOrganizationScope :nodes="tree" :selected="unitId" :loading="unitsLoading" fill-height @select="unitId=$event"/>
      </div>
      <section class="organization-directory">
        <div class="records-head">
          <div class="organization-heading">
            <h2>人员任职</h2>
            <span class="organization-context">{{ selectedUnit ? selectedUnit.name+' · 直接任职' : '全部组织' }}</span>
          </div>
          <a-space wrap>
            <a-button v-if="canWrite" @click="createUnit=true">登记内部组织</a-button>
            <a-button v-if="selectedUnit" @click="dispatchUnit=selectedUnit">销售公司同步</a-button>
            <a-button @click="positionsOpen=true">岗位目录</a-button>
            <a-button v-if="canWrite" type="primary" @click="openEditor('add')">登记任职</a-button>
          </a-space>
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
            <a v-if="column.key==='personName'" class="person-name-link" :href="personLink(record.personId)">{{ text }}</a>
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
  <OrganizationDispatchCreateUnit v-if="createUnit" :api="api" :units="units" :parent-id="selectedUnit?.id" @close="createUnit=false" @saved="savedUnit"/>
  <OrganizationDispatchUnit v-if="dispatchUnit" :key="dispatchUnit.id" :unit="dispatchUnit" :api="api" :can-write="canWrite" :format-time="formatTime" @close="dispatchUnit=undefined"/>
  <OrganizationPositions v-if="positionsOpen && panel==='organization'" :api="api" :can-write="canWrite" :scopes="catalogScopes"
    @close="positionsOpen=false" @changed="emit('position-changed')"/>
  <OrganizationAppointmentForm v-if="canWrite && editor" :api="api" :mode="editor.mode" :record="editor.record"
    :tree="appointmentTree" :kinds="appointmentKinds" :unit-id="selectedUnit?.status==='ACTIVE' ? unitId : undefined"
    @saved="saved" @close="editor=undefined"/>
</template>

<style scoped>
.sync-toolbar {display:flex;align-items:center;justify-content:space-between;gap:12px 24px;flex-wrap:wrap;padding:12px 16px;margin-bottom:12px}
.sync-channels {display:flex;align-items:center;gap:6px 18px;flex-wrap:wrap;flex-basis:100%;border-top:1px solid var(--line);padding-top:10px;color:var(--muted);font-size:13px}
.sync-toolbar h2 {margin:0;font-size:17px}
.sync-toolbar nav {display:flex;gap:4px;flex-wrap:wrap}
.sync-toolbar a {padding:7px 10px;border-radius:4px;color:var(--muted)}
.sync-toolbar a[aria-current] {color:var(--accent);background:var(--accent-soft);font-weight:600}
</style>

<script setup>
import { computed, onActivated, onBeforeUnmount, onDeactivated, ref, watch } from 'vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import { appointmentDateError, beijingTime } from './appointment-time'

const props = defineProps({api:Function, canWrite:Boolean, roles:Array, scopes:Array, formatTime:Function, personLink:Function, person:Object})
const base = '/catalog-appointments'
const settings = ref()
const entries = ref({total:0,items:[]})
const page = ref(1)
const loading = ref(false)
const listError = ref('')
const settingsError = ref('')
const nameInput = ref('')
const name = ref('')
const roleFilter = ref()
const scopeFilter = ref()
const scopeKeyFilter = ref()
const on = ref('')
const dateMode = ref(props.person ? 'history' : 'current')
const waitingForDate = computed(() => dateMode.value==='date' && !on.value)
const form = ref()
const saving = ref(false)
const saveError = ref('')
const notice = ref('')
let request = 0
let settingsRequest = 0

const titles = {add:'登记分润岗位',transfer:'交接分润岗位',end:'结束分润岗位'}
const submitLabels = {add:'保存岗位',transfer:'确认交接',end:'确认结束'}
const successMessages = {add:'分润岗位已登记。',transfer:'分润岗位已交接。',end:'分润岗位已结束。'}
const scopeOptions = (key, activeOnly = false) => (settings.value?.options[key] || [])
  .filter(option=>!activeOnly || option.active)
  .map(option=>option.active ? option : {...option,label:option.label+'（已停用）'})
const scopeLabel = key => props.scopes.find(scope=>scope.value===key)?.label || key
const roleLabel = key => props.roles.find(role=>role.value===key)?.label || key
const roleOptions = computed(() => props.roles.filter(role=>!role.disabled &&
  (role.scope?.scopes===undefined || (Array.isArray(role.scope.scopes) && role.scope.scopes.includes(form.value?.scope)))))
const canManage = computed(() => props.canWrite && settings.value?.ready)
const columns = computed(() => [
  ...(!props.person ? [{title:'人员',dataIndex:'personName',key:'personName',width:140}] : []),
  {title:'岗位',dataIndex:'role',key:'role',width:150},
  {title:'适用范围',dataIndex:'scopeName',key:'scope',width:195},
  {title:'状态',key:'status',width:82},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:155},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:155},
  ...(canManage.value ? [{title:'',key:'actions',width:104,fixed:'right'}] : []),
])
const tableWidth = computed(() => columns.value.reduce((width,column)=>width+column.width,0))
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false,hideOnSinglePage:true}))
function statusFor(record) {
  if (Date.parse(record.validFrom)>Date.now()) return {label:'未生效',color:'blue'}
  if (record.validTo && Date.parse(record.validTo)<=Date.now()) return {label:'已结束'}
  return {label:'有效',color:'green'}
}

async function loadSettings() {
  const sequence = ++settingsRequest
  settingsError.value = ''
  try {
    const result = await props.api(base+'/scopes')
    if (sequence===settingsRequest) settings.value = result
  } catch (err) {
    if (sequence===settingsRequest) settingsError.value = err.message
  }
}

async function load() {
  const sequence = ++request
  listError.value = ''
  if (waitingForDate.value) {
    entries.value = {total:0,items:[]}
    loading.value = false
    return
  }
  loading.value = true
  const params = new URLSearchParams({page:page.value})
  if (props.person) params.set('personId',props.person.id)
  if (name.value) params.set('name',name.value)
  if (roleFilter.value) params.set('role',roleFilter.value)
  if (scopeFilter.value) params.set('scope',scopeFilter.value)
  if (scopeKeyFilter.value) params.set('scopeKey',scopeKeyFilter.value)
  if (dateMode.value==='history') params.set('history','true')
  if (dateMode.value==='date') params.set('on',on.value)
  try {
    const result = await props.api(base+'?'+params)
    if (sequence===request) entries.value = result
  } catch (err) {
    if (sequence===request) {
      entries.value = {total:0,items:[]}
      listError.value = err.message
    }
  } finally {
    if (sequence===request) loading.value = false
  }
}

function refresh() { return Promise.all([(!props.person || props.canWrite) ? loadSettings() : undefined,load()]) }
function search() { name.value = nameInput.value.trim() }
function pageChanged(next) { page.value = next.current; load() }
function open(mode, record) {
  saveError.value = ''
  notice.value = ''
  form.value = {mode,record,personId:mode==='add'?props.person?.id:undefined,scope:mode==='add'?scopeFilter.value:undefined,
    scopeKey:mode==='add' && scopeOptions(scopeFilter.value,true).some(option=>option.value===scopeKeyFilter.value) ? scopeKeyFilter.value : undefined,
    role:mode==='add'?roleFilter.value:undefined}
  if (!roleOptions.value.some(role=>role.value===form.value.role)) form.value.role = undefined
}
function selectScope() {
  form.value.scopeKey = undefined
  if (!roleOptions.value.some(role=>role.value===form.value.role)) form.value.role = undefined
}

const dateError = computed(() => form.value ? appointmentDateError(form.value) : '')
const samePerson = computed(() => form.value?.mode==='transfer' && form.value.personId===form.value.record.personId)
const ready = computed(() => {
  if (!form.value?.effective || dateError.value || samePerson.value) return false
  const {mode,personId,role,scope,scopeKey} = form.value
  return mode==='end' || !!personId && (mode==='transfer' || !!role && !!scope && !!scopeKey)
})
async function save() {
  if (saving.value || !ready.value) return
  const {mode,record,personId,role,scope,scopeKey,effective,through} = form.value
  saving.value = true
  saveError.value = ''
  const at = beijingTime(effective)
  const payload = mode==='add'
    ? {personId,role,scope,scopeKey,startsAt:at,throughAt:beijingTime(through)}
    : {assignmentId:record.id,at,expectedThroughAt:record.validTo,...(mode==='transfer' ? {personId} : {})}
  try {
    await props.api(base+(mode==='add' ? '' : '/'+mode),{method:'POST',body:JSON.stringify(payload)})
    form.value = undefined
    notice.value = successMessages[mode]
    await load()
  } catch (err) {
    saveError.value = err.message
  } finally {
    saving.value = false
  }
}

watch(scopeFilter,()=>{ scopeKeyFilter.value = undefined })
watch([name,roleFilter,scopeFilter,scopeKeyFilter,on,dateMode],()=>{ page.value = 1; load() })
onActivated(refresh)
onDeactivated(()=>{form.value=undefined})
onBeforeUnmount(()=>{ request++; settingsRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-alert v-if="settingsError || listError" class="feedback" type="error" :message="settingsError || listError" show-icon>
    <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
  <section class="records organization-directory" aria-label="分润岗位">
    <div class="records-head catalog-appointment-head">
      <div class="organization-heading">
        <h2>{{ person ? '分润岗位' : '大春 AI 分润岗位' }}</h2>
        <span v-if="person && !loading && !listError" class="organization-context">{{entries.total}} 条记录</span>
        <span v-if="!person" class="organization-context">按订单付款时的岗位和生效时间确定收款人</span>
      </div>
      <a-button v-if="canWrite" :disabled="!canManage" type="primary" @click="open('add')">登记分润岗位</a-button>
    </div>
    <a-alert v-if="settings && !settings.ready" class="feedback" message="请先初始化大春 AI 核算资料。" type="info" show-icon/>
    <div v-if="!person" class="organization-filters">
      <div>
        <label for="catalog-appointment-name">人员</label>
        <a-input-search id="catalog-appointment-name" v-model:value="nameInput" placeholder="输入姓名" allow-clear
          @search="search" @change="!nameInput && search()"/>
      </div>
      <div>
        <label for="catalog-appointment-role-filter">岗位</label>
        <a-select id="catalog-appointment-role-filter" v-model:value="roleFilter" :options="roles.map(role=>({...role,disabled:false}))" placeholder="全部角色" allow-clear/>
      </div>
      <div>
        <label for="catalog-appointment-scope-filter">适用范围</label>
        <a-select id="catalog-appointment-scope-filter" v-model:value="scopeFilter" :options="scopes" placeholder="全部范围" allow-clear/>
      </div>
      <div v-if="scopeFilter">
        <label for="catalog-appointment-key-filter">适用对象</label>
        <a-select id="catalog-appointment-key-filter" v-model:value="scopeKeyFilter" :options="scopeOptions(scopeFilter)"
          option-filter-prop="label" show-search allow-clear :placeholder="'全部'+scopeLabel(scopeFilter)"/>
      </div>
      <AppointmentDateFilter v-model:mode="dateMode" v-model:date="on" class="catalog-appointment-date-filter"/>
    </div>
    <a-table size="middle" :columns="columns" :data-source="entries.items" :loading="loading" row-key="id" :pagination="pagination"
      :scroll="{x:tableWidth}" @change="pageChanged">
      <template #bodyCell="{column,record,text}">
        <a v-if="column.key==='personName'" :href="personLink(record.personId,'positions')" class="person-name-link">{{ text }}</a>
        <template v-if="column.key==='role'">{{ roleLabel(text) }}</template>
        <template v-if="column.key==='scope'">{{ text }}<small class="order-version">{{ scopeLabel(record.scope) }}</small></template>
        <a-tag v-if="column.key==='status'" :color="statusFor(record).color">{{statusFor(record).label}}</a-tag>
        <span v-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
        <a-space v-if="column.key==='actions' && (!person || statusFor(record).label!=='已结束')" :size="1" class="catalog-appointment-actions">
          <a-button type="link" size="small" @click="open('transfer',record)">交接</a-button>
          <a-button type="text" size="small" class="catalog-appointment-end" @click="open('end',record)">结束</a-button>
        </a-space>
      </template>
      <template #emptyText>{{ waitingForDate ? '请选择任职日期' : person ? '暂无分润岗位记录' : '当前条件下没有分润岗位记录' }}</template>
    </a-table>
  </section>
  <a-drawer v-if="form" :title="titles[form.mode]" :open="true" width="min(440px, 100vw)" :closable="!saving"
    :keyboard="!saving" :mask-closable="!saving" @close="form=undefined">
    <a-form layout="vertical" @submit.prevent="save">
      <a-alert v-if="saveError" class="feedback" type="error" :message="saveError" show-icon/>
      <div v-if="form.record" class="catalog-appointment-subject">
        <strong>{{form.record.personName}}</strong>
        <span>{{roleLabel(form.record.role)}} · {{form.record.scopeName}}</span>
      </div>
      <div v-else-if="person" class="catalog-appointment-subject"><span>人员</span><strong>{{person.name}}</strong></div>
      <template v-if="form.mode==='add'">
        <a-form-item label="适用范围" html-for="catalog-appointment-scope" required>
          <a-select id="catalog-appointment-scope" v-model:value="form.scope" :options="scopes" placeholder="选择范围" @change="selectScope"/>
        </a-form-item>
        <a-form-item label="适用对象" html-for="catalog-appointment-key" required>
          <a-select id="catalog-appointment-key" v-model:value="form.scopeKey" :options="scopeOptions(form.scope,true)"
            :disabled="!form.scope" option-filter-prop="label" show-search
            :placeholder="form.scope ? '选择'+scopeLabel(form.scope) : '请先选择适用范围'"/>
        </a-form-item>
        <a-form-item label="岗位" html-for="catalog-appointment-role" required>
          <a-select id="catalog-appointment-role" v-model:value="form.role" :options="roleOptions"
            :disabled="!form.scope" option-filter-prop="label" show-search placeholder="选择岗位"/>
        </a-form-item>
      </template>
      <a-form-item v-if="form.mode==='transfer' || (form.mode==='add' && !person)"
        :label="form.mode==='transfer' ? '接任人员' : '人员'" html-for="catalog-appointment-person" required
        :help="samePerson ? '请选择其他接任人员。' : undefined" :validate-status="samePerson ? 'error' : undefined">
        <AppointmentPersonSelect id="catalog-appointment-person" v-model:value="form.personId" :api="api"/>
      </a-form-item>
      <a-form-item :label="form.mode==='end'?'结束时间（北京时间）':'生效时间（北京时间）'" html-for="catalog-appointment-effective" required
        :help="form.mode!=='add' && dateError || undefined" :validate-status="form.mode!=='add' && dateError ? 'error' : undefined">
        <a-input id="catalog-appointment-effective" v-model:value="form.effective" type="datetime-local" required/>
      </a-form-item>
      <a-form-item v-if="form.mode==='add'" label="计划结束时间（北京时间）" html-for="catalog-appointment-through" extra="长期任职可留空。"
        :help="dateError || undefined" :validate-status="dateError ? 'error' : undefined">
        <a-input id="catalog-appointment-through" v-model:value="form.through" type="datetime-local"/>
      </a-form-item>
      <a-alert v-if="form.record" class="feedback" type="info" show-icon
        message="规则如指定了具体人员，任职变动不会变更收款人，相关分项可能暂停计提。请核对规则和试算结果。"/>
      <div class="form-actions">
        <a-button :disabled="saving" @click="form=undefined">取消</a-button>
        <a-button type="primary" :danger="form.mode==='end'" html-type="submit" :loading="saving" :disabled="!ready">{{submitLabels[form.mode]}}</a-button>
      </div>
    </a-form>
  </a-drawer>
</template>

<style scoped>
.catalog-appointment-head {flex-wrap:wrap}
.catalog-appointment-actions :deep(.ant-btn) {padding-inline:5px}
.catalog-appointment-end {color:var(--muted)}
.catalog-appointment-end:hover,.catalog-appointment-end:focus-visible {color:#b42318}
.catalog-appointment-subject {display:grid;gap:3px;margin-bottom:16px;padding:10px 12px;border-radius:6px;background:var(--accent-soft)}
.catalog-appointment-subject span {color:var(--muted)}
</style>

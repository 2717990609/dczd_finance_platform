<script setup>
import { computed, onActivated, onBeforeUnmount, ref, watch } from 'vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({api:Function, canWrite:Boolean, roles:Array, scopes:Array, formatTime:Function, personLink:Function})
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
const dateMode = ref('current')
const waitingForDate = computed(() => dateMode.value==='date' && !on.value)
const form = ref()
const saving = ref(false)
const saveError = ref('')
const notice = ref('')
let request = 0
let settingsRequest = 0

const titles = {add:'登记分润岗位',transfer:'交接分润岗位',end:'结束分润岗位'}
const scopeOptions = (key, activeOnly = false) => (settings.value?.options[key] || [])
  .filter(option=>!activeOnly || option.active)
  .map(option=>option.active ? option : {...option,label:option.label+'（已停用）'})
const scopeLabel = key => props.scopes.find(scope=>scope.value===key)?.label || key
const roleLabel = key => props.roles.find(role=>role.value===key)?.label || key
const roleOptions = computed(() => props.roles.filter(role=>!role.disabled &&
  (role.scope?.scopes===undefined || (Array.isArray(role.scope.scopes) && role.scope.scopes.includes(form.value?.scope)))))
const columns = computed(() => [
  {title:'人员',dataIndex:'personName',key:'personName',width:140},
  {title:'计提角色',dataIndex:'role',key:'role',width:150},
  {title:'适用范围',dataIndex:'scopeName',key:'scope',width:195},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:155},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:155},
  ...(props.canWrite ? [{title:'操作',key:'actions',width:128}] : []),
])
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false,hideOnSinglePage:true}))

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

function refresh() { return Promise.all([loadSettings(),load()]) }
function search() { name.value = nameInput.value.trim() }
function pageChanged(next) { page.value = next.current; load() }
function open(mode, record) {
  saveError.value = ''
  notice.value = ''
  form.value = {mode,record,scope:mode==='add'?scopeFilter.value:undefined,
    scopeKey:mode==='add' && scopeOptions(scopeFilter.value,true).some(option=>option.value===scopeKeyFilter.value) ? scopeKeyFilter.value : undefined,
    role:mode==='add'?roleFilter.value:undefined}
  if (!roleOptions.value.some(role=>role.value===form.value.role)) form.value.role = undefined
}
function selectScope() {
  form.value.scopeKey = undefined
  if (!roleOptions.value.some(role=>role.value===form.value.role)) form.value.role = undefined
}

const beijing = value => value ? (value.length===16 ? value+':00' : value)+'+08:00' : null
async function save() {
  if (saving.value || !form.value) return
  const {mode,record,personId,role,scope,scopeKey,effective,through} = form.value
  if (!effective || (mode!=='end' && !personId) || (mode==='add' && (!role || !scope || !scopeKey))) {
    saveError.value = '请填写必填项。'
    return
  }
  saving.value = true
  saveError.value = ''
  const at = beijing(effective)
  const payload = mode==='add'
    ? {personId,role,scope,scopeKey,startsAt:at,throughAt:beijing(through)}
    : {assignmentId:record.id,at,expectedThroughAt:record.validTo,...(mode==='transfer' ? {personId} : {})}
  try {
    await props.api(base+(mode==='add' ? '' : '/'+mode),{method:'POST',body:JSON.stringify(payload)})
    form.value = undefined
    notice.value = '计提任职已保存。'
    load()
  } catch (err) {
    saveError.value = err.message
  } finally {
    saving.value = false
  }
}

watch(scopeFilter,()=>{ scopeKeyFilter.value = undefined })
watch([name,roleFilter,scopeFilter,scopeKeyFilter,on,dateMode],()=>{ page.value = 1; load() })
onActivated(refresh)
onBeforeUnmount(()=>{ request++; settingsRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-alert v-if="settingsError || listError" class="feedback" type="error" :message="settingsError || listError" show-icon/>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
  <section class="records organization-directory">
    <div class="records-head">
      <div class="organization-heading">
        <h2>大春 AI 分润岗位</h2>
        <span class="organization-context">按订单付款时的岗位和生效时间确定收款人</span>
      </div>
      <a-button v-if="canWrite && settings?.ready" type="primary" @click="open('add')">登记分润岗位</a-button>
    </div>
    <a-alert v-if="settings && !settings.ready" class="feedback" message="请先初始化大春 AI 核算资料。" type="info" show-icon/>
    <template v-else>
      <div class="organization-filters">
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
        :scroll="{x:canWrite?923:795}" @change="pageChanged">
        <template #bodyCell="{column,record,text}">
          <a v-if="column.key==='personName'" :href="personLink(record.personId,'positions')" class="person-name-link">{{ text }}</a>
          <template v-if="column.key==='role'">{{ roleLabel(text) }}</template>
          <template v-if="column.key==='scope'">{{ text }}<small class="order-version">{{ scopeLabel(record.scope) }}</small></template>
          <span v-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
          <a-space v-if="column.key==='actions'" :size="1">
            <a-button type="link" size="small" @click="open('transfer',record)">交接</a-button>
            <a-button type="link" size="small" @click="open('end',record)">结束</a-button>
          </a-space>
        </template>
        <template #emptyText>{{ waitingForDate ? '请选择任职日期' : '当前条件下没有分润岗位记录' }}</template>
      </a-table>
    </template>
  </section>
  <a-drawer v-if="form" :title="titles[form.mode]" :open="true" width="min(440px, 100vw)" :closable="!saving"
    :keyboard="!saving" :mask-closable="!saving" @close="form=undefined">
    <a-form layout="vertical" @submit.prevent="save">
      <a-alert v-if="saveError" class="feedback" type="error" :message="saveError" show-icon/>
      <p v-if="form.record">{{ form.record.personName }} · {{ roleLabel(form.record.role) }} · {{ form.record.scopeName }}</p>
      <template v-if="form.mode==='add'">
        <a-form-item label="适用范围" html-for="catalog-appointment-scope" required>
          <a-select id="catalog-appointment-scope" v-model:value="form.scope" :options="scopes" @change="selectScope"/>
        </a-form-item>
        <a-form-item label="适用对象" html-for="catalog-appointment-key" required>
          <a-select id="catalog-appointment-key" v-model:value="form.scopeKey" :options="scopeOptions(form.scope,true)"
            :disabled="!form.scope" option-filter-prop="label" show-search/>
        </a-form-item>
        <a-form-item label="岗位" html-for="catalog-appointment-role" required>
          <a-select id="catalog-appointment-role" v-model:value="form.role" :options="roleOptions"
            :disabled="!form.scope" option-filter-prop="label" show-search/>
        </a-form-item>
      </template>
      <a-form-item v-if="form.mode!=='end'" :label="form.mode==='transfer' ? '接任人员' : '人员'" html-for="catalog-appointment-person" required>
        <AppointmentPersonSelect id="catalog-appointment-person" v-model:value="form.personId" :api="api"/>
      </a-form-item>
      <a-form-item :label="form.mode==='end'?'结束时间（北京时间）':'生效时间（北京时间）'" html-for="catalog-appointment-effective" required>
        <a-input id="catalog-appointment-effective" v-model:value="form.effective" type="datetime-local" required/>
      </a-form-item>
      <a-form-item v-if="form.mode==='add'" label="计划结束时间（北京时间）" html-for="catalog-appointment-through">
        <a-input id="catalog-appointment-through" v-model:value="form.through" type="datetime-local"/>
      </a-form-item>
      <a-alert v-if="form.record" class="feedback" type="info" show-icon
        message="规则如指定了具体人员，任职变动不会变更收款人，相关分项可能暂停计提。请核对规则和试算结果。"/>
      <a-space>
        <a-button :disabled="saving" @click="form=undefined">取消</a-button>
        <a-button type="primary" html-type="submit" :loading="saving">保存</a-button>
      </a-space>
    </a-form>
  </a-drawer>
</template>

<script setup>
import { computed, onActivated, onBeforeUnmount, ref, useId, watch } from 'vue'
import AppointmentDateFilter from './AppointmentDateFilter.vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({api:Function, formatTime:Function, canWrite:Boolean, units:Array, subject:Object, personLink:Function, profile:Boolean})
const emit = defineEmits(['clear-subject'])
const base = '/organization-business-duties'
const roleFilterID = useId()
const scopeFilterID = useId()
const settings = ref({roles:[],scopes:[]})
const entries = ref({items:[],total:0})
const loading = ref(false)
const profileUnits = ref([])
const listError = ref('')
const settingsError = ref('')
const saving = ref(false)
const writeError = ref('')
const notice = ref('')
const form = ref()
const roleFilter = ref()
const scopeFilter = ref()
const nameInput = ref('')
const name = ref('')
const on = ref('')
const dateMode = ref(props.profile ? 'history' : 'current')
const page = ref(1)
let listRequest = 0
let settingsRequest = 0

const kinds = {PROJECT:'项目',PRODUCT:'产品',ORGANIZATION_UNIT:'组织'}
const subjects = [{label:'人员',value:'PERSON'},{label:'组织',value:'ORGANIZATION'}]
const titles = {add:'登记业务职责',transfer:'交接业务职责',end:'结束业务职责'}
const scopeOptions = computed(() => settings.value.scopes.map(scope=>({...scope,label:scope.label+' · '+kinds[scope.kind]+
  (scope.active ? '' : '（已停用）')})))
const scopeLabel = id => scopeOptions.value.find(scope=>scope.value===id)?.label
const roleLabel = role => settings.value.roles.find(option=>option.value===role)?.label || role
const selectedRole = computed(() => settings.value.roles.find(role=>role.value===(form.value?.record?.role || form.value?.role)))
const allowedSubjects = computed(() => subjects.filter(subject=>selectedRole.value?.subjects?.includes(subject.value)))
const allowedScopes = computed(() => scopeOptions.value.filter(scope=>scope.active && selectedRole.value?.scopes?.includes(scope.kind)))
const unitOptions = computed(() => (props.units || profileUnits.value).filter(unit=>unit.status==='ACTIVE').map(unit=>({value:unit.id,label:unit.name})))
const person = computed(() => props.subject?.personId ? {id:props.subject.personId,name:props.subject.name} : undefined)
const fields = [
  {title:'任职主体',key:'subject',width:160},
  {title:'业务职责',key:'role',width:160},
  {title:'适用范围',key:'scope',width:180},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:155},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:155},
]
const columns = computed(() => [...fields,...(props.canWrite ? [{title:'',key:'actions',width:102}] : [])])
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false,hideOnSinglePage:true}))

async function loadSettings() {
  const seq = ++settingsRequest
  settingsError.value = ''
  try {
    const [result,units] = await Promise.all([
      props.api('/organization-duty-scopes'),
      props.profile && props.canWrite && !profileUnits.value.length ? props.api('/organization-units') : undefined,
    ])
    if (seq===settingsRequest) {
      settings.value = result
      if (units) profileUnits.value = units
    }
  } catch (err) {
    if (seq===settingsRequest) settingsError.value = err.message
  }
}

async function load() {
  const seq = ++listRequest
  listError.value = ''
  if (dateMode.value==='date' && !on.value) {
    entries.value = {items:[],total:0}
    loading.value = false
    return
  }
  const query = new URLSearchParams({page:page.value,name:name.value})
  if (roleFilter.value) query.set('role',roleFilter.value)
  if (scopeFilter.value) query.set('scopeId',scopeFilter.value)
  if (props.subject?.personId) query.set('personId',props.subject.personId)
  if (props.subject?.unitId) query.set('unitId',props.subject.unitId)
  if (dateMode.value==='history') query.set('history','true')
  if (dateMode.value==='date') query.set('on',on.value)
  loading.value = true
  try {
    const rows = await props.api(base+'?'+query)
    if (seq===listRequest) entries.value = rows
  } catch (err) {
    if (seq===listRequest) {
      listError.value = err.message
      entries.value = {items:[],total:0}
    }
  } finally {
    if (seq===listRequest) loading.value = false
  }
}
function refresh() { return Promise.all([loadSettings(),load()]) }
function changePage(next) { page.value = next.current; load() }
function open(mode, record) {
  writeError.value = ''
  notice.value = ''
  const preferred = props.subject?.unitId || record?.unitId ? 'ORGANIZATION' : 'PERSON'
  form.value = {mode,record,role:mode==='add'?roleFilter.value:undefined,subjectKind:preferred,
    personId:mode==='add'?props.subject?.personId:undefined,unitId:mode==='add'?props.subject?.unitId:undefined}
  if (mode==='add' && allowedScopes.value.some(scope=>scope.value===scopeFilter.value)) form.value.scopeId = scopeFilter.value
}
function selectRole() {
  if (!allowedScopes.value.some(scope=>scope.value===form.value.scopeId)) form.value.scopeId = undefined
  if (!allowedSubjects.value.some(subject=>subject.value===form.value.subjectKind)) form.value.subjectKind = allowedSubjects.value[0]?.value
}
const beijing = value => value ? (value.length===16 ? value+':00' : value)+'+08:00' : null
async function save() {
  if (!form.value || saving.value) return
  const {mode,record,personId,unitId,subjectKind,role,scopeId,effective,through} = form.value
  const owner = subjectKind==='PERSON' ? {personId} : {unitId}
  if (!effective || (mode!=='end' && !Object.values(owner)[0]) || (mode==='add' && (!role || !scopeId))) {
    writeError.value = '请填写必填项。'
    return
  }
  saving.value = true
  writeError.value = ''
  const at = beijing(effective)
  const payload = mode==='add' ? {...owner,role,scopeId,startsAt:at,throughAt:beijing(through)}
    : {assignmentId:record.id,at,expectedThroughAt:record.validTo,...(mode==='transfer' ? owner : {})}
  try {
    await props.api(base+(mode==='add' ? '' : '/'+mode),{method:'POST',body:JSON.stringify(payload)})
    form.value = undefined
    notice.value = '业务职责已保存。'
    await load()
  } catch (err) {
    writeError.value = err.message
  } finally {
    saving.value = false
  }
}

watch(() => props.subject, subject => {
  if (!subject) return
  roleFilter.value = undefined
  scopeFilter.value = undefined
  nameInput.value = ''
  name.value = ''
})
watch([roleFilter,scopeFilter,name,on,dateMode,()=>props.subject], () => { page.value=1; load() })
onActivated(refresh)
onBeforeUnmount(() => { listRequest++; settingsRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-alert v-if="listError || settingsError" class="feedback" type="error" :message="listError || settingsError" show-icon>
    <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
  <section class="records">
    <div class="records-head">
      <h2>业务职责</h2>
      <a v-if="profile" class="context-link" href="#organization?tab=duties">全部业务职责</a>
      <a-button v-if="canWrite" type="primary" :disabled="!settings.scopes.some(scope=>scope.active)" @click="open('add')">登记业务职责</a-button>
    </div>
    <div class="organization-filters duty-filters">
      <div v-if="subject && !profile" class="topbar-actions">
        <a-tag closable @close.prevent="emit('clear-subject')">{{ subject.name }}</a-tag>
      </div>
      <div v-else-if="!profile">
        <label for="duty-name">人员或组织</label>
        <a-input-search id="duty-name" v-model:value="nameInput" placeholder="搜索名称" allow-clear
          @search="name=$event.trim()" @change="!nameInput && (name='')"/>
      </div>
      <div>
        <label :for="roleFilterID">职责</label>
        <a-select :id="roleFilterID" v-model:value="roleFilter" :options="settings.roles" allow-clear placeholder="全部职责"/>
      </div>
      <div>
        <label :for="scopeFilterID">适用范围</label>
        <a-select :id="scopeFilterID" v-model:value="scopeFilter" :options="scopeOptions" allow-clear show-search option-filter-prop="label" placeholder="全部范围"/>
      </div>
      <AppointmentDateFilter v-model:mode="dateMode" v-model:date="on"/>
    </div>
    <div class="table-container">
      <a-table :columns="columns" :data-source="entries.items" :pagination="pagination" :loading="loading" row-key="id"
        :scroll="{x:canWrite?912:810}" @change="changePage">
        <template #bodyCell="{column,record,text}">
          <template v-if="column.key==='subject'">
            <a v-if="record.personId && !profile" :href="personLink(record.personId,'duties')" class="person-name-link">{{ record.subjectName }}</a>
            <span v-else>{{ record.subjectName }}</span><small class="order-version">{{ record.personId ? '人员' : '组织' }}</small>
          </template>
          <span v-else-if="column.key==='role'">{{ roleLabel(record.role) }}</span>
          <span v-else-if="column.key==='scope'">{{ record.scopeName }}<small class="order-version">{{ kinds[record.scopeKind] }}</small></span>
          <span v-else-if="column.key==='validFrom'||column.key==='validTo'">{{ text ? formatTime(text) : '—' }}</span>
          <a-space v-else-if="column.key==='actions'" :size="0">
            <a-button type="link" size="small" @click="open('transfer',record)">交接</a-button>
            <a-button type="link" size="small" @click="open('end',record)">结束</a-button>
          </a-space>
        </template>
        <template #emptyText>{{ dateMode==='date' && !on ? '请选择查看日期' : '暂无符合条件的业务职责' }}</template>
      </a-table>
    </div>
  </section>

  <a-drawer v-if="form" :open="true" :title="titles[form.mode]" width="min(520px, 100vw)"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="form=undefined">
    <a-alert v-if="writeError" class="feedback" type="error" :message="writeError" show-icon/>
    <a-form layout="vertical" @finish="save">
      <template v-if="form.mode==='add'">
        <a-form-item label="业务职责" html-for="duty-role" required>
          <a-select id="duty-role" v-model:value="form.role" :options="settings.roles.filter(role=>role.active)" @change="selectRole"/>
        </a-form-item>
        <a-form-item label="适用范围" html-for="duty-scope" required>
          <a-select id="duty-scope" v-model:value="form.scopeId" :options="allowedScopes" show-search option-filter-prop="label"/>
        </a-form-item>
      </template>
      <p v-else>{{ form.record.subjectName }} · {{ roleLabel(form.record.role) }} · {{ scopeLabel(form.record.scopeId) }}</p>
      <template v-if="form.mode!=='end'">
        <a-form-item label="主体类型" html-for="duty-subject-kind" required>
          <a-select id="duty-subject-kind" v-model:value="form.subjectKind" :options="allowedSubjects"/>
        </a-form-item>
        <a-form-item v-if="form.subjectKind==='PERSON'" label="人员" html-for="duty-person" required>
          <AppointmentPersonSelect id="duty-person" v-model:value="form.personId" :api="api" :current="person"/>
        </a-form-item>
        <a-form-item v-else label="组织" html-for="duty-unit-select" required>
          <a-select id="duty-unit-select" v-model:value="form.unitId" :options="unitOptions" show-search option-filter-prop="label"/>
        </a-form-item>
      </template>
      <a-form-item :label="form.mode==='end' ? '结束时间' : '生效时间'" html-for="duty-effective" required>
        <a-input id="duty-effective" type="datetime-local" v-model:value="form.effective"/>
      </a-form-item>
      <a-form-item v-if="form.mode==='add'" label="结束时间" html-for="duty-through">
        <a-input id="duty-through" type="datetime-local" v-model:value="form.through"/>
      </a-form-item>
    </a-form>
    <template #footer>
      <a-space>
        <a-button :disabled="saving" @click="form=undefined">取消</a-button>
        <a-button :loading="saving" type="primary" @click="save">保存</a-button>
      </a-space>
    </template>
  </a-drawer>
</template>

<style scoped>
.duty-filters { padding-inline: 14px; }
.duty-filters .ant-select { min-width: 150px; }
</style>

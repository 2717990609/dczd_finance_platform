<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { LeftOutlined, UserOutlined } from '@ant-design/icons-vue'
import OrganizationPersonAccounts from './OrganizationPersonAccounts.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import OrganizationPersonAppointments from './OrganizationPersonAppointments.vue'
import { organizationTree } from './organization-tree'
import MonthlyReport from './MonthlyReport.vue'
import OrganizationDuties from './OrganizationDuties.vue'
import CatalogAppointments from './CatalogAppointments.vue'

const props = defineProps({
  api:Function, personId:Number, section:String, back:String, personLink:Function,
  optionLabel:Function, formatTime:Function, formatIdentityTime:Function, formatMoney:Function,
  month:String, reportViews:Array, reportDimensions:Array, reportFilters:Object, periodLink:Function, canWrite:Boolean, appointmentKinds:Array,
  catalogRoles:Array, catalogScopes:Array,
})

const emit = defineEmits(['report-filters'])
const sections = [
  {key:'overview', label:'档案'},
  {key:'duties', label:'业务职责'},
  {key:'positions', label:'分润岗位'},
  {key:'finance', label:'业绩与分润'},
]
const selected = computed(() => sections.find(item=>item.key===props.section) || sections[0])
const person = ref()
const canEdit=computed(()=>props.canWrite && person.value?.sourceStatus!=='INACTIVE')
const personAppointments=ref(),accountArea=ref()
const appointment=ref(),appointmentUnits=ref(),appointmentLoading=ref(false),actionError=ref(''),notice=ref('')
const appointmentTree=computed(()=>organizationTree(appointmentUnits.value || [],kind=>props.optionLabel('organizationKind',kind) || kind,true))
async function editAppointment(mode,record){
 if(appointmentLoading.value)return
 const section=selected.value.key
 actionError.value=''
 notice.value=''
 if(mode!=='end' && !appointmentUnits.value){
  appointmentLoading.value=true
  try{appointmentUnits.value=await props.api('/organization-units')}
  catch(e){actionError.value=e.message;return}
  finally{appointmentLoading.value=false}
 }
 if(selected.value.key===section)appointment.value={mode,record}
}
function appointmentSaved(message){
 appointment.value=undefined
 notice.value=message
 personAppointments.value?.refresh()
}
const heading = ref()
const sectionNav = ref()
const profileError = ref('')
const profileLoading = ref(false)
const report = ref()
const duties = ref()
const positions = ref()
const dutySubject = computed(() => person.value ? {personId:props.personId,name:person.value.name} : undefined)
const reportMonth = ref(props.reportFilters.month || props.month)
const reportView = ref(props.reportFilters.view)
const reportDimension = ref('person')
const personDimensions = computed(() => props.reportDimensions.filter(item=>item.value==='person'))
const rename=ref(),renameBusy=ref(false),renameError=ref('')
async function saveName(){
 renameBusy.value=true;renameError.value=''
 try{await props.api('/organization-dispatch/rename-person',{method:'POST',body:JSON.stringify({personId:props.personId,name:rename.value,expectedName:person.value.name})});rename.value=undefined;notice.value='姓名已更新。';await loadPerson()}
 catch(e){renameError.value=e.message}finally{renameBusy.value=false}
}
let profileRequest = 0
const accountWorkspace=ref()

async function loadPerson(focus = false) {
  const request = ++profileRequest
  profileError.value = ''
  profileLoading.value = true
  try {
    const result = await props.api('/person-detail?'+new URLSearchParams({personId:props.personId}))
    if (request!==profileRequest) return
    person.value = result
    if (focus) {
      await nextTick()
      if(props.section==='accounts'){
        accountArea.value?.focus({preventScroll:true})
        accountArea.value?.scrollIntoView({block:'nearest'})
      }else{
        heading.value?.focus({preventScroll:true})
      }
    }
  } catch (error) {
    if (request===profileRequest) {
      person.value = undefined
      profileError.value = error.message
    }
  } finally {
    if (request===profileRequest) profileLoading.value = false
  }
}
function refresh() {
  loadPerson()
  if (selected.value.key==='overview') {
    personAppointments.value?.refresh()
    accountWorkspace.value?.refresh()
  }
  else if (selected.value.key==='finance') report.value?.refresh()
  else if (selected.value.key==='duties') duties.value?.refresh()
  else positions.value?.refresh()
}
watch(() => selected.value.key, (key, previous) => {
  actionError.value=''
  notice.value=''
  if(previous){
    appointment.value=undefined
    nextTick(()=>sectionNav.value?.scrollIntoView({block:'nearest'}))
  }
})
watch(()=>props.section,section=>{
  if(section==='accounts')nextTick(()=>accountArea.value?.scrollIntoView({block:'nearest'}))
})
watch([reportMonth,reportView], ([month,view]) => {
  if (selected.value.key==='finance') emit('report-filters',{month,view})
})
loadPerson(true)
onBeforeUnmount(() => { profileRequest++ })
defineExpose({refresh})
</script>

<template>
  <article class="person-detail" aria-label="人员详情">
    <div class="person-detail-header">
      <a class="workspace-return" :href="back"><LeftOutlined aria-hidden="true"/>返回列表</a>
      <a-alert v-if="profileError" class="feedback" type="error" :message="profileError" show-icon>
        <template #action><a-button size="small" @click="loadPerson(true)">重试</a-button></template>
      </a-alert>
      <a-spin v-else-if="profileLoading && !person" class="person-profile-loading" tip="正在读取人员档案"/>
      <div v-else-if="person" class="person-detail-identity">
        <div class="person-detail-avatar" aria-hidden="true"><UserOutlined/></div>
        <div class="person-detail-heading">
          <div class="person-name-row">
            <h1 ref="heading" tabindex="-1">{{ person.name }}</h1>
            <a-button v-if="canWrite" size="small" type="text" class="person-edit-name" @click="rename=person.name;renameError=''">修改姓名</a-button>
          </div>
          <dl>
            <div v-if="person.sourceStatus==='INACTIVE'"><dt>状态</dt><dd>已停用 <a v-if="person.canonicalId" :href="personLink(person.canonicalId)">查看合并后的人员</a></dd></div>
            <div><dt>人员 ID</dt><dd class="numeric">{{ person.id }}</dd></div>
            <div><dt>建档时间</dt><dd>{{ formatTime(person.createdAt) }}</dd></div>
          </dl>
        </div>
      </div>
    </div>
    <template v-if="person">
      <OrganizationAppointmentForm v-if="canEdit && appointment" :api="api" :mode="appointment.mode" :record="appointment.record"
        :tree="appointmentTree" :kinds="appointmentKinds" :fixed-person="person"
        @saved="appointmentSaved" @close="appointment=undefined"/>
      <a-modal :open="rename!==undefined" title="修改人员姓名" ok-text="保存" cancel-text="取消" :confirm-loading="renameBusy" :ok-button-props="{disabled:!rename?.trim() || rename.trim()===person.name}" @ok="saveName" @cancel="rename=undefined">
       <a-alert v-if="renameError" :message="renameError" type="error" show-icon/>
       <a-form layout="vertical"><a-form-item label="人员姓名" html-for="person-rename">
         <a-input id="person-rename" v-model:value="rename" :maxlength="25" show-count/>
       </a-form-item></a-form>
      </a-modal>
      <nav ref="sectionNav" class="workspace-nav person-detail-nav" aria-label="个人信息导航">
        <a v-for="item in sections" :key="item.key" :href="personLink(personId,item.key==='overview' ? undefined : item.key)"
          :aria-current="item.key===selected.key ? 'page' : undefined">{{ item.label }}</a>
      </nav>
      <div class="workspace-page-body person-detail-body">
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <a-alert v-if="actionError" class="feedback" type="error" :message="actionError" show-icon/>
        <div v-if="selected.key==='overview'" class="person-dossier">
          <OrganizationPersonAppointments ref="personAppointments" :api="api" :person-id="personId" :initial-history="section==='appointments'"
            :can-write="canEdit" :appointment-loading="appointmentLoading" :option-label="optionLabel" :format-time="formatTime" @appointment="editAppointment"/>
          <div ref="accountArea" class="person-account-area" tabindex="-1">
            <OrganizationPersonAccounts ref="accountWorkspace" :api="api" :person="person" :can-write="canWrite"
              :format-time="formatTime" :format-identity-time="formatIdentityTime"/>
          </div>
        </div>
        <template v-if="selected.key==='finance'">
          <section class="records person-report-filter">
            <label for="person-report-month">结算月</label>
            <a-input id="person-report-month" v-model:value="reportMonth" type="month"/>
          </section>
          <MonthlyReport ref="report" :person-id="personId" :api="api" :format-money="formatMoney" :month="reportMonth"
            v-model:view="reportView" v-model:dimension="reportDimension" :views="reportViews" :dimensions="personDimensions"
            :period-link="periodLink"/>
        </template>
        <KeepAlive v-if="selected.key==='duties'">
          <OrganizationDuties ref="duties" profile :api="api" :format-time="formatTime" :subject="dutySubject"
            :can-write="canEdit" :person-link="personLink"/>
        </KeepAlive>
        <KeepAlive>
          <CatalogAppointments v-if="selected.key==='positions'" ref="positions" :person="person" :api="api"
            :can-write="canEdit" :roles="catalogRoles" :scopes="catalogScopes" :format-time="formatTime"/>
        </KeepAlive>
      </div>
    </template>
  </article>
</template>

<style scoped>
.person-detail {width:min(100%,1040px);margin-inline:auto}
.person-detail-header {padding-bottom:10px}
.person-dossier {display:grid;gap:12px}
.person-account-area {min-width:0;scroll-margin-top:10px}
.person-detail-heading {flex:1}
.person-name-row {display:flex;flex-wrap:wrap;gap:2px 8px;align-items:center}
.person-edit-name {color:var(--muted)}
.person-edit-name:hover {color:var(--accent)}
@media (max-width:680px) {
 .person-detail-identity {align-items:flex-start;gap:10px}
 .person-detail-nav {column-gap:10px}
}
</style>

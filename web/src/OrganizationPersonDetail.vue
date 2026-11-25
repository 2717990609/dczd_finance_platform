<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { DownOutlined, LeftOutlined, UserOutlined } from '@ant-design/icons-vue'
import OrganizationDispatch from './OrganizationDispatch.vue'
import OrganizationAppointmentForm from './OrganizationAppointmentForm.vue'
import OrganizationPersonOverview from './OrganizationPersonOverview.vue'
import PersonAppointmentActions from './PersonAppointmentActions.vue'
import { organizationTree } from './organization-tree'
import MonthlyReport from './MonthlyReport.vue'
import OrganizationDuties from './OrganizationDuties.vue'
import OrganizationSourceRelations from './OrganizationSourceRelations.vue'

const props = defineProps({
  api:Function, personId:Number, section:String, back:String, personLink:Function,
  optionLabel:Function, formatTime:Function, formatIdentityTime:Function, formatMoney:Function,
  month:String, reportViews:Array, reportDimensions:Array, reportFilters:Object, periodLink:Function, canWrite:Boolean, appointmentKinds:Array,
})

const emit = defineEmits(['report-filters'])
const dateColumns = [
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:154},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:154},
]
const statusColumn = {title:'状态',key:'status',width:94}
const sections = [
  {key:'overview', label:'概览'},
  {key:'appointments', label:'任职记录', path:'/organization-assignments', empty:'暂无组织任职记录', columns:[
    {title:'组织',dataIndex:'unitName',width:185},
    {title:'任职',key:'kind',width:168}, statusColumn, ...dateColumns,
  ]},
  {key:'duties', label:'业务职责'},
  {key:'positions', label:'分润岗位', path:'/catalog-appointments', empty:'暂无分润岗位记录', columns:[
    {title:'岗位',key:'role',width:168}, {title:'适用范围',key:'scope',width:185}, statusColumn, ...dateColumns,
  ]},
  {key:'accounts', label:'账号与身份', path:'/person-source-accounts', empty:'暂无业务来源账号', columns:[
    {title:'业务来源',key:'source',width:120}, {title:'来源账号 ID',dataIndex:'externalId',width:170},
    {title:'记录来源',key:'origin',width:104}, statusColumn, ...dateColumns, {title:'操作',key:'accountAction',width:100},
  ]},
  {key:'finance', label:'业绩与分润'},
]
const mainSections=['overview','appointments','accounts','finance']
const mainTabs=sections.filter(item=>mainSections.includes(item.key))
const otherTabs=sections.filter(item=>!mainSections.includes(item.key))
const moreOpen=ref(false)
const selected = computed(() => sections.find(item=>item.key===props.section) || sections[0])
const person = ref()
const canEdit=computed(()=>props.canWrite && person.value?.sourceStatus!=='INACTIVE')
const overview=ref()
const appointment=ref(),appointmentUnits=ref(),appointmentLoading=ref(false),actionError=ref(''),notice=ref('')
const appointmentTree=computed(()=>organizationTree(appointmentUnits.value || [],kind=>props.optionLabel('organizationKind',kind) || kind,true))
const detailColumns=computed(()=>selected.value.key==='appointments' && canEdit.value
 ? [...selected.value.columns,{title:'操作',key:'appointmentAction',width:96}]
 : selected.value.columns)
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
 if(selected.value.key==='overview')overview.value?.refresh()
 else loadRows()
}
const heading = ref()
const sectionNav = ref()
const profileError = ref('')
const profileLoading = ref(false)
const rows = ref({total:0,items:[]})
const listPage = ref(1)
const listError = ref('')
const listLoading = ref(false)
const report = ref()
const duties = ref()
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
const account = ref()
const accountTitle = computed(() => account.value ? sourceLabel(account.value.source)+' · '+account.value.externalId+' 归属历史' : '')
let profileRequest = 0
let rowsRequest = 0
let accountRequest = 0
const pagination = computed(() => ({current:listPage.value,total:rows.value.total,pageSize:25,showSizeChanger:false,hideOnSinglePage:true}))
const financeStatus = {
  ACTIVE:{label:'已启用',color:'green'},
  DISABLED:{label:'已停用'},
  UNCONFIGURED:{label:'未开通'},
}
const accountHistoryColumns = [
  {title:'归属人员',dataIndex:'personName',key:'personName',width:155},
  ...dateColumns,
]
function statusFor(record) {
  const now = Date.now()
  if (Date.parse(record.validFrom)>now) return {label:'未生效',color:'blue'}
  if (record.validTo && Date.parse(record.validTo)<=now) return {label:'已结束'}
  return {label:'有效',color:'green'}
}
const sourceLabel = source => props.optionLabel('source',source)?.split(' · ')[0]

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
      heading.value?.focus({preventScroll:true})
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
async function loadRows() {
  const request = ++rowsRequest
  listError.value = ''
  rows.value = {total:0,items:[]}
  if (!selected.value.path) {
    listLoading.value = false
    return
  }
  listLoading.value = true
  const params = new URLSearchParams({personId:props.personId,page:listPage.value})
  if (selected.value.key!=='accounts') params.set('history','true')
  try {
    const result = await props.api(selected.value.path+'?'+params)
    if (request===rowsRequest) rows.value = result
  } catch (error) {
    if (request===rowsRequest) listError.value = error.message
  } finally {
    if (request===rowsRequest) listLoading.value = false
  }
}
function changePage(next) {
  if (listPage.value===next.current) return
  listPage.value = next.current
  loadRows()
}
async function showAccountHistory({source,externalId}) {
  const request = ++accountRequest
  account.value = {source,externalId,loading:true,items:[]}
  try {
    const result = await props.api('/identities?'+new URLSearchParams({source,externalId}))
    if (request===accountRequest) account.value.items = result
  } catch (error) {
    if (request===accountRequest) account.value.error = error.message
  } finally {
    if (request===accountRequest) account.value.loading = false
  }
}
function closeAccountHistory() {
  accountRequest++
  account.value = undefined
}
function refresh() {
  loadPerson()
  if (selected.value.key==='overview') overview.value?.refresh()
  else if (selected.value.key==='finance') report.value?.refresh()
  else if (selected.value.key==='duties') duties.value?.refresh()
  else loadRows()
}
watch(() => selected.value.key, (key, previous) => {
  if (key!=='accounts') closeAccountHistory()
  moreOpen.value=false
  actionError.value=''
  notice.value=''
  if(previous){
    appointment.value=undefined
    nextTick(()=>sectionNav.value?.scrollIntoView({block:'nearest'}))
  }
  listPage.value = 1
  loadRows()
}, {immediate:true})
watch([reportMonth,reportView], ([month,view]) => {
  if (selected.value.key==='finance') emit('report-filters',{month,view})
})
loadPerson(true)
onBeforeUnmount(() => { profileRequest++; rowsRequest++; accountRequest++ })
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
            <div v-if="person.sourceStatus==='INACTIVE'"><dt>老财务状态</dt><dd>已停用 <a v-if="person.canonicalId" :href="personLink(person.canonicalId)">查看合并后的人员</a></dd></div>
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
        <a v-for="item in mainTabs" :key="item.key" :href="personLink(personId,item.key==='overview' ? undefined : item.key)"
          :aria-current="item.key===selected.key ? 'page' : undefined">{{ item.label }}</a>
        <a-dropdown v-model:open="moreOpen" :trigger="['click']">
          <button type="button" class="organization-more" :class="{'is-current':otherTabs.includes(selected)}"
            aria-haspopup="menu" :aria-expanded="moreOpen">更多<DownOutlined aria-hidden="true"/></button>
          <template #overlay>
            <a-menu @click="moreOpen=false">
              <a-menu-item v-for="item in otherTabs" :key="item.key">
                <a :href="personLink(personId,item.key)" :aria-current="item.key===selected.key ? 'page' : undefined">{{item.label}}</a>
              </a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </nav>
      <div class="workspace-page-body person-detail-body">
        <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
        <a-alert v-if="actionError" class="feedback" type="error" :message="actionError" show-icon/>
        <OrganizationPersonOverview v-if="selected.key==='overview'" ref="overview" :api="api" :person-id="personId"
          :can-write="canEdit" :appointment-loading="appointmentLoading" :person-link="personLink" :option-label="optionLabel" :format-time="formatTime" @appointment="editAppointment"/>
        <template v-if="selected.key==='finance'">
          <section class="records person-report-filter">
            <label for="person-report-month">结算月</label>
            <a-input id="person-report-month" v-model:value="reportMonth" type="month"/>
          </section>
          <MonthlyReport ref="report" :person-id="personId" :api="api" :format-money="formatMoney" :month="reportMonth"
            v-model:view="reportView" v-model:dimension="reportDimension" :views="reportViews" :dimensions="personDimensions"
            :period-link="periodLink"/>
        </template>
        <OrganizationDispatch v-if="selected.key==='accounts'" class="person-dispatch-panel" embedded source-accounts-in-profile :person-id="personId" :person-name="person.name"
          :api="api" :can-write="canEdit" :format-time="formatTime" @changed="refresh"/>
        <OrganizationSourceRelations v-if="['duties','accounts'].includes(selected.key)" :api="api" :person-id="personId" :group="selected.key" :format-time="formatTime"/>
        <KeepAlive v-if="selected.key==='duties'">
          <OrganizationDuties ref="duties" profile :api="api" :format-time="formatTime" :subject="dutySubject"
            :can-write="canWrite" :person-link="personLink"/>
        </KeepAlive>
        <template v-else-if="selected.path">
          <section v-if="selected.key==='accounts' && financeStatus[person.accountStatus]" class="records person-finance-account">
            <h2>财务登录账号</h2>
            <a-tag :color="financeStatus[person.accountStatus].color">{{ financeStatus[person.accountStatus].label }}</a-tag>
            <span v-if="person.ownLogin" class="numeric">{{ person.ownLogin }}</span>
          </section>
          <section class="records">
            <div class="records-head">
              <h2>{{ selected.key==='accounts' ? '业务来源账号' : selected.label }}</h2>
              <span v-if="!listLoading && !listError" class="person-record-count">{{ rows.total }} 条记录</span>
              <a-button v-if="selected.key==='appointments' && canEdit" type="primary" :loading="appointmentLoading" @click="editAppointment('add')">登记任职</a-button>
            </div>
            <a-alert v-if="listError" class="feedback" type="error" :message="listError" show-icon>
              <template #action><a-button size="small" @click="loadRows">重试</a-button></template>
            </a-alert>
            <a-table v-else :key="selected.key" size="middle" :columns="detailColumns" :data-source="rows.items"
              :loading="listLoading" :pagination="pagination" :scroll="{x:detailColumns.reduce((width,column)=>width+column.width,0)}"
              row-key="id" @change="changePage">
              <template #bodyCell="{column,record,text}">
                <template v-if="column.key==='kind'">
                  {{ optionLabel('appointmentKind',record.kind) }}
                  <a-tag v-if="record.appointmentType && record.appointmentType!==record.kind">{{ optionLabel('appointmentKind',record.appointmentType) }}</a-tag>
                  <small v-if="record.position" class="order-version">{{ record.position }}</small>
                </template>
                <span v-else-if="column.key==='role'">{{ optionLabel('catalogAppointmentRole',record.role) }}</span>
                <template v-else-if="column.key==='scope'">
                  {{ record.scopeName }}<small class="order-version">{{ optionLabel('catalogAppointmentScope',record.scope) }}</small>
                </template>
                <a-tag v-else-if="column.key==='status'" :color="statusFor(record).color">{{ statusFor(record).label }}</a-tag>
                <span v-else-if="column.key==='source'">{{ sourceLabel(record.source) }}</span>
                <span v-else-if="column.key==='origin'">{{ record.origin==='DIRECTORY' ? '人员映射' : '业务导入' }}</span>
                <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ selected.key==='accounts' ? formatIdentityTime(text) : formatTime(text) }}</span>
                <template v-else-if="column.key==='appointmentAction'">
                  <PersonAppointmentActions v-if="statusFor(record).label!=='已结束'" :label="record.unitName" @select="mode=>editAppointment(mode,record)"/>
                </template>
                <template v-else-if="column.key==='accountAction'">
                  <a-button v-if="record.origin==='DIRECTORY'" type="link" class="person-account-history" size="small"
                    @click="showAccountHistory(record)">账号历史</a-button>
                  <span v-else>—</span>
                </template>
              </template>
              <template #emptyText>{{ selected.empty }}</template>
            </a-table>
          </section>
        </template>
      </div>
    </template>
    <a-modal v-if="account" open :title="accountTitle" :width="580" :footer="null" @cancel="closeAccountHistory">
      <a-alert v-if="account.error" type="error" :message="account.error" show-icon>
        <template #action><a-button size="small" @click="showAccountHistory(account)">重试</a-button></template>
      </a-alert>
      <a-table v-else size="small" row-key="id" :columns="accountHistoryColumns" :data-source="account.items"
        :loading="account.loading" :pagination="{pageSize:12,hideOnSinglePage:true}" :scroll="{x:463}">
        <template #bodyCell="{column,record,text}">
          <template v-if="column.key==='personName'">
            <a v-if="record.personId!==personId" :href="personLink(record.personId,'accounts')"
              class="person-name-link" @click="closeAccountHistory">{{ text }}</a>
            <span v-else>{{ text }}</span>
            <small class="order-version">人员 {{ record.personId }}</small>
          </template>
          <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatIdentityTime(text) }}</span>
        </template>
      </a-table>
    </a-modal>
  </article>
</template>

<style scoped>
.person-detail-header {padding-bottom:14px}
.person-detail-heading {flex:1}
.person-name-row {display:flex;flex-wrap:wrap;gap:2px 8px;align-items:center}
.person-edit-name {color:var(--muted)}
.person-edit-name:hover {color:var(--accent)}
.person-dispatch-panel {margin-bottom:12px}
@media (max-width:680px) {
 .person-detail-identity {align-items:flex-start;gap:10px}
 .person-detail-nav {column-gap:10px}
}
</style>

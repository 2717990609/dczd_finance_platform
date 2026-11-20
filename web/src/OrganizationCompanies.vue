<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RadioGroup } from 'ant-design-vue'
import OrganizationCategoryEditor from './OrganizationCategoryEditor.vue'
import OrganizationCompanyType from './OrganizationCompanyType.vue'

const props = defineProps({api:Function,formatTime:Function,canWrite:Boolean})
const emit = defineEmits(['show-appointments','show-duties'])
const type = ref('')
const name = ref('')
const search = ref('')
const category = ref()
const page = ref(1)
const result = ref({items:[],categories:[],total:0})
const loading = ref(false)
const error = ref('')
const history = ref()
const companyTypeDetails = ref()
const manageCategories = ref(false)
const selectedIds = ref([])
const assignment = ref()
const assignmentError = ref('')
const assignmentSaving = ref(false)
const notice = ref('')
const selected = computed(() => result.value.items.filter(row=>selectedIds.value.includes(row.id)))
const selection = computed(() => props.canWrite ? {
  selectedRowKeys:selectedIds.value,
  onChange:keys=>{ selectedIds.value=keys },
  getCheckboxProps:row=>({disabled:row.status!=='ACTIVE'}),
} : undefined)
let request = 0
let historyRequest = 0
const types = [
  {value:'',label:'全部公司'}, {value:'INTERNAL',label:'内部'},
  {value:'EXTERNAL',label:'外部'}, {value:'UNKNOWN',label:'待确认'},
]
const typeLabels = Object.fromEntries(types.map(row=>[row.value,row.label]))
const columns = [
  {title:'销售公司',dataIndex:'name',width:210},
  {title:'内外部',key:'type',width:95},
  {title:'销售类别',key:'category',width:145},
  {title:'上级组织',key:'parent',width:170},
  {title:'状态',key:'status',width:82},
  {title:'操作',key:'actions',width:238},
]
const historyColumns = [
  {title:'销售类别',key:'category',width:160},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:175},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:175},
]
const pagination = computed(() => ({current:page.value,pageSize:25,total:result.value.total,showSizeChanger:false}))
async function refresh() {
  const seq = ++request
  loading.value = true
  error.value = ''
  const query = new URLSearchParams({page:page.value,type:type.value,name:search.value})
  if (category.value) query.set('categoryId',category.value)
  try {
    const rows = await props.api('/organization-sales-companies?'+query)
    if (seq===request) result.value = rows
  } catch (err) {
    if (seq===request) {
      error.value = err.message
      result.value = {...result.value,items:[],total:0}
    }
  } finally {
    if (seq===request) loading.value = false
  }
}
function changePage(next) {
  selectedIds.value = []
  page.value = next.current
  refresh()
}
function closeHistory() {
  historyRequest++
  history.value = undefined
}
async function openHistory(company) {
  const seq = ++historyRequest
  history.value = {company,items:[],loading:true,error:''}
  try {
    const items = await props.api('/organization-sales-company-categories?unitId='+company.id)
    if (seq===historyRequest) history.value.items = items
  } catch (err) {
    if (seq===historyRequest) history.value.error = err.message
  } finally {
    if (seq===historyRequest) history.value.loading = false
  }
}
function openAssignment(companies) {
  notice.value = ''
  assignmentError.value = ''
  assignment.value = {companies,categoryId:companies.length===1 ? companies[0].categoryId ?? undefined : undefined}
}
async function applyAssignment() {
  if (!assignment.value || assignmentSaving.value) return
  const {categoryId,companies} = assignment.value
  if (!categoryId) {
    assignmentError.value = '请选择销售类别。'
    return
  }
  assignmentSaving.value = true
  assignmentError.value = ''
  try {
    const saved = await props.api('/organization-sales-company-categories',{method:'POST',body:JSON.stringify({
      categoryId,companies:companies.map(row=>({unitId:row.id,expectedCategoryId:row.categoryId || '',expectedFrom:row.categoryFrom})),
    })})
    assignment.value = undefined
    selectedIds.value = []
    notice.value = saved.changed ? '已调整 '+saved.changed+' 家公司的销售类别，生效时间 '+props.formatTime(saved.effectiveAt)+'。' : '销售类别未变化。'
    await refresh()
    if (!result.value.items.length && page.value>1) {
      page.value = 1
      await refresh()
    }
  } catch (err) {
    assignmentError.value = err.message
  } finally {
    assignmentSaving.value = false
  }
}
watch([type,category,search], () => {
  selectedIds.value = []
  page.value = 1
  refresh()
}, {immediate:true})
onBeforeUnmount(() => { request++; historyRequest++ })
defineExpose({refresh})
</script>

<template>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon>
    <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon closable @close="notice=''"/>
  <section class="records">
    <div class="records-head company-directory-head">
      <h2>销售公司目录</h2>
      <RadioGroup v-model:value="type" :options="types" option-type="button" button-style="solid" aria-label="销售公司类型"/>
      <div v-if="canWrite" class="topbar-actions category-actions">
        <a-button v-if="selected.length" @click="openAssignment(selected)">批量调整类别</a-button>
        <a-button @click="manageCategories=true">管理销售类别</a-button>
      </div>
    </div>
    <div class="organization-filters company-directory-filters">
      <div>
        <label for="company-category">销售类别</label>
        <a-select id="company-category" v-model:value="category" :options="result.categories" show-search option-filter-prop="label"
          placeholder="全部类别" allow-clear/>
      </div>
      <div>
        <label for="company-name">公司名称</label>
        <a-input-search id="company-name" v-model:value="name" placeholder="搜索销售公司" allow-clear
          @search="search=$event.trim()" @change="!name && (search='')"/>
      </div>
    </div>
    <div class="table-container">
      <a-table :columns="columns" :data-source="result.items" :loading="loading" row-key="id" :pagination="pagination"
        :scroll="{x:canWrite ? 970 : 938}" :row-selection="selection" @change="changePage">
        <template #bodyCell="{column,record}">
          <button v-if="column.key==='type'" type="button" class="order-link" :aria-label="'查看'+record.name+'的公司类型'"
            @click="companyTypeDetails=record">
            <a-tag :color="record.companyType==='UNKNOWN' ? 'orange' : 'blue'">{{ typeLabels[record.companyType] }}</a-tag>
          </button>
          <template v-else-if="column.key==='category'">
            <button v-if="canWrite && record.status==='ACTIVE'" type="button" class="order-link"
              :aria-label="'调整'+record.name+'的销售类别'" @click="openAssignment([record])">{{ record.categoryName || '未分类' }}</button>
            <span v-else>{{ record.categoryName || '未分类' }}</span>
          </template>
          <span v-else-if="column.key==='parent'">{{ record.parentName || '—' }}</span>
          <a-tag v-else-if="column.key==='status'" :color="record.status==='ACTIVE' ? 'green' : 'default'">{{ record.status==='ACTIVE' ? '启用' : '停用' }}</a-tag>
          <template v-else-if="column.key==='actions'">
            <a-button type="link" size="small" @click="emit('show-appointments',record.id)">查看任职</a-button>
            <a-button type="link" size="small" @click="openHistory(record)">类别历史</a-button>
            <a-button type="link" size="small" @click="emit('show-duties',{unitId:record.id,name:record.name})">业务职责</a-button>
          </template>
        </template>
        <template #emptyText>{{ error ? '销售公司读取失败' : '暂无符合条件的销售公司' }}</template>
      </a-table>
    </div>
  </section>
  <OrganizationCompanyType v-if="companyTypeDetails" :api="api" :company="companyTypeDetails" :can-write="canWrite"
    :format-time="formatTime" :types="types" @close="companyTypeDetails=undefined" @changed="refresh"/>
  <OrganizationCategoryEditor v-if="manageCategories" :api="api" @close="manageCategories=false" @changed="refresh"/>
  <a-drawer v-if="assignment" :open="true" title="调整销售类别" width="min(520px, 100vw)"
    :closable="!assignmentSaving" :mask-closable="!assignmentSaving" :keyboard="!assignmentSaving" @close="assignment=undefined">
    <a-alert v-if="assignmentError" class="feedback" type="error" :message="assignmentError" show-icon/>
    <a-form :model="assignment" layout="vertical" @finish="applyAssignment">
      <a-form-item label="公司">
        <span v-if="assignment.companies.length===1">{{ assignment.companies[0].name }}</span>
        <details v-else>
          <summary>已选 {{ assignment.companies.length }} 家公司</summary>
          <ul><li v-for="company in assignment.companies" :key="company.id">{{ company.name }}</li></ul>
        </details>
      </a-form-item>
      <a-form-item label="新销售类别" html-for="assign-category" required>
        <a-select id="assign-category" v-model:value="assignment.categoryId" :options="result.categories"
          :disabled="assignmentSaving" show-search option-filter-prop="label" placeholder="选择启用的类别"/>
      </a-form-item>
      <p class="draft-report-note">保存时生效。大春 AI 按付款时间匹配销售类别。</p>
    </a-form>
    <template #footer>
      <a-space>
        <a-button :disabled="assignmentSaving" @click="assignment=undefined">取消</a-button>
        <a-button :loading="assignmentSaving" type="primary" @click="applyAssignment">保存调整</a-button>
      </a-space>
    </template>
  </a-drawer>
  <a-drawer v-if="history" :open="true" :title="history.company.name+' · 类别历史'" width="min(580px, 100vw)" @close="closeHistory">
    <section aria-label="销售类别历史">
      <a-alert v-if="history.error" class="feedback" type="error" :message="history.error" show-icon>
        <template #action><a-button size="small" @click="openHistory(history.company)">重试</a-button></template>
      </a-alert>
      <a-table v-else :columns="historyColumns" :data-source="history.items" :loading="history.loading" row-key="validFrom"
        :pagination="{pageSize:15,hideOnSinglePage:true}" :scroll="{x:510}">
        <template #bodyCell="{column,record,text}">
          <span v-if="column.key==='category'">{{ record.categoryName || '未分类' }}</span>
          <span v-else>{{ text ? formatTime(text) : '—' }}</span>
        </template>
        <template #emptyText>暂无销售类别历史</template>
      </a-table>
    </section>
  </a-drawer>
</template>

<style scoped>
.company-directory-head { justify-content: flex-start; flex-wrap: wrap; }
.category-actions { margin-inline-start: auto; }
.company-directory-filters { padding-inline: 14px; }
.company-directory-filters .ant-select { min-width: 170px; }
</style>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Checkbox, TreeSelect } from 'ant-design-vue'

const props = defineProps({api:Function, optionLabel:Function, formatTime:Function})
const units = ref([])
const unitId = ref()
const name = ref('')
const search = ref('')
const on = ref('')
const history = ref(false)
const page = ref(1)
const entries = ref({total:0,items:[]})
const unitsError = ref('')
const entriesError = ref('')
const loading = ref(false)
let request = 0
let unitRequest = 0

const tree = computed(() => {
  const nodes = new Map(units.value.map(unit => [unit.id, {
    value:unit.id,
    title:unit.name + ' · ' + props.optionLabel('organizationKind',unit.kind) + (unit.status==='INACTIVE' ? '（已停用）' : ''),
    children:[],
  }]))
  const roots = []
  for (const unit of units.value) {
    const node = nodes.get(unit.id)
    const parent = nodes.get(unit.parentId)
    if (parent) parent.children.push(node)
    else roots.push(node)
  }
  return roots
})
const columns = [
  {title:'人员',dataIndex:'personName',key:'personName',width:130},
  {title:'组织',dataIndex:'unitName',key:'unitName',width:180},
  {title:'任职',dataIndex:'kind',key:'kind',width:135},
  {title:'生效时间',dataIndex:'validFrom',key:'validFrom',width:145},
  {title:'结束时间',dataIndex:'validTo',key:'validTo',width:145},
]
const pagination = computed(() => ({current:page.value,pageSize:25,total:entries.value.total,showSizeChanger:false}))

async function loadUnits() {
  const version = ++unitRequest
  unitsError.value = ''
  try {
    const rows = await props.api('/organization-units')
    if (version === unitRequest) units.value = rows
  } catch (error) {
    if (version === unitRequest) unitsError.value = error.message
  }
}

async function loadEntries() {
  const version = ++request
  loading.value = true
  entriesError.value = ''
  const params = new URLSearchParams({page:page.value})
  if (unitId.value) params.set('unitId',unitId.value)
  if (search.value) params.set('name',search.value)
  if (history.value) params.set('history','true')
  else if (on.value) params.set('on',on.value)
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

function refresh() { return Promise.all([loadUnits(),loadEntries()]) }
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
watch([unitId,on,history,search],filterEntries)
onMounted(refresh)
onBeforeUnmount(() => { request++; unitRequest++ })
defineExpose({refresh})
</script>

<template>
  <section class="heading">
    <div><h1>组织与人员</h1><p>查看公司的部门、小组和人员任职。</p></div>
  </section>
  <a-alert v-if="unitsError || entriesError" class="feedback" type="error" :message="unitsError || entriesError" show-icon/>
  <section class="records organization-directory">
    <div class="organization-filters">
      <div>
        <label for="organization-unit">组织</label>
        <TreeSelect id="organization-unit" aria-label="组织" v-model:value="unitId" :tree-data="tree" tree-node-filter-prop="title" placeholder="全部组织" show-search allow-clear/>
      </div>
      <div>
        <label for="organization-date">任职日期</label>
        <input id="organization-date" v-model="on" type="date" class="ant-input organization-date" :disabled="history"/>
      </div>
      <div>
        <label for="organization-name">人员</label>
        <a-input-search id="organization-name" v-model:value="name" placeholder="输入姓名" allow-clear @search="searchPeople" @change="!name && searchPeople()"/>
      </div>
      <Checkbox v-model:checked="history">显示全部任职记录</Checkbox>
    </div>
    <div class="records-head">
      <h2>人员任职</h2>
      <span class="organization-context">{{ history ? '全部有效区间' : on ? on+' 00:00（北京时间）' : '当前归属' }}</span>
    </div>
    <a-table :columns="columns" :data-source="entries.items" :loading="loading" row-key="id" :pagination="pagination" :scroll="{x:735}" @change="changePage">
      <template #bodyCell="{column,record,text}">
        <template v-if="column.key==='kind'">
          {{ optionLabel('appointmentKind',text) }}<small v-if="record.position" class="order-version">{{ record.position }}</small>
        </template>
        <span v-else-if="column.key==='validFrom'||column.key==='validTo'" class="numeric">{{ formatTime(text) }}</span>
      </template>
      <template #emptyText>当前条件下没有任职记录</template>
    </a-table>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({ api:Function, formatMoney:Function, productId:Number, views:Array, dimensions:Array, unitId:String })
const month = ref(new Intl.DateTimeFormat('sv-SE', {year:'numeric',month:'2-digit',timeZone:'Asia/Shanghai'}).format(new Date()))
const view = ref()
const dimension = ref()
const page = ref(1)
const snapshot = ref('')
const report = ref()
const loading = ref(false)
const error = ref('')
let sequence = 0

const dimensionLabel = computed(() => props.dimensions.find(option => option.value === dimension.value)?.label)
const columns = computed(() => [
  {title:dimensionLabel.value,dataIndex:'name',key:'name',width:175},
  {title:'净业绩',dataIndex:'performanceMinor',key:'performanceMinor',width:120,align:'right'},
  {title:'计提分润',dataIndex:'accruedMinor',key:'accruedMinor',width:115,align:'right'},
  {title:'退款扣减',dataIndex:'refundMinor',key:'refundMinor',width:110,align:'right'},
  {title:'受益人更正',dataIndex:'correctionMinor',key:'correctionMinor',width:120,align:'right'},
  {title:'团队池分配',dataIndex:'allocationMinor',key:'allocationMinor',width:120,align:'right'},
  {title:'净分润',dataIndex:'totalMinor',key:'totalMinor',width:120,align:'right'},
])
const periodColumns = [
  {title:'产品',dataIndex:'productName',key:'product',width:150},
  {title:'核算期',dataIndex:'name',key:'name',width:155},
  {title:'起止日期',key:'dates',width:210},
  {title:'结果状态',key:'status',width:150},
  {title:'净分润',dataIndex:'commissionMinor',key:'amount',width:120,align:'right'},
]
const statuses = {
  SETTLED:{label:'已结算',color:'green'}, DRAFT:{label:'草稿',color:'blue'},
  PENDING:{label:'待重新试算',color:'orange'}, NOT_CALCULATED:{label:'未试算',color:'orange'}, NEEDS_REVIEW:{label:'有待核项',color:'orange'},
}
const periodKinds = {calendar:'固定账期', class:'开班期次', refund_month:'退款账期'}
const pendingCount = computed(() => report.value?.periods.filter(period => period.status === 'PENDING' || period.status === 'NOT_CALCULATED').length || 0)
const unresolvedCount = computed(() => report.value?.periods.filter(period => period.issueCount>0 && period.status !== 'SETTLED').length || 0)
const includedCount = computed(() => report.value?.periods.filter(period => period.version != null).length || 0)
const pagination = computed(() => ({current:page.value,pageSize:25,total:report.value?.total || 0,showSizeChanger:false}))

async function load() {
  const request = ++sequence
  if (!month.value || !dimension.value || !view.value) {
    report.value = undefined
    loading.value = false
    return
  }
  error.value = ''
  loading.value = true
  const params = new URLSearchParams({month:month.value,view:view.value,dimension:dimension.value,page:page.value})
  if (props.productId) params.set('productId',props.productId)
  if (props.unitId) params.set('unitId',props.unitId)
  if (snapshot.value) params.set('snapshot',snapshot.value)
  try {
    const result = await props.api('/monthly-report?'+params)
    if (request===sequence) {
      report.value = result
      snapshot.value = result.snapshot
    }
  } catch (err) {
    if (request===sequence) {
      report.value = undefined
      error.value = err.status===409 ? '本月结果已变化，请刷新汇总。' : err.message
    }
  } finally {
    if (request===sequence) loading.value = false
  }
}

function refresh() {
  snapshot.value = ''
  page.value = 1
  load()
}

function changePage(next) {
  page.value = next.current
  load()
}

watch(() => props.views, choices => {
  if (!choices.some(option => option.value===view.value)) view.value = choices[0]?.value
}, {immediate:true})
watch(() => props.dimensions, choices => {
  if (!choices.some(option => option.value===dimension.value)) dimension.value = choices[0]?.value
}, {immediate:true})
watch([month,view,() => props.productId,() => props.unitId,dimension], (values, previous) => {
  report.value = undefined
  if (values.slice(0,4).some((value,index) => value!==previous[index])) {
    refresh()
  } else {
    page.value = 1
    load()
  }
}, {immediate:true})
onBeforeUnmount(() => { sequence++ })
defineExpose({refresh})
</script>

<template>
  <section class="heading">
    <h1>月度汇总</h1>
  </section>
  <section class="records draft-controls">
    <div class="draft-toolbar monthly-filters">
      <label for="monthly-report-month">报表月份</label>
      <a-input id="monthly-report-month" v-model:value="month" type="month"/>
      <label for="monthly-report-view">结果范围</label>
      <a-select id="monthly-report-view" v-model:value="view" :options="views"/>
      <label for="monthly-report-dimension">汇总维度</label>
      <a-select id="monthly-report-dimension" v-model:value="dimension" :options="dimensions"/>
      <a-button :disabled="!month || !view || !dimension" :loading="loading" @click="refresh">刷新汇总</a-button>
    </div>
  </section>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
  <template v-if="report">
    <a-alert v-if="pendingCount" class="feedback" type="warning" :message="
      pendingCount+' 个核算期待试算。尚无草稿的期次暂无金额，已有草稿的期次显示最近一次结果。'" show-icon/>
    <a-alert v-if="unresolvedCount" class="feedback" type="warning"
      :message="unresolvedCount+' 个核算期还有待核项，当前汇总可能缺少待核金额。'" show-icon/>
    <a-alert v-if="report.conflictingPerformance" class="feedback" type="warning"
      message="同一付款或退款在多个核算期的金额或人员归属不同，净业绩待核对。" show-icon/>
    <a-alert v-else-if="report.duplicateFacts" class="feedback" type="info"
      :message="report.duplicateFacts+' 笔重复的付款或退款已按来源主键只计一次业绩。'" show-icon/>
    <a-alert v-if="includedCount && !report.performanceReady && !report.conflictingPerformance" class="feedback"
      type="info" message="部分结果缺少完整的净业绩数据。" show-icon/>
    <div class="summary-grid">
      <article><div><small>净业绩</small><strong>{{ formatMoney(report.performanceMinor) }}</strong></div></article>
      <article><div><small>净分润</small><strong>{{ includedCount ? formatMoney(report.commissionMinor) : '—' }}</strong></div></article>
      <article><div><small>纳入汇总</small><strong>{{ includedCount }}<em> / {{ report.periods.length }} 期</em></strong></div></article>
    </div>
    <section class="records">
      <div class="records-head"><h2>{{ dimensionLabel }}汇总</h2></div>
      <a-table :columns="columns" :data-source="report.items" :loading="loading" :pagination="pagination" row-key="id"
        :scroll="{x:880}" @change="changePage">
        <template #bodyCell="{column,text,record}">
          <template v-if="column.key==='name'">{{ text }}<small v-if="record.subtitle" class="order-version">{{ record.subtitle }}</small></template>
          <strong v-else-if="column.key==='totalMinor'" class="numeric">{{ formatMoney(text) }}</strong>
          <span v-else class="numeric">{{ formatMoney(text) }}</span>
        </template>
        <template #emptyText>本月暂无汇总记录</template>
      </a-table>
    </section>
    <details v-if="report.periods.length" class="records monthly-periods">
      <summary>核算范围（{{ report.periods.length }} 期）</summary>
      <a-table :columns="periodColumns" :data-source="report.periods" row-key="id" :pagination="{pageSize:10,showSizeChanger:false}" :scroll="{x:785}">
        <template #bodyCell="{column,record,text}">
          <template v-if="column.key==='name'">{{ text }}<small class="order-version">{{ periodKinds[record.kind] }}</small></template>
          <span v-else-if="column.key==='dates'">{{ record.startsOn }} 至 {{ record.endsOn }}</span>
          <template v-else-if="column.key==='status'">
            <a-tag :color="statuses[record.status].color">{{ statuses[record.status].label }}</a-tag>
            <small v-if="record.version" class="order-version">v{{ record.version }}<template v-if="record.issueCount"> · {{ record.issueCount }} 条待核</template></small>
          </template>
          <span v-else-if="column.key==='amount'" class="numeric">{{ formatMoney(text) }}</span>
        </template>
      </a-table>
    </details>
  </template>
  <section v-else-if="!error" class="records empty">
    <a-empty :description="loading ? '正在汇总' : '请选择报表月份'"/>
  </section>
</template>

<style scoped>
.monthly-filters .ant-input { width: 150px; }
.monthly-filters .ant-select { min-width: 108px; }
.monthly-periods { margin-top: 16px; }
.monthly-periods > summary { padding: 12px 16px; cursor: pointer; font-weight: 600; }
</style>

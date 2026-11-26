<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RadioGroup } from 'ant-design-vue'

const props = defineProps({ api:Function, month:String, productId:Number, periods:Array, periodsLoading:Boolean, periodLink:Function })
const mode = defineModel({type:String})
const results = ref([])
const fetching = ref(false)
const loading = computed(() => fetching.value || props.periodsLoading)
const error = ref('')
let sequence = 0

const today = () => new Intl.DateTimeFormat('sv-SE', {dateStyle:'short',timeZone:'Asia/Shanghai'}).format(new Date())
const periodTypes = {calendar:'固定账期',class:'开班期次',refund_month:'退款账期'}
const modes = [{value:'pending',label:'待处理'},{value:'all',label:'全部期次'}]
const columns = [
  {title:'核算期',key:'period',width:200},
  {title:'当前事项',key:'task',width:140},
  {title:'操作',key:'action',width:120,responsive:['md']},
  {title:'起止日期',key:'dates',width:200,responsive:['md']},
]
const periodById = computed(() => new Map(props.periods.map(period=>[period.id,period])))

function nextTask(row, metadata, currentDate) {
  if (row.status==='SETTLED') return {label:'已结算',color:'green',action:'查看结算'}
  if (row.kind==='class' && metadata) {
    if (!metadata.sourcePeriodKey) return {label:'待关联来源期次',color:'orange',pending:true}
    if (!metadata.practiceRosterVersion) return {label:'待拉取签到名单',color:'orange',pending:true}
    if (!metadata.ruleVersion) return {label:'待配置规则',color:'orange',pending:true}
  }
  if (metadata?.ruleMatchingAvailable && !metadata.catalogDraftAvailable) {
    return {label:'待核对订单规则',color:'blue',pending:true,view:'matching',action:'查看规则匹配'}
  }
  if (row.status==='PENDING' || !row.version) {
    return {label:row.version ? '待重新试算' : '待试算',color:'orange',pending:true}
  }
  if (row.issueCount > 0) return {label:'待核 '+row.issueCount+' 条',color:'orange',pending:true,view:'issues',action:'查看待核'}
  if (row.endsOn < currentDate) {
    return row.kind!=='calendar' || metadata?.catalogDraftAvailable
      ? {label:'待结算核对',color:'blue',pending:true,view:'review',action:'去结算核对'}
      : {label:'待核对试算',color:'blue',pending:true,action:'查看试算'}
  }
  return {label:'已有草稿',color:'blue',action:'查看试算'}
}

const rows = computed(() => {
  const currentDate = today()
  return results.value.map(row=>({...row,task:nextTask(row,periodById.value.get(row.id),currentDate)}))
    .sort((left,right) => Number(!!right.task.pending)-Number(!!left.task.pending) || left.endsOn.localeCompare(right.endsOn))
})
const pendingRows = computed(() => rows.value.filter(row=>row.task.pending))
const visibleRows = computed(() => mode.value==='all' ? rows.value : pendingRows.value)
const emptyText = computed(() => {
  if (loading.value) return '正在读取期次'
  if (error.value) return '待办读取失败'
  if (!props.month) return '请选择结算月'
  if (!rows.value.length) return '所选月份和产品暂无核算期'
  return '当前范围暂无待处理期次'
})

async function refresh() {
  const request = ++sequence
  results.value = []
  error.value = ''
  fetching.value = Boolean(props.month)
  if (!props.month) return
  const params = new URLSearchParams({month:props.month,view:'current',dimension:'person',page:1})
  if (props.productId) params.set('productId',props.productId)
  try {
    const report = await props.api('/monthly-report?'+params)
    if (request===sequence) results.value = report.periods
  } catch (err) {
    if (request===sequence) error.value = err.message
  } finally {
    if (request===sequence) fetching.value = false
  }
}
watch([() => props.month, () => props.productId],refresh,{immediate:true})
onBeforeUnmount(() => { sequence++ })
defineExpose({refresh})
</script>

<template>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon>
    <template #action><a-button size="small" @click="refresh">重试</a-button></template>
  </a-alert>
  <section class="records">
    <div class="records-head period-tasks-head">
      <h2>核算期办理</h2>
      <RadioGroup v-model:value="mode" :options="modes" option-type="button" button-style="solid" role="radiogroup" aria-label="待办范围"/>
      <span v-if="!loading && !error && rows.length" role="status" class="period-tasks-count">
        {{ pendingRows.length }} 期待处理 / 共 {{ rows.length }} 期
      </span>
    </div>
    <div class="table-container">
      <a-table :key="mode" :columns="columns" :data-source="visibleRows" :loading="loading" row-key="id"
        :pagination="{pageSize:15,showSizeChanger:false,hideOnSinglePage:true}" :scroll="{x:true}">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='period'">
            <a :href="periodLink(record.id,record.task.view,'tasks')">{{ record.name }}</a>
            <small class="order-version">{{ record.productName }} · {{ periodTypes[record.kind] }}</small>
            <small class="order-version period-task-dates">{{ record.startsOn }} 至 {{ record.endsOn }}</small>
          </template>
          <template v-else-if="column.key==='task'">
            <a-tag :color="record.task.color">{{ record.task.label }}</a-tag>
            <small v-if="record.version" class="order-version">v{{ record.version }}</small>
          </template>
          <a v-else-if="column.key==='action'" :href="periodLink(record.id,record.task.view,'tasks')">{{ record.task.action || '查看本期' }}</a>
          <span v-else>{{ record.startsOn }} 至 {{ record.endsOn }}</span>
        </template>
        <template #emptyText>
          <a-empty :description="emptyText">
            <a-button v-if="!loading && !error && month && !rows.length" :href="'#periods'+(productId ? '?productId='+productId : '')">前往核算期次</a-button>
            <a-button v-if="!loading && rows.length && !pendingRows.length" @click="mode='all'">查看全部期次</a-button>
          </a-empty>
        </template>
      </a-table>
    </div>
  </section>
</template>

<style scoped>
.period-tasks-head { justify-content: flex-start; flex-wrap: wrap; }
.period-tasks-count { color: var(--muted); font-size: 13px; }
@media (min-width: 768px) {
  .period-task-dates { display: none; }
}
</style>

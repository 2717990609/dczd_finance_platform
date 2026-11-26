<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Switch } from 'ant-design-vue'

const props = defineProps({api:Function, formatTime:Function})
const rows = ref([])
const jobs = ref({})
const loading = ref(false)
const settingsError = ref('')
const statusError = ref('')
const products = ref([])
const productId = ref()
const pullingPeriods = ref(false)
const periodError = ref('')
const periodResult = ref()
const sourceProductsError = ref('')
let statusTimer
let statusRequest = 0
let closed = false

const labels = {
  VIP_ORDERS:'大春 AI · 会员订单',
  HEALTH_IDENTITY_ORDERS:'大春 AI · 健康身份订单',
  ORDER_FACTS:'大春之道 · 实战班订单'
}
const dateFormat = new Intl.DateTimeFormat('sv-SE',{dateStyle:'short',timeZone:'Asia/Shanghai'})
const today = dateFormat.format(new Date())
const productOptions = computed(() => products.value.map(product=>({value:product.id,label:product.name})))
const dczd = computed(() => rows.value.find(row=>row.stream==='ORDER_FACTS'))
const jobFor = row => jobs.value[row.stream]
const busy = job => job?.status==='QUEUED' || job?.status==='RUNNING'
const changed = row => row.edit.auto!==row.saved.auto || row.edit.intervalMinutes!==row.saved.intervalMinutes ||
  row.edit.fromDate!==row.saved.fromDate

function runDate(row) {
  const job = jobFor(row)
  if (job?.targetThrough) return ''
  const retry = job?.status==='FAILED' ? job.requestedFromDate : ''
  return (job?.initialFrom ? retry : row.saved.fromDate || retry) || ''
}

function runDisabledReason(row) {
  const job = jobFor(row)
  if (!row.configured) return '来源尚未接入'
  if (!job) return '暂无任务状态'
  if (job.sourceMatches===false) return '当前来源与已有同步记录不一致'
  if (job.unverifiedSource) return '已有订单数据尚未绑定来源'
  if (!busy(job) && !job.initialFrom && !job.targetThrough && !runDate(row)) return '首次拉取前，请保存起点。'
  return ''
}

function runStatus(row) {
  const job = jobFor(row)
  if (!row.configured) return ['未接入','default']
  if (job?.sourceMatches===false || job?.unverifiedSource) return ['来源异常','orange']
  if (job?.status==='QUEUED') return ['已排队','blue']
  if (job?.status==='RUNNING') return ['拉取中','blue']
  if (job?.status==='FAILED' || job?.lastSyncFailed) return ['拉取失败','red']
  if (job?.targetThrough) return ['待续拉','orange']
  if (job?.lastSuccessAt) return ['拉取完成','green']
  return [job ? '尚未拉取' : '状态未知','default']
}

async function loadStatus() {
  const request = ++statusRequest
  try {
    const statuses = await props.api('/orders/sync')
    if (closed || request!==statusRequest) return
    jobs.value = Object.fromEntries(statuses.map(job=>[job.stream,job]))
    statusError.value = ''
  } catch (err) {
    if (!closed && request===statusRequest) statusError.value = err.message
  }
}

async function reload() {
  if (loading.value) return
  loading.value = true
  const [settings, sourceProducts] = await Promise.allSettled([
    props.api('/orders/sync-settings'), props.api('/periods/source-products'), loadStatus()
  ])
  if (closed) return
  if (settings.status==='fulfilled') {
    const current = new Map(rows.value.map(row=>[row.stream,row]))
    rows.value = settings.value.map(({stream,configured,...saved}) => {
      const row = current.get(stream)
      if (!row) return {stream,configured,saved,edit:{...saved},feedback:null}
      row.configured = configured
      if (!row.saving) {
        if (!changed(row)) row.edit = {...saved}
        row.saved = saved
      }
      return row
    })
    settingsError.value = ''
  } else {
    settingsError.value = settings.reason.message
  }
  if (sourceProducts.status==='fulfilled') {
    products.value = sourceProducts.value
    if (!products.value.some(product=>product.id===productId.value)) {
      selectProduct(products.value.length===1 ? products.value[0].id : undefined)
    }
    sourceProductsError.value = ''
  } else {
    sourceProductsError.value = sourceProducts.reason.message
  }
  loading.value = false
}

async function save(row) {
  if (row.saving || !changed(row)) return
  row.saving = true
  row.feedback = null
  const {stream,edit} = row
  const request = {...edit}
  try {
    await props.api('/orders/sync-settings',{method:'POST',body:JSON.stringify({stream,...request})})
    row.saved = request
    row.feedback = {text:'配置已保存',error:false}
  } catch (err) {
    row.feedback = {text:err.message,error:true}
  } finally {
    row.saving = false
  }
}

async function start(row) {
  if (row.starting || row.saving || busy(jobFor(row)) || runDisabledReason(row)) return
  row.starting = true
  row.feedback = null
  try {
    const job = await props.api('/orders/sync',{method:'POST',body:JSON.stringify({stream:row.stream,fromDate:runDate(row)})})
    ++statusRequest
    jobs.value[row.stream] = {...jobFor(row),status:job.status,taskId:job.id,message:''}
    await loadStatus()
  } catch (err) {
    row.feedback = {text:err.message,error:true}
  } finally {
    row.starting = false
  }
}

function selectProduct(value) {
  productId.value = value
  periodResult.value = undefined
  periodError.value = ''
}

async function pullPeriods() {
  if (pullingPeriods.value || !productId.value || !dczd.value?.configured) return
  pullingPeriods.value = true
  periodResult.value = undefined
  periodError.value = ''
  try {
    periodResult.value = await props.api('/orders/sync-periods',{method:'POST',body:JSON.stringify({productId:productId.value})})
  } catch (err) {
    periodError.value = err.message
  } finally {
    pullingPeriods.value = false
  }
}

async function pollStatus() {
  if (document.visibilityState==='visible') await loadStatus()
  if (!closed) statusTimer = setTimeout(pollStatus,5000)
}

onMounted(() => {
  reload()
  statusTimer = setTimeout(pollStatus,5000)
})
onBeforeUnmount(() => { closed = true; clearTimeout(statusTimer) })
defineExpose({reload})
</script>

<template>
  <a-alert v-if="settingsError" class="feedback" type="error" :message="settingsError" show-icon/>
  <a-alert v-if="statusError" class="feedback" type="warning" message="暂时无法更新拉取状态" :description="statusError" show-icon/>
  <a-spin :spinning="loading && !rows.length">
    <section v-for="row in rows" :key="row.stream" class="records sync-settings" :aria-labelledby="'sync-title-'+row.stream">
      <div class="records-head sync-head">
        <div class="sync-heading">
          <h2 :id="'sync-title-'+row.stream">{{ labels[row.stream] }}</h2>
          <a-tag :color="runStatus(row)[1]" role="status">{{ runStatus(row)[0] }}</a-tag>
        </div>
        <a-button :loading="row.starting" :disabled="!!runDisabledReason(row) || row.saving || busy(jobFor(row))"
          :aria-describedby="runDisabledReason(row) ? 'sync-hint-'+row.stream : undefined" @click="start(row)">
          {{ jobFor(row)?.targetThrough ? '继续拉取' : jobFor(row)?.status==='FAILED' ? '重试' : '立即拉取' }}
        </a-button>
      </div>
      <div v-if="runDisabledReason(row) || jobFor(row)?.lastSuccessAt || jobFor(row)?.targetThrough ||
        jobFor(row)?.message || jobFor(row)?.requestedFromDate && !jobFor(row)?.initialFrom" class="sync-state">
        <span v-if="runDisabledReason(row)" :id="'sync-hint-'+row.stream">{{ runDisabledReason(row) }}</span>
        <span v-if="jobFor(row)?.requestedFromDate && !jobFor(row)?.initialFrom">上次起点 {{ jobFor(row).requestedFromDate }}</span>
        <span v-if="jobFor(row)?.lastSuccessAt">最近成功 {{ formatTime(jobFor(row).lastSuccessAt) }}</span>
        <span v-if="jobFor(row)?.completedThrough">数据覆盖至 {{ formatTime(jobFor(row).completedThrough) }}</span>
        <span v-if="busy(jobFor(row)) && jobFor(row)?.scannedOrders">已读取 {{ jobFor(row).scannedOrders }} 条</span>
        <span v-else-if="jobFor(row)?.status==='SUCCEEDED' && jobFor(row).newVersions!=null">新增 {{ jobFor(row).newVersions }} 个版本</span>
        <span v-if="jobFor(row)?.message" class="sync-error" role="status">{{ jobFor(row).message }}</span>
      </div>
      <a-form :model="row.edit" class="sync-form sync-order-form" layout="vertical" @finish="save(row)">
        <a-form-item label="自动拉取" :html-for="'sync-auto-'+row.stream">
          <Switch :id="'sync-auto-'+row.stream" v-model:checked="row.edit.auto" aria-label="自动拉取" :disabled="row.saving"/>
        </a-form-item>
        <a-form-item label="拉取间隔（分钟）" :html-for="'sync-interval-'+row.stream">
          <a-input-number :id="'sync-interval-'+row.stream" v-model:value="row.edit.intervalMinutes" :min="1" :max="10080"
            :precision="0" :disabled="row.saving || !row.edit.auto"/>
        </a-form-item>
        <a-form-item v-if="!jobFor(row)?.initialFrom" class="sync-date" label="首次起点（北京时间）" :html-for="'sync-from-'+row.stream">
          <a-input :id="'sync-from-'+row.stream" v-model:value="row.edit.fromDate" type="date" :max="today" :disabled="row.saving"/>
        </a-form-item>
        <div v-else class="sync-start-date">
          <div>历史起点</div>
          <span>{{ dateFormat.format(new Date(jobFor(row).initialFrom)) }}</span>
        </div>
        <div class="sync-save">
          <a-button html-type="submit" type="primary" :loading="row.saving"
            :disabled="row.saving || !changed(row) || !row.edit.intervalMinutes">保存配置</a-button>
          <span v-if="changed(row) && !row.saving" class="sync-muted">有未保存的修改</span>
        </div>
        <p v-if="row.feedback && (row.feedback.error || !changed(row))" class="sync-feedback" :class="{'sync-error':row.feedback.error}" role="status">{{ row.feedback.text }}</p>
      </a-form>
    </section>
    <section v-if="!loading || products.length" class="records sync-settings" aria-labelledby="sync-periods-title">
      <div class="records-head sync-head">
        <h2 id="sync-periods-title">大春之道 · 核算期次</h2>
        <a-button type="link" :href="productId ? '#periods?productId='+productId : '#periods'">查看核算期次</a-button>
      </div>
      <p v-if="sourceProductsError" class="sync-state sync-error" role="status">{{ sourceProductsError }}</p>
      <p v-else-if="!products.length" class="sync-state">暂无已映射的大春之道实战班产品。</p>
      <template v-else>
        <a-form :model="{productId}" class="sync-form" layout="vertical" @finish="pullPeriods">
          <a-form-item class="sync-period-product" label="实战班产品" html-for="sync-period-product">
            <a-select id="sync-period-product" :value="productId" :options="productOptions" :virtual="false" placeholder="选择产品"
              :disabled="pullingPeriods" @change="selectProduct"/>
          </a-form-item>
          <a-button html-type="submit" :loading="pullingPeriods" :disabled="!dczd?.configured || !productId">拉取期次</a-button>
        </a-form>
        <p v-if="dczd && !dczd.configured" class="sync-state">来源尚未接入</p>
        <p v-if="periodError" class="sync-state sync-error" role="status">{{ periodError }}</p>
        <p v-if="periodResult" class="sync-state" role="status">
          新增 {{ periodResult.added }} 期，已有 {{ periodResult.existing }} 期<span v-if="periodResult.skipped">，跳过 {{ periodResult.skipped }} 期</span>
        </p>
      </template>
    </section>
  </a-spin>
</template>

<style scoped>
.sync-settings { margin-bottom: 14px; }
.sync-head { border-bottom: 1px solid var(--line); }
.sync-head h2 { white-space: normal; }
.sync-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 5px 8px; min-width: 0; }
.sync-heading :deep(.ant-tag) { margin: 0; }
.sync-head > button, .sync-head > a { flex-shrink: 0; }
.sync-form { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 10px 18px; padding: 12px 14px; }
.sync-form :deep(.ant-form-item) { margin: 0; }
.sync-save { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.sync-date { width: 160px; }
.sync-period-product { width: min(100%, 240px); }
.sync-start-date { display: grid; gap: 6px; padding-bottom: 6px; }
.sync-start-date span, .sync-muted { color: var(--muted); }
.sync-state { display: flex; flex-wrap: wrap; gap: 4px 14px; padding: 9px 14px 0; margin: 0; color: var(--muted); }
p.sync-state { padding-bottom: 12px; }
.sync-feedback { flex-basis: 100%; margin: 0; }
.sync-error { color: #a23232; font-weight: 500; }
@media (max-width: 560px) {
  .sync-head { flex-wrap: nowrap; }
  .sync-head > button, .sync-head > a { min-height: 44px; }
  .sync-order-form { display: grid; grid-template-columns: 1fr 1fr; }
  .sync-order-form :deep(.ant-input), .sync-order-form :deep(.ant-input-number) { width: 100%; }
  .sync-date { width: auto; }
  .sync-feedback { grid-column: 1 / -1; }
  .sync-form :deep(.ant-btn) { min-height: 44px; }
}
</style>

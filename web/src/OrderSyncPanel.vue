<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({api:Function, streams:Array, formatTime:Function, target:Object})
const emit = defineEmits(['updated'])
const rows = ref([])
const selected = ref()
const form = ref({fromDate:''})
const submitting = ref(false)
const formError = ref('')
const loadError = ref('')
let readSequence = 0
let timer
let closed = false

const busy = row => row.status === 'QUEUED' || row.status === 'RUNNING'
const label = stream => props.streams.find(item => item.value === stream)?.label || stream
const visibleRows = computed(() => rows.value.filter(row => props.streams.some(option => option.value === row.stream))
  .sort((a,b) => Number(b.stream === props.target?.stream) - Number(a.stream === props.target?.stream)))
const badge = row => {
  if (!row.sourceMatches) return ['来源连接待核对', 'orange']
  if (row.unverifiedSource) return ['存在未绑定的订单数据', 'orange']
  if (row.status === 'QUEUED') return ['已排队', 'blue']
  if (row.status === 'RUNNING') return ['同步中', 'blue']
  if (row.lastSyncFailed || row.status === 'FAILED') return ['同步失败', 'red']
  if (row.targetThrough) return ['同步未完成', 'orange']
  return row.completedThrough ? ['已同步', 'green'] : ['尚未同步', 'default']
}

async function reload() {
  clearTimeout(timer)
  const reading = ++readSequence
  try {
    const updated = await props.api('/orders/sync')
    if (closed || reading !== readSequence) return
    const finished = updated.some(row => row.completedThrough && rows.value.some(previous =>
      previous.stream === row.stream && (row.completedThrough !== previous.completedThrough ||
        row.status === 'SUCCEEDED' && (row.taskId !== previous.taskId || busy(previous)))))
    rows.value = updated
    loadError.value = ''
    if (finished) emit('updated')
  } catch (err) {
    if (!closed && reading === readSequence) loadError.value = err.message
  } finally {
    if (!closed && reading === readSequence && document.visibilityState !== 'hidden') {
      timer = setTimeout(reload, rows.value.some(busy) ? 1500 : 15000)
    }
  }
}

function open(row) {
  selected.value = row
  const suggestedDate = row.stream === props.target?.stream ? props.target.fromDate : ''
  const suggestedStart = Date.parse(suggestedDate+'T00:00:00+08:00')
  const needsHistory = suggestedStart <= Date.now() && (!row.initialFrom || suggestedStart < Date.parse(row.initialFrom))
  const retryDate = (!row.initialFrom || row.status === 'FAILED') ? row.requestedFromDate : ''
  form.value.fromDate = row.targetThrough ? '' : (needsHistory ? suggestedDate : retryDate) || ''
  formError.value = ''
}

async function submit() {
  if (submitting.value || !selected.value) return
  submitting.value = true
  formError.value = ''
  const stream = selected.value.stream
  try {
    const job = await props.api('/orders/sync', {method:'POST', body:JSON.stringify({stream, fromDate:form.value.fromDate})})
    if (closed) return
    rows.value = rows.value.map(row => row.stream === stream ? {...row, status:job.status, taskId:job.id, message:''} : row)
    selected.value = undefined
    await reload()
  } catch (err) {
    if (!closed) formError.value = err.message
  } finally {
    submitting.value = false
  }
}

function onVisibilityChange() {
  if (document.visibilityState === 'hidden') clearTimeout(timer)
  else reload()
}
onMounted(() => { reload(); document.addEventListener('visibilitychange', onVisibilityChange) })
onBeforeUnmount(() => { closed = true; clearTimeout(timer); document.removeEventListener('visibilitychange', onVisibilityChange) })
defineExpose({reload})
</script>

<template>
  <section class="records order-sync" aria-label="订单同步">
    <div class="records-head"><h2>订单同步</h2></div>
    <a-alert v-if="loadError" type="error" :message="loadError" show-icon/>
    <ul v-if="visibleRows.length" class="sync-streams">
      <li v-for="row in visibleRows" :key="row.stream" :class="{'sync-target':row.stream===target?.stream}">
        <div class="sync-detail">
          <strong>{{ label(row.stream) }}</strong>
          <a-tag :color="badge(row)[1]" aria-live="polite">{{ badge(row)[0] }}</a-tag>
          <small v-if="row.completedThrough">同步至 {{ formatTime(row.completedThrough) }}</small>
          <small v-if="row.targetThrough && row.scannedOrders">已读取 {{ row.scannedOrders }} {{ row.source==='DCZD' ? '条业务记录' : '笔订单' }}</small>
          <small v-if="row.status === 'SUCCEEDED' && row.sourceMatches">{{ row.newVersions ? '新增 '+row.newVersions+' 个版本' : '订单版本无变化' }}</small>
          <small v-if="row.message" class="sync-message">{{ row.message }}</small>
        </div>
        <a-button :disabled="busy(row) || !row.sourceMatches || row.unverifiedSource" @click="open(row)">
          {{ row.targetThrough || row.status === 'FAILED' || row.lastSyncFailed ? '继续同步' : '同步订单' }}
        </a-button>
      </li>
    </ul>
  </section>
  <a-drawer :open="!!selected" title="同步订单" :width="'min(440px, 100vw)'" @close="!submitting && (selected = undefined)">
    <div v-if="selected" role="dialog" aria-label="同步订单">
      <a-alert v-if="formError" class="feedback" type="error" :message="formError" show-icon/>
      <a-form :model="form" layout="vertical" @finish="submit">
        <p class="form-subhead">{{ label(selected.stream) }}</p>
        <a-form-item v-if="!selected.targetThrough" name="fromDate" label="历史起点" html-for="sync-date" :required="!selected.initialFrom"
          :extra="selected.initialFrom ? '填写更早日期可补拉历史。留空按已有进度同步。' : selected.source==='DCZD' ? '从北京时间当天零点起，读取进入来源接口的业务记录。' : '按北京时间从当天零点开始读取。'">
          <a-input type="date" id="sync-date" v-model:value="form.fromDate" :disabled="submitting"/>
        </a-form-item>
        <p v-else>继续读取上次同步范围。</p>
        <div class="form-actions"><a-button type="primary" html-type="submit" :disabled="!selected.initialFrom && !form.fromDate" :loading="submitting">开始同步</a-button></div>
      </a-form>
    </div>
  </a-drawer>
</template>

<style scoped>
.order-sync { margin-bottom: 16px; }
.sync-streams { list-style: none; margin: 0; padding: 0; }
.sync-streams li { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 15px; border-top: 1px solid var(--line); }
.sync-streams li.sync-target { background: var(--accent-soft); }
.sync-detail { display: flex; align-items: center; flex-wrap: wrap; gap: 5px 9px; min-width: 0; }
.sync-detail small { color: var(--muted); }
.sync-detail .sync-message { flex-basis: 100%; color: #a23232; }
</style>

<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'

const props = defineProps({draft:Object, period:Object, api:Function, formatMoney:Function, formatTime:Function, sourceStreams:Array})
const emit = defineEmits(['show-pools', 'show-issues', 'confirmed'])
const review = ref()
const loading = ref(false)
const error = ref('')
const confirmation = ref()
const confirming = ref(false)
const confirmationError = ref('')
let sequence = 0
const coverageStatus = {
  UNSUPPORTED:'该订单来源待接入',
  NOT_CONFIGURED:'来源接口未配置',
  NOT_STARTED:'尚未开始同步',
  SOURCE_CHANGED:'来源连接已变化，请核对同步配置',
  FAILED:'同步失败，请继续同步',
  INCOMPLETE:'本轮同步未完成',
  OUTSIDE_RANGE:'同步数据尚未覆盖本账期',
  STALE:'同步时间已超过关账时效',
  CLOCK_GAP:'来源水位晚于同步完成时间',
  READY:'已覆盖本账期',
}
const streamLabel = stream => props.sourceStreams?.find(option => option.value === stream)?.label || '订单来源'

async function load() {
  const request = ++sequence
  const params = new URLSearchParams({periodId:props.draft.periodId, version:props.draft.version})
  loading.value = true
  confirmation.value = undefined
  review.value = undefined
  error.value = ''
  try {
    const result = await props.api('/settlement-review?' + params)
    if (request === sequence) review.value = result
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}
function confirmReview() {
  if (!review.value?.canConfirm || confirming.value) return
  confirmationError.value = ''
  confirmation.value = {
    periodId:props.draft.periodId,
    expectedVersion:review.value.version,
    reviewToken:review.value.reviewToken,
  }
}
async function closePeriod() {
  const request = sequence
  const input = confirmation.value
  if (!input || confirming.value) return
  confirming.value = true
  confirmationError.value = ''
  try {
    const result = await props.api('/settlements',{method:'POST',body:JSON.stringify(input)})
    if (request !== sequence) return
    confirmation.value = undefined
    review.value = {...review.value,settlement:result,canConfirm:false}
    emit('confirmed')
  } catch (err) {
    if (request === sequence) confirmationError.value = err.message
  } finally {
    confirming.value = false
  }
}
watch(() => props.draft, load, {immediate:true})
onBeforeUnmount(() => { sequence++ })
</script>

<template>
  <section class="records">
    <div class="records-head">
      <h2>结算核对</h2>
      <div class="topbar-actions">
        <a-button :loading="loading" :disabled="confirming" @click="load">刷新核对</a-button>
        <a-button v-if="review?.canConfirm" type="primary" :disabled="confirming" @click="confirmReview">确认结算</a-button>
      </div>
    </div>
    <div class="settlement-review">
      <a-alert v-if="error" type="error" :message="error" show-icon/>
      <span v-else-if="loading" role="status">正在读取结算核对</span>
      <template v-else-if="review?.settlement">
        <a-alert type="success" :message="'已结算 · ' + formatTime(review.settlement.confirmedAt)" show-icon/>
      </template>
      <template v-else-if="review">
        <a-alert v-if="!review.enabled" type="info" message="该产品尚未开放结算" show-icon/>
        <a-alert v-if="!review.ended" type="info" message="账期尚未结束" show-icon/>
        <a-alert v-if="review.draftStale" type="warning" message="试算数据已变化，请重新试算并核对" show-icon/>
        <a-alert v-if="review.laterPoolActivity" type="warning" message="后续账期已结算此团队池，请核对跨期处理" show-icon/>
        <a-alert v-if="review.practiceDependencies?.length" type="warning" message="本期涉及以下待结算班次或退款账期" show-icon>
          <template #description>
            <ul>
              <li v-for="dependency in review.practiceDependencies" :key="dependency.kind + '/' + dependency.periodId + '/' + dependency.name">{{ dependency.name }}</li>
            </ul>
          </template>
        </a-alert>
        <ul v-if="review.sources?.length" class="review-pools" aria-label="来源同步">
          <li v-for="source in review.sources" :key="source.stream">
            <span>{{ streamLabel(source.stream) }} · {{ coverageStatus[source.status] }}</span>
            <span v-if="source.completedThrough">同步至 {{ formatTime(source.completedThrough) }}</span>
            <span v-if="source.lastSuccessAt">同步完成于 {{ formatTime(source.lastSuccessAt) }}</span>
            <span v-if="source.status==='OUTSIDE_RANGE' && source.requiredFrom">需覆盖 {{ formatTime(source.requiredFrom) }} 至 {{ formatTime(source.requiredThrough) }}</span>
            <span v-if="source.refundGap">{{ source.refundGap }} 笔退款缺少金额或时间</span>
          </li>
        </ul>
        <div v-if="review.issueCount">
          <a-button type="link" @click="emit('show-issues')">查看 {{ review.issueCount }} 条待核项</a-button>
        </div>
        <template v-if="review.pools.length">
          <ul class="review-pools">
            <li v-for="item in review.pools" :key="JSON.stringify([item.unitId,item.scenario,item.itemKey])">
              <span>{{ item.name }}</span>
              <span v-if="item.stale">分配需重新核对</span>
              <span v-if="item.unallocatedMinor > 0">待分配 {{ formatMoney(item.unallocatedMinor) }}</span>
            </li>
          </ul>
          <div><a-button type="link" @click="emit('show-pools')">查看团队池分配</a-button></div>
        </template>
        <a-alert v-else-if="!review.laterPoolActivity && !review.draftStale" type="success"
          :message="draft.practiceRefundMonth ? '团队池流水已核对' : '团队池已对平'" show-icon/>
      </template>
    </div>
  </section>
  <Modal :open="!!confirmation" title="确认本期结算" :confirm-loading="confirming" :closable="!confirming"
    :mask-closable="!confirming" :keyboard="!confirming" ok-text="确认结算" cancel-text="返回核对"
    :cancel-button-props="{disabled:confirming}" @ok="closePeriod" @cancel="confirmation=undefined">
    <p>{{ period?.productName }} · {{ period?.name }}</p>
    <p>草稿 v{{ confirmation?.expectedVersion }} · 合计 <strong>{{ formatMoney(review?.totalMinor) }}</strong></p>
    <p>确认后锁定本期结算与分配结果。</p>
    <a-alert v-if="confirmationError" :message="confirmationError" type="error" show-icon/>
  </Modal>
</template>

<style scoped>
.settlement-review { display: grid; gap: 12px; padding: 16px; }
.review-pools { margin: 0; padding: 0; border: 1px solid #e1e6ed; border-radius: 6px; list-style: none; }
.review-pools li { display: flex; flex-wrap: wrap; gap: 8px; padding: 10px 12px; }
.review-pools li + li { border-top: 1px solid #e1e6ed; }
.review-pools li > :first-child { flex: 1 1 180px; overflow-wrap: anywhere; }
</style>

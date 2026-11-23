<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'

const props = defineProps({draft:Object, period:Object, api:Function, formatMoney:Function, formatTime:Function, sourceStreams:Array,
  working:Boolean, canPullRoster:Boolean, relatedPeriodLink:Function})
const emit = defineEmits(['show-pools', 'show-issues', 'recalculate', 'pull-roster', 'refresh-draft', 'confirmed'])
const review = ref()
const loading = ref(false)
const error = ref('')
const reviewChanged = ref(false)
const confirmation = ref()
const confirming = ref(false)
const confirmationError = ref('')
const confirmationChanged = ref(false)
let sequence = 0
const coverageStatus = {
  UNSUPPORTED:'该订单来源待接入，请联系维护人员',
  NOT_CONFIGURED:'来源接口未配置，请联系维护人员检查连接',
  NOT_STARTED:'尚未开始同步',
  SOURCE_CHANGED:'来源连接已变化，请核对同步配置',
  FAILED:'同步失败，请继续同步',
  INCOMPLETE:'本轮同步未完成',
  OUTSIDE_RANGE:'同步数据尚未覆盖本账期',
  STALE:'同步时间已超过关账时效',
  CLOCK_GAP:'来源水位晚于同步完成时间，请核对来源时钟',
  READY:'已覆盖本账期',
}
const streamLabel = stream => props.sourceStreams?.find(option => option.value === stream)?.label || '订单来源'
const rosterStatus = {
  NOT_CONFIGURED:'签到名单接口未配置，请联系维护人员检查连接',
  ROUTE_MISSING:'签到名单读取接口未接通，请核对大春之道的接口部署或网关路由',
  ACCESS_DENIED:'签到名单读取未通过授权，请核对大春之道服务凭证和权限',
  UNAVAILABLE:'暂时无法核验大春之道的签到名单',
  UNCONFIRMED:'签到名单尚未确认或已有变更，请在大春之道确认后重新拉取',
  PAYMENT_CONFLICT:'报名付款同时出现在多个期次，请在大春之道核对延期',
  NEEDS_PULL:'签到名单版本已变化，请重新拉取并试算',
  NEEDS_DRAFT:'已拉取新版签到名单，请重新试算',
  READY:'签到名单已核对',
}
const resumableSources = new Set(['NOT_STARTED', 'FAILED', 'INCOMPLETE', 'OUTSIDE_RANGE', 'STALE'])
const sourcesReady = computed(() => review.value?.sources?.every(source => source.status==='READY' && !source.refundGap))
const prerequisitesReady = computed(() => review.value?.enabled && review.value?.ended && sourcesReady.value &&
  !review.value.draftStale && !review.value.issueCount && !review.value.pools.length && !review.value.laterPoolActivity &&
  !review.value.practiceDependencies?.length && (!review.value.practiceRoster || review.value.practiceRoster.sourceStatus==='READY'))
const settlementStatus = computed(() => {
  if (review.value?.settlement) return {label:'已结算', tone:'success'}
  if (review.value?.canConfirm) return {label:'可以确认结算', tone:'success'}
  if (prerequisitesReady.value && !review.value?.mayConfirm) return {label:'当前账号无确认结算权限', tone:'neutral'}
  return {label:'还有事项待处理', tone:'pending'}
})
function orderSyncLink(source) {
  const params = new URLSearchParams({stream:source.stream,returnPeriodId:props.draft.periodId})
  if (source.requiredFrom && (!source.initialFrom || Date.parse(source.requiredFrom) < Date.parse(source.initialFrom))) {
    const date = new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Shanghai',year:'numeric',month:'2-digit',day:'2-digit'})
    params.set('fromDate',date.format(new Date(source.requiredFrom)))
  }
  return '#orders?' + params
}

async function load() {
  const request = ++sequence
  const params = new URLSearchParams({periodId:props.draft.periodId, version:props.draft.version})
  loading.value = true
  confirmation.value = undefined
  review.value = undefined
  error.value = ''
  reviewChanged.value = false
  try {
    const result = await props.api('/settlement-review?' + params)
    if (request === sequence) review.value = result
  } catch (err) {
    if (request === sequence) {
      error.value = err.message
      reviewChanged.value = err.status === 409
    }
  } finally {
    if (request === sequence) loading.value = false
  }
}
function confirmReview() {
  if (!review.value?.canConfirm || confirming.value) return
  confirmationError.value = ''
  confirmationChanged.value = false
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
    if (request === sequence) {
      confirmationError.value = err.message
      confirmationChanged.value = err.status === 409
    }
  } finally {
    confirming.value = false
  }
}
function refreshAfterConflict() {
  confirmation.value = undefined
  emit('refresh-draft')
}
watch(() => props.draft, load, {immediate:true})
onBeforeUnmount(() => { sequence++ })
</script>

<template>
  <section class="records">
    <div class="records-head">
      <h2>结算核对</h2>
      <div class="topbar-actions">
        <a-button :loading="loading" :disabled="confirming || working" @click="load">刷新核对</a-button>
        <a-button v-if="review?.canConfirm" type="primary" :disabled="confirming || working" @click="confirmReview">确认结算</a-button>
      </div>
    </div>
    <div class="settlement-review">
      <a-alert v-if="error" type="error" :message="error" show-icon>
        <template v-if="reviewChanged" #action><a-button size="small" @click="refreshAfterConflict">读取最新结果</a-button></template>
      </a-alert>
      <span v-else-if="loading" role="status">正在读取结算核对</span>
      <template v-else-if="review">
        <div class="settlement-summary" :data-tone="settlementStatus.tone">
          <div role="status">
            <strong>{{ settlementStatus.label }}</strong>
            <p>{{ period?.productName }} · {{ period?.name }} · {{ review.settlement ? '结算' : '草稿' }} v{{ review.version }}
              <template v-if="review.settlement"> · 确认于 {{ formatTime(review.settlement.confirmedAt) }}</template>
            </p>
          </div>
          <div class="settlement-amount"><small>全期净分润</small><b>{{ formatMoney(review.totalMinor) }}</b></div>
        </div>
        <template v-if="!review.settlement">
          <p v-if="!review.enabled || !review.ended" class="review-availability">
            <span v-if="!review.enabled">该产品尚未开放结算。</span>
            <span v-if="!review.ended">{{ review.practiceRoster ? '班次尚未结束。' : '账期尚未结束。' }}</span>
          </p>
          <div class="settlement-checklist">
            <section v-if="review.sources?.length" class="settlement-check">
              <div class="settlement-check-head"><h3>订单同步</h3><a-tag :color="sourcesReady ? 'green' : 'orange'">{{ sourcesReady ? '已核对' : '待处理' }}</a-tag></div>
              <ul class="review-list">
                <li v-for="source in review.sources" :key="source.stream">
                  <div>
                    <p>{{ streamLabel(source.stream) }} · {{ coverageStatus[source.status] || '待核对' }}</p>
                    <small v-if="source.completedThrough">同步至 {{ formatTime(source.completedThrough) }}</small>
                    <small v-if="source.lastSuccessAt">同步完成于 {{ formatTime(source.lastSuccessAt) }}</small>
                    <small v-if="source.status==='OUTSIDE_RANGE' && source.requiredFrom">需覆盖 {{ formatTime(source.requiredFrom) }} 至 {{ formatTime(source.requiredThrough) }}</small>
                    <small v-if="source.refundGap">{{ source.refundGap }} 笔退款缺少金额或时间，请在子系统更正后重新拉取。</small>
                  </div>
                  <a v-if="source.stream && resumableSources.has(source.status)" :href="orderSyncLink(source)">去订单同步</a>
                </li>
              </ul>
            </section>
            <section v-if="review.practiceRoster" class="settlement-check">
              <div class="settlement-check-head"><h3>签到名单</h3>
                <a-tag :color="review.practiceRoster.sourceStatus==='READY' ? 'green' : 'orange'">{{ review.practiceRoster.sourceStatus==='READY' ? '已核对' : '待处理' }}</a-tag>
              </div>
              <p v-if="review.practiceRoster.sourceStatus!=='READY'">{{ rosterStatus[review.practiceRoster.sourceStatus] || rosterStatus.NOT_CONFIGURED }}</p>
              <p class="review-meta">
                试算名单 v{{ review.practiceRoster.version }} · 到课 {{ review.practiceRoster.attendance }} 人
                <template v-if="review.practiceRoster.latestVersion !== review.practiceRoster.version"> · 已导入 v{{ review.practiceRoster.latestVersion }}</template>
                <template v-if="review.practiceRoster.sourceVersion && review.practiceRoster.sourceVersion !== review.practiceRoster.latestVersion"> · 来源 v{{ review.practiceRoster.sourceVersion }}</template>
                · 开班 {{ formatTime(review.practiceRoster.startsAt) }} · 名单确认 {{ formatTime(review.practiceRoster.confirmedAt) }}
                <template v-if="review.practiceRoster.refundCutoff !== review.practiceRoster.confirmedAt"> · 班次退款截至 {{ formatTime(review.practiceRoster.refundCutoff) }}</template>
              </p>
              <a-button v-if="canPullRoster && review.practiceRoster.sourceStatus==='NEEDS_PULL'" size="small" :disabled="working" @click="emit('pull-roster')">拉取签到名单</a-button>
            </section>
            <section v-if="review.practiceDependencies?.length" class="settlement-check">
              <div class="settlement-check-head"><h3>关联班次与退款账期</h3><a-tag color="orange">待处理</a-tag></div>
              <ul class="review-list">
                <li v-for="dependency in review.practiceDependencies" :key="dependency.kind + '/' + dependency.periodId + '/' + dependency.name">
                  <span>{{ dependency.name }}</span>
                  <a :href="relatedPeriodLink(dependency.periodId)">{{ dependency.periodId ? '打开核算期' : '查看核算期次' }}</a>
                </li>
              </ul>
            </section>
            <section class="settlement-check">
              <div class="settlement-check-head"><h3>草稿与待核项</h3>
                <a-tag :color="review.draftStale || review.issueCount ? 'orange' : 'green'">{{ review.draftStale || review.issueCount ? '待处理' : '已核对' }}</a-tag>
              </div>
              <p v-if="review.draftStale">试算数据已变化，请重新试算并核对。</p>
              <p v-if="review.issueCount">草稿仍有 {{ review.issueCount }} 条待核项。</p>
              <p v-if="!review.draftStale && !review.issueCount">草稿 v{{ review.version }} · 待核项 0 条</p>
              <div v-if="review.draftStale || review.issueCount" class="review-actions">
                <a-button v-if="review.issueCount" size="small" @click="emit('show-issues')">查看 {{ review.issueCount }} 条待核项</a-button>
                <a-button v-if="review.draftStale" size="small" :disabled="working || period?.classStartOutsidePeriod" @click="emit('recalculate')">重新试算</a-button>
              </div>
            </section>
            <section v-if="draft.poolAllocationAvailable || review.pools.length || review.laterPoolActivity" class="settlement-check">
              <div class="settlement-check-head"><h3>团队池分配</h3>
                <a-tag :color="review.pools.length || review.laterPoolActivity ? 'orange' : 'green'">{{ review.pools.length || review.laterPoolActivity ? '待处理' : '已核对' }}</a-tag>
              </div>
              <p v-if="review.laterPoolActivity">后续账期已结算此团队池，请核对跨期处理。</p>
              <ul v-if="review.pools.length" class="review-list">
                <li v-for="item in review.pools" :key="JSON.stringify([item.unitId,item.scenario,item.itemKey])">
                  <span>{{ item.name }}</span>
                  <small v-if="item.stale">分配需重新核对</small>
                  <span v-if="item.unallocatedMinor > 0">待分配 {{ formatMoney(item.unallocatedMinor) }}</span>
                </li>
              </ul>
              <a-button v-if="draft.poolAllocationAvailable" size="small" @click="emit('show-pools')">查看团队池分配</a-button>
            </section>
          </div>
        </template>
      </template>
    </div>
  </section>
  <Modal :open="!!confirmation" title="确认本期结算" :confirm-loading="confirming" :closable="!confirming"
    :mask-closable="!confirming" :keyboard="!confirming" ok-text="确认结算" cancel-text="返回核对"
    :ok-button-props="{disabled:confirmationChanged}" :cancel-button-props="{disabled:confirming}" @ok="closePeriod" @cancel="confirmation=undefined">
    <p>{{ period?.productName }} · {{ period?.name }}</p>
    <p>草稿 v{{ confirmation?.expectedVersion }} · 合计 <strong>{{ formatMoney(review?.totalMinor) }}</strong></p>
    <p v-if="review?.practiceRoster">签到名单 v{{ review.practiceRoster.version }} · 首次确认 {{ formatTime(review.practiceRoster.refundCutoff) }}</p>
    <p>确认后锁定本期结算与分配结果。</p>
    <a-alert v-if="confirmationError" :message="confirmationError" type="error" show-icon>
      <template v-if="confirmationChanged" #action><a-button size="small" @click="refreshAfterConflict">读取最新结果</a-button></template>
    </a-alert>
  </Modal>
</template>

<style scoped>
.settlement-review { display: grid; gap: 12px; padding: 0 14px 14px; }
.settlement-summary { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px 20px; padding: 12px 14px; background: var(--accent-soft); border: 1px solid #dce6f7; border-radius: 6px; }
.settlement-summary[data-tone='pending'] { background: #fff9eb; border-color: #f4dfac; }
.settlement-summary[data-tone='success'] { background: #f0f9f4; border-color: #d1e9da; }
.settlement-summary strong { font-size: 16px; }
.settlement-summary p { margin: 4px 0 0; color: var(--muted); font-size: 13px; }
.settlement-amount small { display: block; color: var(--muted); }
.settlement-amount b { font-size: 20px; font-variant-numeric: tabular-nums; }
.review-availability { margin: 0; color: #765310; }
.settlement-checklist { border: 1px solid var(--line); border-radius: 6px; }
.settlement-check { padding: 11px 13px; }
.settlement-check + .settlement-check { border-top: 1px solid var(--line); }
.settlement-check-head { display: flex; align-items: center; gap: 7px; margin-bottom: 6px; }
.settlement-check h3 { margin: 0; font-size: 14px; }
.settlement-check p { margin: 0 0 5px; overflow-wrap: anywhere; }
.review-meta, .review-list small { color: var(--muted); font-size: 12px; }
.review-list { margin: 0; padding: 0; list-style: none; }
.review-list li { display: flex; align-items: center; justify-content: space-between; gap: 6px 12px; flex-wrap: wrap; padding: 4px 0; overflow-wrap: anywhere; }
.review-list small + small { margin-left: 8px; }
.review-list a { white-space: nowrap; }
.review-actions { display: flex; flex-wrap: wrap; gap: 7px; }
</style>

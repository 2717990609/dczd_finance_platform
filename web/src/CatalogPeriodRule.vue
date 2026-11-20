<script setup>
import { computed, ref, watch } from 'vue'

const props = defineProps({rule:Object, api:Function, formatMoney:Function, formatTime:Function, optionLabel:Function, canReviewActivity:Boolean})
const emit = defineEmits(['close','published'])
const sheet = ref()
const rows = ref([])
const effectiveDate = ref('')
const amounts = ref({receipts:0,refunds:0,carry:0})
const loading = ref(false)
const saving = ref(false)
const previewing = ref(false)
const error = ref('')
const quote = ref()
const activityMonth = ref(new Date(Date.now()+8*3600000).toISOString().slice(0,7))
const reviewingActivity = ref(false)
const activity = ref()
const activityError = ref('')
let versionRequest = 0
let quoteRequest = 0
let activityRequest = 0

const cycleLabels = {MONTHLY:'自然月',MONTHLY_25_TO_24:'每月 25 日至次月 24 日'}
const activityIssueLabels = {
  REFUND_ADJUSTMENT:'退款更正待核对', INVALID_EVENT:'交易编号或金额缺失', DUPLICATE_EVENT:'交易编号重复',
  REFUND_ORIGIN_MISSING:'退款未能唯一关联原付款', UNCLASSIFIED:'原付款未分类', MAPPING_MISSING:'缺少场景映射',
  MAPPING_CONFLICT:'场景映射冲突', INVALID_PAYMENT:'原付款信息不完整', REFUND_EXCEEDS_PAYMENT:'累计退款超过原付款',
}
const sourceStatusLabels = {READY:'覆盖本期',NOT_CONFIGURED:'来源未配置',NOT_STARTED:'未同步',INCOMPLETE:'同步未完成',
  OUTSIDE_RANGE:'未覆盖本期',STALE:'同步已过期',SOURCE_CHANGED:'来源已变更',FAILED:'同步失败',CLOCK_GAP:'来源时钟待核',UNSUPPORTED:'来源待核'}
const localDate = stamp => new Date(Date.parse(stamp) + 8*3600000).toISOString().slice(0,10)
const firstStart = computed(() => {
  if (!sheet.value) return undefined
  const from = new Date(Date.parse(sheet.value.effectiveFrom)+8*3600000)
  const day = sheet.value.cycle==='MONTHLY' ? 1 : 25
  let start = new Date(Date.UTC(from.getUTCFullYear(),from.getUTCMonth(),day))
  if (start<=from) start = new Date(Date.UTC(from.getUTCFullYear(),from.getUTCMonth()+1,day))
  return start.toISOString().slice(0,10)
})
const cutoff = computed(() => sheet.value?.effectiveTo
  ? localDate(new Date(Date.parse(sheet.value.effectiveTo)-86400000).toISOString()) : undefined)
const canSave = computed(() => sheet.value?.mayPublish && (!cutoff.value || firstStart.value<=cutoff.value))
const changed = computed(() => JSON.stringify(rows.value) !== JSON.stringify(editableRows(sheet.value?.items || [])))

function editableRows(items) {
  return items.map(item => ({itemKey:item.itemKey,tiers:item.tiers.map(row => ({
    from:(row.min_inclusive || 0)/100,rate:row.rate_bps/100,
  }))}))
}
function minor(value, message) {
  if (value == null || !Number.isSafeInteger(Math.round(value*100)) || value < 0) throw new Error(message)
  return Math.round(value*100)
}
function editedTiers() {
  if (!canSave.value || !changed.value) return []
  return rows.value.map(item=>{
    const starts = item.tiers.map(row=>minor(row.from,'请输入每档净额起点。'))
    if (starts.some((from,index)=>index && from<=starts[index-1])) throw new Error('每一档净额起点须高于上一档。')
    return {itemKey:item.itemKey,tiers:item.tiers.map((row,index)=>{
      const rate = minor(row.rate,'请输入每档比例。')
      if (rate>10000) throw new Error('比例须在 0% 到 100% 之间。')
      return {min_inclusive:starts[index],max_exclusive:starts[index+1],rate_bps:rate}
    })}
  })
}
function removeTier(tiers, index) {
  tiers.splice(index,1)
  tiers[0].from = 0
}

watch(()=>props.rule.id, async id=>{
  const request = ++versionRequest
  error.value = ''
  sheet.value = undefined
  loading.value = true
  try {
    const data = await props.api('/catalog-period-rules?ruleId='+encodeURIComponent(id))
    if (request !== versionRequest) return
    sheet.value = data
    rows.value = editableRows(data.items)
    effectiveDate.value = canSave.value ? firstStart.value : ''
  } catch (err) {
    if (request === versionRequest) error.value = err.message
  } finally {
    if (request === versionRequest) loading.value = false
  }
}, {immediate:true})
watch([rows,amounts], ()=>{
  ++quoteRequest
  previewing.value = false
  quote.value = undefined
}, {deep:true})
watch([activityMonth,()=>props.rule.id], ()=>{
  ++activityRequest
  activity.value = undefined
  activityError.value = ''
  reviewingActivity.value = false
})

async function readActivity() {
  const request = ++activityRequest
  reviewingActivity.value = true
  activityError.value = ''
  activity.value = undefined
  try {
    const query = new URLSearchParams({ruleId:props.rule.id,month:activityMonth.value})
    const result = await props.api('/catalog-period-activity?'+query)
    if (request === activityRequest) activity.value = result
  } catch (err) {
    if (request === activityRequest) activityError.value = err.message
  } finally {
    if (request === activityRequest) reviewingActivity.value = false
  }
}

async function preview() {
  const request = ++quoteRequest
  quote.value = undefined
  error.value = ''
  previewing.value = true
  try {
    const result = await props.api('/catalog-period-rules/preview',{method:'POST',body:JSON.stringify({
      baseId:props.rule.id,edits:editedTiers(),
      receiptsMinor:minor(amounts.value.receipts,'请输入本期实收。'),
      refundsMinor:minor(amounts.value.refunds,'请输入本期退款。'),
      carryInMinor:-minor(amounts.value.carry,'请输入上期未抵扣退款。'),
    })})
    if (request === quoteRequest) quote.value = result
  } catch (err) {
    if (request === quoteRequest) error.value = err.message
  } finally {
    if (request === quoteRequest) previewing.value = false
  }
}
async function publish() {
  if (!effectiveDate.value) {
    error.value = '请选择新版本的生效日期。'
    return
  }
  saving.value = true
  error.value = ''
  try {
    const result = await props.api('/catalog-period-rules',{method:'POST',body:JSON.stringify({
      baseId:props.rule.id,effectiveFrom:new Date(effectiveDate.value+'T00:00:00+08:00').toISOString(),
      edits:editedTiers(),
    })})
    emit('published',result)
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer open title="整期阶梯配置" width="min(760px, 100vw)"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <a-alert v-if="error" class="feedback" :message="error" type="error" show-icon/>
    <a-spin :spinning="loading">
      <section v-if="sheet">
        <h2>{{ sheet.label }} <a-tag>v{{ sheet.version }}</a-tag></h2>
        <a-alert v-if="sheet.paused" class="feedback" type="info" show-icon
          message="当前暂停执行。保存档位新版本后仍维持暂停。"/>
        <a-descriptions size="small" :column="1" bordered>
          <a-descriptions-item label="结算周期">{{ cycleLabels[sheet.cycle] }}</a-descriptions-item>
          <a-descriptions-item label="有效范围">{{ formatTime(sheet.effectiveFrom) }}<template v-if="sheet.effectiveTo"> 至 {{ formatTime(sheet.effectiveTo) }}</template></a-descriptions-item>
          <a-descriptions-item label="参与场景">
            <a-tag v-for="sku in sheet.scenarios" :key="sku.code">{{ sku.label }}</a-tag>
          </a-descriptions-item>
        </a-descriptions>
        <section v-for="(item,index) in sheet.items" :key="item.itemKey" class="period-tier-item">
          <h3 class="form-subhead">{{ item.label }}</h3>
          <p><strong>发给谁</strong> {{ item.targetKind==='pool' ? item.poolName : item.targetName || '规则指定公司' }}
            <span v-if="item.targetKind==='pool' && item.targetName"> · {{ item.targetName }}</span>
          </p>
          <p v-if="item.ownerRole" class="rule-formula">
            分配负责人按「{{ optionLabel('catalogAppointmentRole',item.ownerRole) }}」任职确定。
          </p>
          <p class="rule-formula">整期净收款选档，全部可计提净额乘以该档比例。</p>
          <div class="period-tier-grid period-tier-header" aria-hidden="true">
            <span>净额起点（元，含）</span><span>净额止点（元，不含）</span><span>计提比例（%）</span><span v-if="canSave">操作</span>
          </div>
          <div v-for="(tier,i) in rows[index].tiers" :key="i" class="period-tier-grid">
            <a-input-number v-model:value="tier.from" :aria-label="'第 '+(i+1)+' 档净额起点'"
              :min="0" :precision="2" :disabled="!canSave || saving || i===0"/>
            <span>{{ rows[index].tiers[i+1]?.from == null ? (i===rows[index].tiers.length-1 ? '不限' : '待填写') : formatMoney(Math.round(rows[index].tiers[i+1].from*100)) }}</span>
            <a-input-number v-model:value="tier.rate" :aria-label="'第 '+(i+1)+' 档计提比例'"
              :min="0" :max="100" :precision="2" :disabled="!canSave || saving"/>
            <a-button v-if="canSave" danger type="link" size="small" :disabled="saving || rows[index].tiers.length===1"
              @click="removeTier(rows[index].tiers,i)">删除</a-button>
          </div>
          <a-button v-if="canSave" type="dashed" :disabled="saving" @click="rows[index].tiers.push({from:undefined,rate:0})">新增档位</a-button>
        </section>
        <template v-if="canReviewActivity">
          <h3 class="form-subhead">订单和退款核对</h3>
          <p class="rule-formula">按当前规则的场景读取本地已同步数据。正式计提还要按原付款规则和团队池归属分组，并核对已确认结转。</p>
          <a-form layout="inline" class="period-activity-query">
            <a-form-item label="核对账期" html-for="period-activity-month">
              <a-input id="period-activity-month" v-model:value="activityMonth" type="month"/>
            </a-form-item>
            <a-form-item>
              <a-button :disabled="!activityMonth" :loading="reviewingActivity" @click="readActivity">读取已同步数据</a-button>
            </a-form-item>
          </a-form>
          <a-alert v-if="activityError" class="feedback" :message="activityError" type="error" show-icon/>
          <section v-if="activity" class="period-activity-result" aria-label="已同步数据核对结果">
            <p class="rule-formula">{{ formatTime(activity.start) }} 至 {{ formatTime(activity.through) }}（截止时刻不含）</p>
            <div class="period-activity-source">
              <a-tag v-for="source in activity.sources" :key="source.stream" :color="source.status==='READY'?'green':'orange'">
                {{ optionLabel('dcaiOrderStream',source.stream) || source.stream }} · {{ sourceStatusLabels[source.status] }}
              </a-tag>
              <a-tag v-if="!activity.sources.length" color="orange">缺少可核对的来源映射</a-tag>
            </div>
            <div class="period-activity-table">
              <table>
                <thead><tr><th>场景</th><th>已匹配收款</th><th>已匹配退款</th><th>其中跨期原付款退款</th></tr></thead>
                <tbody>
                  <tr v-for="scope in activity.scenarios" :key="scope.code">
                    <th scope="row">{{ sheet.scenarios.find(sku=>sku.code===scope.code)?.label || scope.code }}
                      <small v-if="!scope.mapped" class="order-version">本期缺少映射</small>
                    </th>
                    <td>
                      <template v-if="scope.mapped || scope.paymentCount">{{ formatMoney(scope.receiptsMinor) }} <small>· {{ scope.paymentCount }} 笔</small></template>
                      <template v-else>—</template>
                    </td>
                    <td>
                      <template v-if="scope.mapped || scope.refundCount">{{ formatMoney(scope.refundsMinor) }} <small>· {{ scope.refundCount }} 笔</small></template>
                      <template v-else>—</template>
                    </td>
                    <td>{{ scope.mapped || scope.refundCount ? formatMoney(scope.priorPeriodRefundsMinor) : '—' }}</td>
                  </tr>
                  <tr><th scope="row">已匹配合计</th><td>{{ formatMoney(activity.receiptsMinor) }}</td>
                    <td>{{ formatMoney(activity.refundsMinor) }}</td><td>{{ formatMoney(activity.priorPeriodRefundsMinor) }}</td></tr>
                </tbody>
              </table>
            </div>
            <ul v-if="Object.keys(activity.issues).length" class="period-activity-issues" aria-label="待核交易">
              <li v-for="(count,issue) in activity.issues" :key="issue">{{ activityIssueLabels[issue] || issue }} · {{ count }} 笔</li>
            </ul>
          </section>
        </template>
        <h3 class="form-subhead">金额演算</h3>
        <p class="rule-formula">手填所选场景的汇总金额，用于核对档位和计提金额。</p>
        <a-form layout="vertical">
          <div class="period-quote-inputs">
            <a-form-item label="本期实收（元）" html-for="period-quote-received">
              <a-input-number id="period-quote-received" v-model:value="amounts.receipts" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="本期退款（元）" html-for="period-quote-refunds">
              <a-input-number id="period-quote-refunds" v-model:value="amounts.refunds" :min="0" :precision="2"/>
            </a-form-item>
            <a-form-item label="上期未抵扣退款（元）" html-for="period-quote-carry">
              <a-input-number id="period-quote-carry" v-model:value="amounts.carry" :min="0" :precision="2"/>
            </a-form-item>
          </div>
          <a-button :loading="previewing" @click="preview">演算金额</a-button>
        </a-form>
        <a-descriptions v-if="quote" size="small" :column="1" bordered class="period-quote-result" aria-label="金额演算结果">
          <a-descriptions-item label="可计提净额">{{ formatMoney(quote.basisMinor) }}</a-descriptions-item>
          <a-descriptions-item v-for="award in quote.awards" :key="award.itemKey"
            :label="sheet.items.find(item=>item.itemKey===award.itemKey)?.label">{{ award.rateBps/100 }}% · {{ formatMoney(award.amountMinor) }}</a-descriptions-item>
          <a-descriptions-item label="下期结转退款">{{ formatMoney(-quote.carryForwardMinor) }}</a-descriptions-item>
        </a-descriptions>
        <a-form v-if="canSave" layout="vertical" class="period-quote-result">
          <a-form-item label="新版本生效日期（北京时间零点）" html-for="period-rule-effective" :extra="cycleLabels[sheet.cycle]+'的账期起点生效。'">
            <a-input id="period-rule-effective" v-model:value="effectiveDate" type="date" :min="firstStart" :max="cutoff" :disabled="saving"/>
          </a-form-item>
        </a-form>
      </section>
    </a-spin>
    <template #footer>
      <div class="workspace-edit-footer">
        <a-button :disabled="saving" @click="emit('close')">关闭</a-button>
        <a-button v-if="canSave" type="primary" :disabled="!changed" :loading="saving" @click="publish">保存新版本</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.period-tier-item { margin:16px 0; padding:12px; border:1px solid var(--line); border-radius:6px; }
.period-tier-grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)) auto; gap:8px; align-items:center; margin-bottom:8px; }
.period-tier-header { color:var(--muted); font-size:12px; }
.period-tier-grid :deep(.ant-input-number),.period-quote-inputs :deep(.ant-input-number) { width:100%; }
.period-quote-inputs { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
.period-quote-result { margin-top:12px; }
.period-activity-query,.period-activity-result { margin-bottom:14px; }
.period-activity-source { margin-bottom:8px; }
.period-activity-table { overflow-x:auto; }
.period-activity-table table { width:100%; min-width:520px; border-collapse:collapse; font-size:13px; }
.period-activity-table th,.period-activity-table td { padding:8px; border-bottom:1px solid var(--line); text-align:left; }
.period-activity-table th { font-weight:500; }
.period-activity-table td { white-space:nowrap; font-variant-numeric:tabular-nums; }
.period-activity-table td small { color:var(--muted); }
.period-activity-issues { margin:8px 0 0; padding-inline-start:18px; color:var(--muted); }
@media (max-width:540px) { .period-quote-inputs { grid-template-columns:1fr; gap:0; } }
</style>

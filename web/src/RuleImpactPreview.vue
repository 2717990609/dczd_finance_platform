<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({periods:Array, periodId:Number, simulate:Function, formatMoney:Function, formatTime:Function})
const loading = defineModel('busy', {type:Boolean, default:false})
const selected = ref(props.periodId)
const result = ref()
const error = ref('')
const keyword = ref('')
const options = computed(() => props.periods.map(period=>({
  value:period.id,
  label:period.name+' · '+(period.classStartsAt && props.formatTime ? props.formatTime(period.classStartsAt) : period.startsOn+' 至 '+period.endsOn),
})))
const rows = computed(() => {
  const term = keyword.value.trim().toLowerCase()
  return (result.value?.items || []).filter(row=>!term || [row.orderId,row.orderNo,row.recipient].some(value=>value?.toLowerCase().includes(term)))
})
const columns = [
  {title:'原订单',key:'order',width:145},
  {title:'受益对象',dataIndex:'recipient',key:'recipient',width:125},
  {title:'类型',dataIndex:'entryKind',key:'entryKind',width:75},
  {title:'现行',dataIndex:'beforeMinor',key:'beforeMinor',width:82,align:'right'},
  {title:'拟发布',dataIndex:'afterMinor',key:'afterMinor',width:82,align:'right'},
  {title:'差额',dataIndex:'deltaMinor',key:'deltaMinor',width:82,align:'right',fixed:'right'},
]
const entryLabels = {accrual:'计提',refund:'退款扣减',correction:'更正'}
const delta = minor => (minor>0 ? '+' : '')+props.formatMoney(minor)
let sequence = 0
let pending

watch(options, values => {
  if (values.some(option=>option.value===selected.value)) return
  selected.value = values.length===1 ? values[0].value : undefined
}, {immediate:true})
watch(selected, () => {
  sequence++
  pending?.abort()
  result.value = undefined
  error.value = ''
  keyword.value = ''
  loading.value = false
})
async function calculate() {
  if (!selected.value || loading.value) return
  const request = ++sequence
  pending = new AbortController()
  loading.value = true
  error.value = ''
  result.value = undefined
  try {
    const report = await props.simulate(selected.value,pending.signal)
    if (request===sequence) result.value = report
  } catch (err) {
    if (request===sequence) error.value = err.message
  } finally {
    if (request===sequence) loading.value = false
  }
}
onBeforeUnmount(() => {
  sequence++
  pending?.abort()
  loading.value = false
})
</script>

<template>
  <section class="rule-impact-preview" aria-label="发布前差额试算">
    <h3 class="form-subhead">发布前差额试算</h3>
    <div class="rule-impact-period">
      <div>
        <label for="rule-impact-period">核算期</label>
        <a-select id="rule-impact-period" v-model:value="selected" :options="options" :disabled="loading" show-search option-filter-prop="label"
          placeholder="选择开放账期或班次"/>
      </div>
      <a-button :disabled="!selected" :loading="loading" @click="calculate">计算差额</a-button>
    </div>
    <p v-if="!options.length" class="workbench-scope-note">暂无适用的开放核算期。</p>
    <a-alert v-if="error" class="feedback" :message="error" type="error" show-icon/>
    <template v-if="result">
      <p v-if="!result.currentRuleAvailable" class="workbench-scope-note">现行规则尚未覆盖所选期次，现行金额按零显示。</p>
      <p v-if="result.matchedPaymentCount!=null" class="workbench-scope-note">拟发布规则匹配 {{ result.matchedPaymentCount }} 笔付款</p>
      <div class="rule-impact-totals">
        <div><small>现行规则净分润</small><strong>{{ formatMoney(result.beforeMinor) }}</strong></div>
        <div><small>拟发布规则净分润</small><strong>{{ formatMoney(result.afterMinor) }}</strong></div>
        <div><small>净差额</small><strong>{{ delta(result.deltaMinor) }}</strong></div>
      </div>
      <p class="workbench-scope-note">待核项 {{ result.beforeIssueCount }} → {{ result.afterIssueCount }} 条</p>
      <a-input v-if="result.items.length" v-model:value="keyword" aria-label="搜索差额明细"
        placeholder="搜索原订单号或受益对象" allow-clear/>
      <a-table :columns="columns" :data-source="rows" size="small" row-key="key"
        :scroll="{x:591}" :pagination="{pageSize:10,hideOnSinglePage:true,showSizeChanger:false}">
        <template #bodyCell="{column,record,text}">
          <template v-if="column.key==='order'">
            {{ record.orderNo || record.orderId }}
            <small v-if="record.orderNo && record.orderNo!==record.orderId" class="order-version">{{ record.orderId }}</small>
          </template>
          <template v-else-if="column.key==='entryKind'">{{ entryLabels[text] || text }}</template>
          <span v-else-if="column.key==='deltaMinor'" class="numeric">{{ delta(text) }}</span>
          <span v-else-if="column.key==='beforeMinor' || column.key==='afterMinor'" class="numeric">{{ formatMoney(text) }}</span>
        </template>
        <template #emptyText>
          {{ keyword ? '未找到符合条件的差额' : result.matchedPaymentCount===0 ? '本期暂无适用付款' : '本期金额无变化' }}
        </template>
      </a-table>
    </template>
  </section>
</template>

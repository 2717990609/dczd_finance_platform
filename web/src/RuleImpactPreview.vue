<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { salesRoles } from './catalog-labels'
import { draftIssueLabels } from './draft-issue-labels'

const props = defineProps({periods:Array, periodId:Number, simulate:Function, formatMoney:Function, formatTime:Function, showMatchedOrders:Boolean})
const loading = defineModel('busy', {type:Boolean, default:false})
const selected = ref(props.periodId)
const result = ref()
const error = ref('')
const keyword = ref('')
const matchedOrders = ref(false)
const arrangementId = ref()
const page = ref(1)
const options = computed(() => props.periods.map(period=>({
  value:period.id,
  label:period.name+' · '+(period.classStartsAt && props.formatTime ? props.formatTime(period.classStartsAt) : period.startsOn+' 至 '+period.endsOn),
})))
const items = computed(() => result.value?.items || [])
const memberLabels = {new:'新报',retrain:'复训'}
const arrangements = computed(() => {
  if (!matchedOrders.value) return []
  const groups = new Map()
  for (const row of items.value) {
    if (!row.arrangementId) continue
    if (!groups.has(row.arrangementId)) {
      groups.set(row.arrangementId,{value:row.arrangementId,name:row.arrangement,
        member:memberLabels[row.arrangementClassMember],role:salesRoles[row.arrangementRole],recipients:new Set()})
    }
    groups.get(row.arrangementId).recipients.add(row.recipient)
  }
  const choices = Array.from(groups.values(),({value,name,member,role,recipients})=>({
    value,label:[name,member,role,recipients.size===1 ? recipients.values().next().value : recipients.size+' 个收款对象']
      .filter(Boolean).join(' · '),
  })).sort((a,b)=>a.label.localeCompare(b.label,'zh-CN') || a.value-b.value)
  const counts = new Map()
  for (const choice of choices) counts.set(choice.label,(counts.get(choice.label) || 0)+1)
  const indices = new Map()
  return choices.map(choice=>{
    if (counts.get(choice.label)===1) return choice
    const position = (indices.get(choice.label) || 0)+1
    indices.set(choice.label,position)
    return {...choice,label:choice.label+' · 同名安排 '+position}
  })
})
const rows = computed(() => {
  const term = keyword.value.trim().toLowerCase()
  return items.value.filter(row=>(arrangementId.value==null || row.arrangementId===arrangementId.value) &&
    (!term || [row.orderId,row.orderNo,row.recipient,row.allocationOwner,row.arrangement,salesRoles[row.arrangementRole]]
      .some(value=>value?.toLowerCase().includes(term))))
})
const columns = [
  {title:'原订单',key:'order',width:145},
  {title:'收款对象',dataIndex:'recipient',key:'recipient',width:125},
  {title:'收款安排',dataIndex:'arrangement',key:'arrangement',width:124},
  {title:'现行',dataIndex:'beforeMinor',key:'beforeMinor',width:82,align:'right'},
  {title:'拟发布',dataIndex:'afterMinor',key:'afterMinor',width:82,align:'right'},
  {title:'差额',dataIndex:'deltaMinor',key:'deltaMinor',width:82,align:'right',fixed:'right'},
]
const visibleColumns = computed(() => matchedOrders.value ? [
  ...columns.slice(0,3),
  {title:'计提金额',dataIndex:'afterMinor',key:'afterMinor',width:100,align:'right'},
] : columns)
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
  arrangementId.value = undefined
  loading.value = false
})
watch([result,keyword,arrangementId], () => { page.value=1 })
async function calculate(showOrders = false) {
  if (!selected.value || loading.value) return
  const request = ++sequence
  pending = new AbortController()
  loading.value = true
  if (matchedOrders.value!==showOrders) keyword.value = ''
  matchedOrders.value = showOrders
  arrangementId.value = undefined
  error.value = ''
  result.value = undefined
  try {
    const report = await props.simulate(selected.value,pending.signal,showOrders)
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
      <a-button :disabled="!selected || matchedOrders && loading" :loading="!matchedOrders && loading" @click="calculate()">计算差额</a-button>
      <a-button v-if="showMatchedOrders" :disabled="!selected || !matchedOrders && loading" :loading="matchedOrders && loading"
        @click="calculate(true)">查看计提订单</a-button>
    </div>
    <p v-if="!options.length" class="workbench-scope-note">暂无适用的开放核算期。</p>
    <a-alert v-if="error" class="feedback" :message="error" type="error" show-icon/>
    <template v-if="result">
      <p v-if="!result.currentRuleAvailable" class="workbench-scope-note">现行配置尚未覆盖所选核算期。</p>
      <p v-if="result.matchedPaymentCount!=null" class="workbench-scope-note">拟发布规则匹配 {{ result.matchedPaymentCount }} 笔付款</p>
      <div v-if="!matchedOrders" class="rule-impact-totals">
        <div><small>现行规则净分润</small><strong>{{ formatMoney(result.beforeMinor) }}</strong></div>
        <div><small>拟发布规则净分润</small><strong>{{ formatMoney(result.afterMinor) }}</strong></div>
        <div><small>净差额</small><strong>{{ delta(result.deltaMinor) }}</strong></div>
      </div>
      <details v-if="result.issueReasons?.length" class="rule-impact-issues">
        <summary>待核项 {{ result.beforeIssueCount }} → {{ result.afterIssueCount }} 条</summary>
        <table aria-label="待核原因对比">
          <thead><tr><th scope="col">待核原因</th><th scope="col">现行</th><th scope="col">拟发布</th></tr></thead>
          <tbody>
            <tr v-for="issue in result.issueReasons" :key="issue.code">
              <th scope="row">{{ draftIssueLabels[issue.code] || issue.code }}</th>
              <td>{{ issue.beforeCount }}</td>
              <td :class="{'rule-impact-issue-increase':issue.afterCount>issue.beforeCount}">{{ issue.afterCount }}</td>
            </tr>
          </tbody>
        </table>
      </details>
      <p v-else class="workbench-scope-note">待核项 {{ result.beforeIssueCount }} → {{ result.afterIssueCount }} 条</p>
      <div v-if="items.length" class="rule-impact-filters">
        <a-select v-if="arrangements.length>1" v-model:value="arrangementId" :options="arrangements"
          aria-label="筛选收款安排" placeholder="全部收款安排" allow-clear show-search option-filter-prop="label"/>
        <a-input v-model:value="keyword" :aria-label="matchedOrders ? '搜索计提订单' : '搜索差额明细'"
          placeholder="搜索订单号、收款对象、负责人或安排" allow-clear/>
      </div>
      <p v-if="arrangementId!=null" class="workbench-scope-note">{{ rows.length }} 条计提明细</p>
      <a-table :columns="visibleColumns" :data-source="rows" size="small" row-key="key"
        :scroll="{x:matchedOrders ? 494 : 640}"
        :pagination="{current:page,pageSize:10,hideOnSinglePage:true,showSizeChanger:false,onChange:value=>page=value}">
        <template #bodyCell="{column,record,text}">
          <template v-if="column.key==='order'">
            {{ record.orderNo || record.orderId }}
            <small v-if="record.orderNo && record.orderNo!==record.orderId" class="order-version">{{ record.orderId }}</small>
          </template>
          <template v-else-if="column.key==='recipient'">
            {{ text }}
            <small v-if="record.allocationOwner" class="order-version">分配负责人 {{ record.allocationOwner }}</small>
          </template>
          <template v-else-if="column.key==='arrangement'">
            {{ text || entryLabels[record.entryKind] }}
            <small v-if="record.arrangementRole || record.entryKind!=='accrual'" class="order-version">
              {{ [salesRoles[record.arrangementRole] || record.arrangementRole, record.entryKind!=='accrual' && entryLabels[record.entryKind]].filter(Boolean).join(' · ') }}
            </small>
          </template>
          <span v-else-if="column.key==='deltaMinor'" class="numeric">{{ delta(text) }}</span>
          <span v-else-if="column.key==='beforeMinor' || column.key==='afterMinor'" class="numeric">{{ formatMoney(text) }}</span>
        </template>
        <template #emptyText>
          {{ matchedOrders ? (keyword || arrangementId!=null ? '未找到匹配的计提订单' : '本期暂无可计提订单') :
            keyword ? '未找到符合条件的差额' : result.matchedPaymentCount===0 ? '本期暂无适用付款' :
            result.afterIssueCount ? '暂未算出金额差额，请查看待核项。' : '本期金额无变化' }}
        </template>
      </a-table>
    </template>
  </section>
</template>

<style scoped>
.rule-impact-filters {
  display:grid;
  grid-template-columns:repeat(auto-fit,minmax(min(100%,225px),1fr));
  gap:8px;
}
</style>

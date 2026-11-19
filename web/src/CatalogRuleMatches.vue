<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({periodId:Number, api:Function, formatMoney:Function, formatTime:Function})
const report = ref()
const error = ref('')
const loading = ref(false)
const page = ref(1)
let sequence = 0
const columns = [
  {title:'原订单',key:'order',width:210},
  {title:'付款时间',key:'paidAt',width:155},
  {title:'实收金额',key:'paidMinor',width:110,align:'right'},
  {title:'适用场景',key:'scenario',width:155},
  {title:'规则版本',key:'rule',width:210},
  {title:'匹配状态',key:'status',width:130},
]
const statuses = {
  MATCHED:'已匹配',
  INVALID_PAYMENT:'付款待核',
  DUPLICATE_PAYMENT:'重复付款',
  UNCLASSIFIED:'分类待核',
  MAPPING_MISSING:'未匹配场景',
  MAPPING_CONFLICT:'场景冲突',
  RULE_MISSING:'未匹配规则',
  RULE_CONFLICT:'规则冲突',
}
const salesStatus = {
  SALES_SOURCE_MISSING:'需重拉订单销售数据',
  SALES_SOURCE_CONFLICT:'成交人或锁粉归属冲突',
  SALES_PERSON_MISSING:'成交人待核',
  SALES_PERSON_CONFLICT:'成交人标识冲突',
  SALES_COMPANY_MISSING:'成交时的销售公司待核',
  SALES_COMPANY_CONFLICT:'成交时的销售公司有冲突',
  SALES_APPOINTMENT_MISSING:'成交时的销售任职待核',
  SALES_CATEGORY_MISSING:'成交时的销售类别待核',
  SALES_CATEGORY_CONFLICT:'成交时的销售类别有冲突',
  SALES_RELATIONSHIP_MISSING:'直销或间销待核',
  SALES_RATE_MISSING:'缺少适用费率',
  SALES_RATE_CONFLICT:'适用费率冲突',
  SALES_COMPANY_REVIEW:'销售公司更正待核',
  SALES_TEAM_PENDING:'成交时的团队任职待核',
  SALES_GROUP_PENDING:'成交时的销售小组待核',
}
const salesRoles = {SALES:'成交人',DCAI_GROUP_LEAD:'小组组长',DCAI_SERVICE:'服务岗',DCAI_TEAM_HEAD:'团队负责人'}
const pagination = computed(() => ({current:page.value,pageSize:25,total:report.value?.orderCount || 0,showSizeChanger:false}))

async function refresh() {
  const request = ++sequence
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams({periodId:props.periodId,page:page.value})
    const result = await props.api('/period-rule-matches?'+params)
    if (request===sequence) report.value=result
  } catch (err) {
    if (request===sequence) {
      report.value=undefined
      error.value=err.message
    }
  } finally {
    if (request===sequence) loading.value=false
  }
}
function changePage(next) {
  page.value=next.current
  refresh()
}
watch(() => props.periodId, () => {
  page.value=1
  report.value=undefined
  refresh()
}, {immediate:true})
onBeforeUnmount(() => { sequence++ })
</script>

<template>
  <section class="records catalog-match">
    <div class="records-head">
      <h2>订单规则匹配</h2>
      <a-button :loading="loading" @click="refresh">刷新匹配</a-button>
    </div>
    <p class="draft-report-note">按付款时间核对场景和规则版本。该产品的分润待启用。</p>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <div v-if="report" class="catalog-match-totals">
      <span>已匹配 <strong>{{ report.matchedCount }} / {{ report.orderCount }}</strong> 笔</span>
      <span>账期实收 <strong>{{ formatMoney(report.paidMinor) }}</strong></span>
      <span>已匹配实收 <strong>{{ formatMoney(report.matchedMinor) }}</strong></span>
    </div>
    <a-table :columns="columns" :data-source="report?.items || []" :loading="loading" row-key="sourceVersionId"
      :pagination="pagination" :scroll="{x:970}" @change="changePage">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='order'">
          {{ record.orderNo || record.orderId }}
          <small v-if="record.sourceTransactionType" class="order-version">原交易类型 {{ record.sourceTransactionType }}</small>
        </template>
        <template v-else-if="column.key==='paidAt'">{{ formatTime(record.paidAt) }}</template>
        <template v-else-if="column.key==='paidMinor'">{{ formatMoney(record.paidMinor) }}</template>
        <template v-else-if="column.key==='scenario'">{{ record.scenarioLabel || '—' }}</template>
        <template v-else-if="column.key==='rule'">
          {{ record.ruleVersion ? 'v'+record.ruleVersion+' · '+record.ruleLabel : '—' }}
          <details v-if="record.fixedParts.length" class="rule-count-review">
            <summary>查看已核对固定分项</summary>
            <div v-for="part in record.fixedParts" :key="part.itemKey" class="order-version">{{ part.label }} {{ formatMoney(part.amountMinor) }}</div>
          </details>
          <details v-if="record.salesStatus==='CALCULATED'" class="rule-count-review">
            <summary>查看销售分项</summary>
            <div v-for="part in record.salesParts" :key="part.role" class="order-version">
              {{ salesRoles[part.role] }} · {{ part.personName }} · {{ formatMoney(part.amountMinor) }}
            </div>
          </details>
          <small v-else-if="record.salesStatus" class="order-version">{{ salesStatus[record.salesStatus] || '销售分项待核' }}</small>
        </template>
        <a-tag v-else-if="column.key==='status'" :color="record.status==='MATCHED'?'green':'orange'">{{ statuses[record.status] || record.status }}</a-tag>
      </template>
      <template #emptyText>本期暂无付款记录</template>
    </a-table>
  </section>
</template>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { benefitStatus, salesRoles, salesStatus } from './catalog-labels'

const props = defineProps({periodId:Number, draftAvailable:Boolean, api:Function, formatMoney:Function, formatTime:Function})
const emit = defineEmits(['show-order'])
const report = ref()
const error = ref('')
const loading = ref(false)
const page = ref(1)
const memberType = ref('')
const poolOpen = ref(false)
const poolReport = ref()
const poolError = ref('')
const poolLoading = ref(false)
let sequence = 0
let poolSequence = 0
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
  PAYMENT_ALREADY_SETTLED:'此付款已在其他账期结算',
  UNCLASSIFIED:'分类待核',
  MAPPING_MISSING:'未匹配场景',
  MAPPING_CONFLICT:'场景冲突',
  RULE_MISSING:'未匹配规则',
  RULE_CONFLICT:'规则冲突',
  COMPOUND_RECOGNITION_UNSUPPORTED:'组合确认待启用',
  COMPOUND_COMPLETION_PENDING:'待完成确认',
  COMPOUND_COMPLETED_IN_OTHER_PERIOD:'分润归入完成账期',
  COMPOUND_PRIOR_REFUND_INVALID:'完成前退款待核',
}
const poolColumns = [
  {title:'团队池',dataIndex:'name'},
  {title:'归集付款笔数',dataIndex:'paymentCount',align:'right'},
  {title:'付款归集金额',key:'amount',align:'right'},
]
const memberTypes = {NEW:'新购',RENEWAL:'续费'}
const memberTypeOptions = [{value:'',label:'全部订单'},
  ...Object.entries(memberTypes).map(([value,label])=>({value,label})),{value:'UNKNOWN',label:'未标记'}]
const pagination = computed(() => ({current:page.value,pageSize:25,total:report.value?.orderCount || 0,showSizeChanger:false}))

async function refresh() {
  const request = ++sequence
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams({periodId:props.periodId,page:page.value})
    if (memberType.value) params.set('memberTransactionType',memberType.value)
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
function changeMemberType(value) {
  memberType.value=value
  page.value=1
  refresh()
}
async function openPools() {
  const request = ++poolSequence
  poolOpen.value = true
  poolLoading.value = true
  poolReport.value = undefined
  poolError.value = ''
  try {
    const result = await props.api('/period-pool-contributions?'+new URLSearchParams({periodId:props.periodId}))
    if (request===poolSequence) poolReport.value=result
  } catch (err) {
    if (request===poolSequence) poolError.value=err.message
  } finally {
    if (request===poolSequence) poolLoading.value=false
  }
}
watch(() => props.periodId, () => {
  poolSequence++
  poolOpen.value=false
  page.value=1
  memberType.value=''
  report.value=undefined
  refresh()
}, {immediate:true})
onBeforeUnmount(() => { sequence++; poolSequence++ })
</script>

<template>
  <section class="records catalog-match">
    <div class="records-head">
      <h2>订单规则匹配</h2>
      <div class="topbar-actions">
        <a-select v-if="report?.hasMembershipOrders" :value="memberType" :options="memberTypeOptions"
          class="catalog-match-member-type" aria-label="会员订单类型" @change="changeMemberType"/>
        <a-button @click="openPools">团队池归集</a-button>
        <a-button :loading="loading" @click="refresh">刷新匹配</a-button>
      </div>
    </div>
    <p class="draft-report-note">{{ report?.hasMembershipOrders ? '会员新购和续费共用一个分润场景，规则版本按付款时间匹配。'
      : draftAvailable ? '按当前订单核对适用规则。试算和结算明细可查实际使用的规则版本。' : '按付款时间核对业务场景和适用规则。该产品暂未开放分润试算。' }}</p>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <div v-if="report" class="catalog-match-totals">
      <span>已匹配 <strong>{{ report.matchedCount }} / {{ report.orderCount }}</strong> 笔</span>
      <span>{{ memberType ? '筛选实收' : '账期实收' }} <strong>{{ formatMoney(report.paidMinor) }}</strong></span>
      <span>已匹配基数 <strong>{{ formatMoney(report.matchedMinor) }}</strong></span>
      <button v-if="!memberType && report.hasMembershipOrders && report.unmarkedMembershipCount"
        type="button" class="order-link" @click="changeMemberType('UNKNOWN')">{{ report.unmarkedMembershipCount }} 笔类型未标记</button>
    </div>
    <a-table :columns="columns" :data-source="report?.items || []" :loading="loading" row-key="sourceVersionId"
      :pagination="pagination" :scroll="{x:970}" @change="changePage">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='order'">
          <button type="button" class="order-link" @click="emit('show-order',record.sourceVersionId)">{{ record.orderNo || record.orderId }}</button>
          <small v-if="record.sourceDomain==='VIP' || record.sourceTransactionType" class="order-version">
            {{ record.sourceDomain==='VIP' ? memberTypes[record.sourceTransactionType] || '类型未标记' : record.sourceTransactionType }}
          </small>
        </template>
        <template v-else-if="column.key==='paidAt'">
          {{ formatTime(record.paidAt) }}
          <small v-if="record.completedAt" class="order-version">完成 {{ formatTime(record.completedAt) }}<template v-if="record.completionCarried"> · 补入本期</template></small>
        </template>
        <template v-else-if="column.key==='paidMinor'">{{ formatMoney(record.paidMinor) }}</template>
        <template v-else-if="column.key==='scenario'">{{ record.scenarioLabel || '—' }}</template>
        <template v-else-if="column.key==='rule'">
          {{ record.ruleVersion ? 'v'+record.ruleVersion+' · '+record.ruleLabel : '—' }}
          <details v-if="record.fixedParts.length" class="rule-count-review">
            <summary>查看已核对固定分项</summary>
            <div v-for="part in record.fixedParts" :key="part.itemKey" class="order-version">{{ part.label }} {{ formatMoney(part.amountMinor) }}</div>
          </details>
          <details v-if="record.benefitParts?.length" class="rule-count-review">
            <summary>查看负责人、公司与团队池</summary>
            <div v-for="part in record.benefitParts" :key="part.itemKey" class="order-version">
              {{ part.label }} ·
              <template v-if="part.status==='CALCULATED'">
                {{ part.beneficiaryName }} · {{ formatMoney(part.amountMinor) }}
                <template v-if="part.kind==='pool'"> · 待分配（{{ part.ownerName }}）</template>
              </template>
              <template v-else-if="part.status==='ZERO'">{{ formatMoney(0) }}</template>
              <template v-else>{{ benefitStatus[part.status] || salesStatus[part.status] || '分润分项待核' }}</template>
            </div>
          </details>
          <small v-else-if="record.benefitStatus" class="order-version">{{ benefitStatus[record.benefitStatus] }}</small>
          <details v-if="record.salesStatus==='CALCULATED' && record.salesParts?.length" class="rule-count-review">
            <summary>查看销售分项</summary>
            <div v-for="part in record.salesParts" :key="part.role" class="order-version">
              <template v-if="part.companyName">收款公司 {{ part.companyName }} · 成交销售 {{ part.personName }}</template>
              <template v-else>{{ salesRoles[part.role] }} · {{ part.personName }}</template>
              · {{ formatMoney(part.amountMinor) }}
            </div>
          </details>
          <small v-else-if="record.salesStatus==='CALCULATED'" class="order-version">销售分润 {{ formatMoney(0) }}</small>
          <small v-else-if="record.salesStatus" class="order-version">{{ salesStatus[record.salesStatus] || '销售分项待核' }}</small>
        </template>
        <a-tag v-else-if="column.key==='status'" :color="record.status==='MATCHED'?'green':'orange'">{{ statuses[record.status] || record.status }}</a-tag>
      </template>
      <template #emptyText>本期暂无付款记录</template>
    </a-table>
    <a-drawer :open="poolOpen" title="本期团队池归集"
      :width="'min(650px, 100vw)'" @close="poolOpen=false">
      <p class="draft-report-note">{{ draftAvailable ? '按本期付款归集。退款扣减和实际分配见试算结果。' : '按本期付款核对团队池归集金额。' }}</p>
      <a-alert v-if="poolError" type="error" :message="poolError" show-icon/>
      <a-alert v-if="poolReport?.pendingPaymentCount" type="warning" show-icon
        :message="`${poolReport.pendingPaymentCount} / ${poolReport.paymentCount} 笔付款存在待核归集分项`"/>
      <a-table :columns="poolColumns" :data-source="poolReport?.pools || []" :loading="poolLoading" row-key="key"
        size="small" :pagination="false" :scroll="{x:430}">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='amount'">{{ formatMoney(record.amountMinor) }}</template>
        </template>
        <template #emptyText>本期暂无可归集金额</template>
      </a-table>
    </a-drawer>
  </section>
</template>

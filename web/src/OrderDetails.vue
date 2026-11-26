<script setup>
import { computed, ref, watch } from 'vue'

const props = defineProps({ selection: Object, api: Function, formatMoney: Function, formatTime: Function,
  sourceLabel: Function, labels: Object, roleLabel: Function, canPull: Boolean, recordsLink: Function })
const emit = defineEmits(['close', 'pull'])
const order = ref()
const loading = ref(false)
const error = ref('')
const display = computed(() => order.value?.display || {})
const dcaiPaymentMethods = {'1':'微信小程序','2':'微信公众号','3':'线下转账','4':'微信小程序虚拟支付'}
const practicePaymentMethods = {'1':'微信','2':'支付宝','3':'银联','4':'对公转账','6':'通联微信','7':'通联快捷','10':'大春AI兑换'}
const transactionTypes = {NEW:'新购',RENEWAL:'续费',UPGRADE:'升级',STANDARD:'普通订单'}
const paymentMethod = computed(() => {
  const raw = display.value.paymentMethod
  if (order.value?.source==='DCAI') return dcaiPaymentMethods[raw] || raw
  if (order.value?.domain==='PRACTICE') return practicePaymentMethods[raw] || raw
  return raw
})
const transactionType = computed(() => {
  const raw = order.value?.transactionType
  if (order.value?.domain==='PRACTICE') {
    if (raw==='2') return display.value.settlesDepositOrderId ? '尾款' : '全款'
    if (raw==='1') return '订金'
    if (raw==='3') return '复训'
  }
  return transactionTypes[raw] || raw
})
const isRefund = computed(() => ['ORDER_REFUNDED','REFUND_SUCCEEDED','REFUND_ADJUSTED'].includes(order.value?.kind))
const paidAt = computed(() => display.value.paidAt || (order.value?.kind === 'ORDER_PAID' ? order.value.occurredAt : null))
const refundedAt = computed(() => display.value.refundedAt || (order.value?.kind === 'REFUND_SUCCEEDED' ? order.value.occurredAt : null))
const contacts = computed(() => [
  {label:'客户', name:display.value.customerName, nickname:display.value.customerNickname, phone:display.value.customerPhone},
  {label:'推荐人', name:display.value.referrerName, nickname:display.value.referrerNickname, phone:display.value.referrerPhone},
  {label:'销售人员', name:display.value.sellerName, nickname:display.value.sellerNickname, phone:display.value.sellerPhone},
].map(({label, name, nickname, phone}) => ({
  label, text:[...new Set([nickname, name, phone].filter(Boolean))].join(' · '),
})).filter(({text}) => text))
const salesCompanyID = computed(() => order.value?.practiceRegistration?.companyId || order.value?.salesCompanyId)
const recordsHref = computed(() => !loading.value && !error.value && order.value?.orderId
  ? props.recordsLink(order.value) : undefined)
const referralRoles = [
  {role:'DIRECT_PID_USER',label:'直接推荐账号'},
  {role:'LOCK_DIRECT_SHARE_USER',label:'锁粉推荐账号'},
]
const referralAccounts = computed(() => {
  if (order.value?.source !== 'DCAI') return []
  const participants = order.value.participants || []
  return referralRoles.map(entry => ({
    ...entry,
    accounts: [...new Set(participants.filter(person=>person.role===entry.role && person.userId).map(person=>person.userId))],
  })).filter(entry=>entry.accounts.length)
})

watch(() => props.selection, async (selection, _, onCleanup) => {
  order.value = selection.order
  error.value = ''
  loading.value = Boolean(selection.versionId)
  if (!selection.versionId) return
  let active = true
  onCleanup(() => { active = false })
  try {
    const result = await props.api('/orders/version?id=' + selection.versionId)
    if (active) order.value = {...selection.order, ...result}
  } catch (err) {
    if (active) error.value = err.message
  } finally {
    if (active) loading.value = false
  }
}, { immediate: true })
</script>

<template>
  <a-drawer :open="true" title="订单详情" :width="'min(510px, 100vw)'" @close="emit('close')">
    <section aria-label="订单详情">
      <a-alert v-if="error" type="error" :message="error" show-icon/>
      <a-spin v-else :spinning="loading">
        <a-descriptions v-if="order" bordered size="small" :column="1">
          <a-descriptions-item label="订单编号">{{ order.orderNo || order.orderId }}</a-descriptions-item>
          <a-descriptions-item label="原订单 ID">{{ order.orderId }}</a-descriptions-item>
          <a-descriptions-item label="订单来源">{{ sourceLabel(order.source) }}</a-descriptions-item>
          <a-descriptions-item v-if="order.productName" label="归属产品">{{ order.productName }}</a-descriptions-item>
          <a-descriptions-item label="业务类型">{{ labels[order.kind] || order.kind }}</a-descriptions-item>
          <a-descriptions-item v-if="transactionType" label="来源交易类型">{{ transactionType }}</a-descriptions-item>
          <a-descriptions-item v-for="contact in contacts" :key="contact.label" :label="contact.label">{{ contact.text }}</a-descriptions-item>
          <a-descriptions-item v-if="paymentMethod" label="支付方式">{{ paymentMethod }}</a-descriptions-item>
          <a-descriptions-item v-if="order.amountMinor != null" :label="isRefund ? '退款金额' : '实付金额'">{{ formatMoney(order.amountMinor) }}</a-descriptions-item>
          <a-descriptions-item v-if="display.originalAmountMinor != null" label="订单原金额">{{ formatMoney(display.originalAmountMinor) }}</a-descriptions-item>
          <a-descriptions-item v-if="display.sourceSettlementBasisMinor != null" label="来源结算基数">{{ formatMoney(display.sourceSettlementBasisMinor) }}</a-descriptions-item>
          <a-descriptions-item v-if="paidAt" label="支付时间">{{ formatTime(paidAt) }}</a-descriptions-item>
          <a-descriptions-item v-if="refundedAt" label="退款时间">{{ formatTime(refundedAt) }}</a-descriptions-item>
          <a-descriptions-item v-if="(!paidAt && !refundedAt) || !['ORDER_PAID','REFUND_SUCCEEDED'].includes(order.kind)" label="业务时间">{{ formatTime(order.occurredAt) }}</a-descriptions-item>
          <a-descriptions-item v-if="order.paymentId" label="付款编号">{{ order.paymentId }}</a-descriptions-item>
          <a-descriptions-item v-if="display.paymentTrace" label="支付流水号">{{ display.paymentTrace }}</a-descriptions-item>
          <a-descriptions-item v-if="display.refundNo || order.refundId" label="退款单号">{{ display.refundNo || order.refundId }}</a-descriptions-item>
          <a-descriptions-item v-if="display.settlesDepositOrderId" label="关联订金订单 ID">{{ display.settlesDepositOrderId }}</a-descriptions-item>
          <a-descriptions-item v-if="isRefund && order.refundsPayment?.length" label="原付款订单 ID">{{ order.refundsPayment.map(item => item.orderId).join('、') }}</a-descriptions-item>
          <a-descriptions-item v-if="display.correctionItemId" label="归属更正">
            <div>{{ display.correctionBatchNo || display.correctionItemId }}
              <small v-if="display.correctionCreatedAt" class="order-version">{{ formatTime(display.correctionCreatedAt) }}</small>
            </div>
          </a-descriptions-item>
          <a-descriptions-item v-if="order.referrerId" :label="order.practiceRegistration ? '报名销售账号' : '来源推荐人 ID'">{{ order.referrerId }}</a-descriptions-item>
          <a-descriptions-item v-for="entry in referralAccounts" :key="entry.role" :label="entry.label">{{ entry.accounts.join('、') }}</a-descriptions-item>
          <a-descriptions-item v-if="order.practiceNoReferrer" label="来源销售归属">无推荐人</a-descriptions-item>
          <a-descriptions-item v-if="order.practiceInactiveCompany" label="来源销售归属">原销售公司在付款前已停用</a-descriptions-item>
          <a-descriptions-item v-if="order.salesAttribution?.length" label="来源销售归属">
            <ul class="order-attribution">
              <li v-for="(claim,index) in order.salesAttribution" :key="index">
                <strong>{{ roleLabel(claim.role) }}</strong>
                <small v-if="claim.user" class="order-version">人员标识 <span>{{ claim.user }}</span></small>
                <small v-if="claim.company" class="order-version">公司或组织标识 <span>{{ claim.company }}</span></small>
              </li>
            </ul>
          </a-descriptions-item>
          <a-descriptions-item v-if="salesCompanyID" :label="order.practiceRegistration ? '报名销售公司' : '来源销售公司'">
            <div>{{ order.salesCompanyName || salesCompanyID }}
              <small v-if="order.salesCompanyName" class="order-version">公司标识 {{ salesCompanyID }}</small>
            </div>
          </a-descriptions-item>
          <a-descriptions-item v-if="order.practiceRegistration?.periodKey" label="报名期次">{{ order.practiceRegistration.periodKey }}</a-descriptions-item>
          <a-descriptions-item v-if="display.referrerCompanyName || order.referrerCompanyId" label="推荐人公司">
            {{ display.referrerCompanyName || order.referrerCompanyId }}
          </a-descriptions-item>
          <a-descriptions-item label="订单版本">{{ order.version }}</a-descriptions-item>
          <a-descriptions-item label="来源更新时间">{{ formatTime(order.updatedAt) }}</a-descriptions-item>
        </a-descriptions>
      </a-spin>
    </section>
    <template #footer>
      <div class="order-details-actions">
        <a-button v-if="recordsHref" :href="recordsHref">查看同单业务记录</a-button>
        <a-button @click="emit('close')">关闭</a-button>
        <a-button v-if="canPull && order?.sourceConfigured && !loading && !error" type="primary" @click="emit('pull',order)">重新拉取订单</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.order-details-actions { display: flex; flex-wrap: wrap; gap: 8px; justify-content: flex-end; }
.order-attribution { list-style: none; margin: 0; padding: 0; }
.order-attribution li + li { margin-top: 6px; }
:deep(.ant-descriptions-item-content) { overflow-wrap: anywhere; }
</style>

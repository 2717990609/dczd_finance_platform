<script setup>
import { computed, ref, watch } from 'vue'

const props = defineProps({ selection: Object, api: Function, formatMoney: Function, formatTime: Function,
  sourceLabel: Function, labels: Object, roleLabel: Function, canPull: Boolean, recordsLink: Function })
const emit = defineEmits(['close', 'pull'])
const order = ref()
const loading = ref(false)
const error = ref('')
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
          <a-descriptions-item label="来源金额">{{ formatMoney(order.amountMinor) }}</a-descriptions-item>
          <a-descriptions-item label="业务时间">{{ formatTime(order.occurredAt) }}</a-descriptions-item>
          <a-descriptions-item v-if="order.paymentId" label="付款编号">{{ order.paymentId }}</a-descriptions-item>
          <a-descriptions-item v-if="order.refundId" label="退款编号">{{ order.refundId }}</a-descriptions-item>
          <a-descriptions-item v-if="order.referrerId" :label="order.practiceRegistration ? '报名销售账号' : '来源推荐人 ID'">{{ order.referrerId }}</a-descriptions-item>
          <a-descriptions-item v-for="entry in referralAccounts" :key="entry.role" :label="entry.label">{{ entry.accounts.join('、') }}</a-descriptions-item>
          <a-descriptions-item v-if="order.practiceNoReferrer" label="来源销售归属">无推荐人</a-descriptions-item>
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
          <a-descriptions-item v-if="order.referrerCompanyId" label="来源推荐公司标识">{{ order.referrerCompanyId }}</a-descriptions-item>
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

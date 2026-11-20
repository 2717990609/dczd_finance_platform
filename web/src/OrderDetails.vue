<script setup>
import { ref, watch } from 'vue'

const props = defineProps({ selection: Object, api: Function, formatMoney: Function, formatTime: Function,
  sourceLabel: Function, labels: Object, canPull: Boolean })
const emit = defineEmits(['close', 'pull'])
const order = ref()
const loading = ref(false)
const error = ref('')

watch(() => props.selection, async (selection, _, onCleanup) => {
  order.value = selection.order
  error.value = ''
  loading.value = Boolean(selection.versionId)
  if (!selection.versionId) return
  let active = true
  onCleanup(() => { active = false })
  try {
    const result = await props.api('/orders/version?id=' + selection.versionId)
    if (active) order.value = result
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
          <a-descriptions-item v-if="order.referrerId" label="来源推荐人 ID">{{ order.referrerId }}</a-descriptions-item>
          <a-descriptions-item label="订单版本">{{ order.version }}</a-descriptions-item>
          <a-descriptions-item label="来源更新时间">{{ formatTime(order.updatedAt) }}</a-descriptions-item>
        </a-descriptions>
      </a-spin>
    </section>
    <template #footer>
      <div class="order-details-actions">
        <a-button @click="emit('close')">关闭</a-button>
        <a-button v-if="canPull" type="primary" @click="emit('pull')">重新拉取订单</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.order-details-actions { display: flex; gap: 8px; justify-content: flex-end; }
:deep(.ant-descriptions-item-content) { overflow-wrap: anywhere; }
</style>

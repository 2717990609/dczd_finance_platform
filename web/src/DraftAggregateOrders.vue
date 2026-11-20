<script setup>
import { ref, watch } from 'vue'

const props = defineProps({periodId:Number,version:Number,group:String,api:Function,formatMoney:Function,formatTime:Function})
const emit = defineEmits(['show-order','close'])
const page = ref(1)
const result = ref({items:[],total:0})
const loading = ref(false)
const error = ref('')
let sequence = 0

watch([()=>props.group,page], async ()=>{
  const request = ++sequence
  loading.value = true
  error.value = ''
  try {
    const query = new URLSearchParams({periodId:props.periodId,version:props.version,group:props.group,page:page.value})
    const rows = await props.api('/catalog-aggregate-drafts/events?'+query)
    if (request===sequence) result.value = rows
  } catch (failure) {
    if (request===sequence) error.value = failure.message
  } finally {
    if (request===sequence) loading.value = false
  }
}, {immediate:true})
</script>

<template>
  <a-modal open title="本组收退款" :width="760" :footer="null" @cancel="emit('close')">
    <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
    <a-table v-else :data-source="result.items" row-key="sourceVersionId" :loading="loading" size="small"
      :pagination="{current:page,pageSize:25,total:result.total,showSizeChanger:false,hideOnSinglePage:true,onChange:value=>page=value}"
      :columns="[{title:'订单',key:'order'},{title:'交易',key:'kind'},{title:'发生时间',key:'time'},{title:'金额',key:'amount'}]">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='order'">
          <button class="order-link" type="button" @click="emit('show-order',record.sourceVersionId)">{{ record.orderNo || record.orderId }}</button>
          <small class="order-version">v{{ record.sourceVersion }}</small>
        </template>
        <template v-else-if="column.key==='kind'">{{ record.kind==='ORDER_PAID' ? '收款' : '退款' }}</template>
        <template v-else-if="column.key==='time'">
          {{ formatTime(record.occurredAt) }}
          <small v-if="record.completedAt && (record.kind==='ORDER_PAID' || record.occurredAt<record.completedAt)" class="order-version">
            订单完成 {{ formatTime(record.completedAt) }}
          </small>
        </template>
        <span v-else class="numeric">{{ formatMoney(record.amountMinor) }}</span>
      </template>
      <template #emptyText><a-empty description="本期没有新交易"/></template>
    </a-table>
  </a-modal>
</template>

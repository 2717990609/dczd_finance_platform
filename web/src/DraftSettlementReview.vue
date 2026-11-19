<script setup>
import { ref, watch } from 'vue'

const props = defineProps({draft:Object, api:Function, formatMoney:Function, formatTime:Function})
const emit = defineEmits(['show-pools', 'show-issues'])
const review = ref()
const loading = ref(false)
const error = ref('')
let sequence = 0

async function load() {
  const request = ++sequence
  const params = new URLSearchParams({periodId:props.draft.periodId, version:props.draft.version})
  loading.value = true
  review.value = undefined
  error.value = ''
  try {
    const result = await props.api('/settlement-review?' + params)
    if (request === sequence) review.value = result
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}
watch(() => props.draft, load, {immediate:true})
</script>

<template>
  <section class="records">
    <div class="records-head">
      <h2>结算核对</h2>
      <a-button :loading="loading" @click="load">刷新核对</a-button>
    </div>
    <div class="settlement-review">
      <a-alert v-if="error" type="error" :message="error" show-icon/>
      <span v-else-if="loading" role="status">正在读取结算核对</span>
      <template v-else-if="review?.settlement">
        <a-alert type="success" :message="'已结算 · ' + formatTime(review.settlement.confirmedAt)" show-icon/>
      </template>
      <template v-else-if="review">
        <a-alert v-if="!review.enabled" type="info" message="该产品尚未开放结算" show-icon/>
        <a-alert v-if="!review.ended" type="info" message="账期尚未结束" show-icon/>
        <a-alert v-if="review.draftStale" type="warning" message="试算数据已变化，请重新试算并核对" show-icon/>
        <a-alert v-if="review.laterPoolActivity" type="warning" message="后续账期已结算此团队池，请核对跨期处理" show-icon/>
        <div v-if="review.issueCount">
          <a-button type="link" @click="emit('show-issues')">查看 {{ review.issueCount }} 条待核项</a-button>
        </div>
        <template v-if="review.pools.length">
          <ul class="review-pools">
            <li v-for="item in review.pools" :key="JSON.stringify([item.unitId,item.scenario,item.itemKey])">
              <span>{{ item.name }}</span>
              <span v-if="item.stale">分配需重新核对</span>
              <span v-if="item.unallocatedMinor > 0">待分配 {{ formatMoney(item.unallocatedMinor) }}</span>
            </li>
          </ul>
          <div><a-button type="link" @click="emit('show-pools')">查看团队池分配</a-button></div>
        </template>
        <a-alert v-else-if="!review.laterPoolActivity && !review.draftStale" type="success" message="团队池已对平" show-icon/>
      </template>
    </div>
  </section>
</template>

<style scoped>
.settlement-review { display: grid; gap: 12px; padding: 16px; }
.review-pools { margin: 0; padding: 0; border: 1px solid #e1e6ed; border-radius: 6px; list-style: none; }
.review-pools li { display: flex; flex-wrap: wrap; gap: 8px; padding: 10px 12px; }
.review-pools li + li { border-top: 1px solid #e1e6ed; }
.review-pools li > :first-child { flex: 1 1 180px; overflow-wrap: anywhere; }
</style>

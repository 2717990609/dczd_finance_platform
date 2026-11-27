<script setup>
import { computed, watch } from 'vue'
import DraftPoolAllocations from './DraftPoolAllocations.vue'
import TeamPoolLedger from './TeamPoolLedger.vue'

const props = defineProps({periods:Array, loading:Boolean, requestedPeriod:Number, requestedPool:String, api:Function,
  formatMoney:Function, formatTime:Function, productId:Number, unitId:String, directoryScope:String, canConfigure:Boolean, periodLink:Function})
const selected = defineModel({type:Number})
const candidates = computed(() => props.periods.filter(period => period.poolDraftVersion))
const options = computed(() => candidates.value.map(period => ({
  value:period.id, label:period.productName+' · '+period.name+(period.settled ? ' · 已结算' : ' · 草稿'),
})))
const period = computed(() => candidates.value.find(period => period.id===selected.value))
const draft = computed(() => period.value && ({
  periodId:period.value.id, version:period.value.poolDraftVersion, settlement:period.value.settled,
  practiceRefundMonth:period.value.practiceRefundMonth,
}))

watch([candidates, () => props.requestedPeriod], ([available, requested], previous) => {
  if (available.some(period => period.id===requested) && (previous[1]!==requested || !period.value)) selected.value = requested
  else if (!period.value) selected.value = available.length===1 ? available[0].id : undefined
}, {immediate:true})
</script>

<template>
  <TeamPoolLedger :api="api" :product-id="productId" :unit-id="unitId" :directory-scope="directoryScope"
    :format-money="formatMoney" :format-time="formatTime" :period-link="periodLink"/>
  <section class="records workbench-controls">
    <div class="workbench-selection workbench-toolbar">
      <div class="workbench-period">
        <label for="team-pool-period">产品和核算期</label>
        <a-select id="team-pool-period" v-model:value="selected" :options="options" :loading="loading"
          placeholder="选择产品和核算期" show-search option-filter-prop="label"/>
      </div>
      <div class="workbench-actions">
        <a-button v-if="period" :href="periodLink(period.id)">查看本期分润</a-button>
        <a-button v-if="canConfigure" href="#pool-config">团队池配置</a-button>
      </div>
    </div>
  </section>
  <DraftPoolAllocations v-if="draft" :key="selected" :draft="draft" :api="api" :format-money="formatMoney"
    :requested-pool="requestedPool" :unit-id="unitId" :directory-scope="directoryScope"/>
  <section v-else class="records empty">
    <a-empty :description="loading ? '正在读取核算期' : options.length ? '选择核算期查看团队池' : '所选范围暂无可查询的团队池核算期'">
      <a-button v-if="!loading && !options.length" href="#tasks">查看待办</a-button>
    </a-empty>
  </section>
</template>

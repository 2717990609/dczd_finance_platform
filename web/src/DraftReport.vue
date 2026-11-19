<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({draft:Object, api:Function, formatMoney:Function, dimensions:Array})
const rows = ref([])
const dimension = ref()
const performanceReady = ref(false)
const periodPerformance = ref(null)
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')
let sequence = 0

const dimensionLabel = computed(() => props.dimensions.find(item=>item.value===dimension.value)?.label)
const columns = computed(() => [
  {title:dimensionLabel.value,dataIndex:'name',key:'name',width:150},
  {title:props.draft.refundsReady ? '本期净业绩' : '本期实收业绩',dataIndex:'performanceMinor',key:'performanceMinor',width:135,align:'right'},
  {title:'正向计提',dataIndex:'accruedMinor',key:'accruedMinor',width:125,align:'right'},
  ...(props.draft.refundsReady ? [
    {title:'退款扣减',dataIndex:'refundMinor',key:'refundMinor',width:125,align:'right'},
    ...(props.draft.catalog ? [] : [{title:'推荐人更正',dataIndex:'correctionMinor',key:'correctionMinor',width:125,align:'right'}]),
    ...(props.draft.catalog ? [{title:props.draft.settlement ? '团队池分配' : '团队池拟分配',dataIndex:'allocationMinor',key:'allocationMinor',width:135,align:'right'}] : []),
    {title:'本期净分润',dataIndex:'totalMinor',key:'totalMinor',width:125,align:'right'},
  ] : []),
])
const pagination = computed(() => ({current:page.value,pageSize:25,total:total.value,showSizeChanger:false}))

async function load() {
  const request = ++sequence
  if (!dimension.value) return
  error.value = ''
  rows.value = []
  total.value = 0
  loading.value = true
  const params = new URLSearchParams({periodId:props.draft.periodId,version:props.draft.version,page:page.value,dimension:dimension.value})
  try {
    const result = await props.api('/draft-report?'+params)
    if (request===sequence) {
      rows.value = result.items
      total.value = result.total
      performanceReady.value = result.performanceReady
      periodPerformance.value = result.performanceMinor
    }
  } catch (err) {
    if (request===sequence) {
      rows.value = []
      total.value = 0
      error.value = err.status===409 ? '试算版本已更新，请刷新结果。' : err.message
    }
  } finally {
    if (request===sequence) loading.value = false
  }
}
function changePage(next) {
  page.value = next.current
  load()
}
watch(() => props.dimensions, choices => {
  if (!choices.some(item=>item.value===dimension.value)) dimension.value=choices[0]?.value
}, {immediate:true})
watch([() => props.draft,dimension], () => { page.value = 1; load() }, {immediate:true})
onBeforeUnmount(() => { sequence++ })
</script>

<template>
  <section class="records draft-report">
    <div class="records-head">
      <h2>业绩与分润汇总</h2>
      <div class="draft-report-dimension">
        <label for="draft-report-dimension">汇总维度</label>
        <a-select id="draft-report-dimension" v-model:value="dimension" :options="dimensions"/>
      </div>
    </div>
    <p v-if="!loading && !error" class="draft-report-note">
      <template v-if="performanceReady">{{ draft.refundsReady ? '全期净业绩' : '全期实收业绩' }} <strong>{{ formatMoney(periodPerformance) }}</strong></template>
      <template v-else>该版本尚无业绩统计。</template>
    </p>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <a-table :columns="columns" :data-source="rows" :loading="loading" row-key="id" :pagination="pagination"
      :scroll="{x:columns.reduce((sum,column)=>sum+column.width,0)}" @change="changePage">
      <template #bodyCell="{column,text}">
        <strong v-if="column.key==='totalMinor'" class="numeric">{{ formatMoney(text) }}</strong>
        <span v-else-if="column.key!=='name'" class="numeric">{{ formatMoney(text) }}</span>
      </template>
      <template #emptyText>本期没有汇总记录</template>
    </a-table>
  </section>
</template>

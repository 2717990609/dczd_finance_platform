<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({draft:Object, api:Function, formatMoney:Function})
const rows = ref([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')
let sequence = 0

const columns = [
  {title:'人员',dataIndex:'personName',key:'personName',width:140},
  {title:'正向计提',dataIndex:'accruedMinor',key:'accruedMinor',width:125,align:'right'},
  {title:'退款扣减',dataIndex:'refundMinor',key:'refundMinor',width:125,align:'right'},
  {title:'推荐人更正',dataIndex:'correctionMinor',key:'correctionMinor',width:125,align:'right'},
  {title:'本期净分润',dataIndex:'totalMinor',key:'totalMinor',width:125,align:'right'},
]
const pagination = computed(() => ({current:page.value,pageSize:25,total:total.value,showSizeChanger:false}))

async function load() {
  const request = ++sequence
  error.value = ''
  loading.value = true
  const params = new URLSearchParams({periodId:props.draft.periodId,version:props.draft.version,page:page.value})
  try {
    const result = await props.api('/draft-personnel?'+params)
    if (request===sequence) {
      rows.value = result.items
      total.value = result.total
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
watch(() => props.draft, () => { page.value = 1; load() }, {immediate:true})
onBeforeUnmount(() => { sequence++ })
</script>

<template>
  <section class="records draft-personnel">
    <div class="records-head"><h2>人员分润汇总</h2></div>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <a-table :columns="columns" :data-source="rows" :loading="loading" row-key="personId" :pagination="pagination" :scroll="{x:640}" @change="changePage">
      <template #bodyCell="{column,text}">
        <strong v-if="column.key==='totalMinor'" class="numeric">{{ formatMoney(text) }}</strong>
        <span v-else-if="column.key!=='personName'" class="numeric">{{ formatMoney(text) }}</span>
      </template>
      <template #emptyText>本期没有人员分润记录</template>
    </a-table>
  </section>
</template>

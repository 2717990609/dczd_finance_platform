<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({api:Function, periodId:Number, productId:Number, formatTime:Function})
const emit = defineEmits(['picked'])
const value = defineModel('value', {type:String})
const manual = ref(false)
const directory = ref([])
const loading = ref(false)
const error = ref('')
let searchText = ''
let sequence = 0
let timer

const options = computed(() => {
  const rows = directory.value.map(row=>({value:row.key,
    label:props.formatTime(row.startsAt)+' · '+row.attendanceCount+' 人 · v'+row.version+' · '+row.key}))
  if (value.value && !rows.some(row=>row.value===value.value)) {
    rows.unshift({value:value.value,label:value.value})
  }
  return rows
})
function search(text = '') {
  searchText = text.trim()
  const request = ++sequence
  clearTimeout(timer)
  loading.value = true
  error.value = ''
  const params = new URLSearchParams()
  if (props.periodId) params.set('periodId',props.periodId)
  else if (props.productId) params.set('productId',props.productId)
  if (searchText) params.set('q',searchText)
  timer = setTimeout(async () => {
    try {
      const rows = await props.api('/practice-rosters/options?'+params)
      if (request===sequence) directory.value = rows
    } catch (err) {
      if (request===sequence) error.value = err.message
    } finally {
      if (request===sequence) loading.value = false
    }
  }, 200)
}
function picked(key) {
  value.value = key
  const roster = directory.value.find(row=>row.key===key)
  if (roster) emit('picked',roster)
}
function cancelSearch() {
  clearTimeout(timer)
  sequence++
}
watch(()=>props.productId,()=>{
  cancelSearch()
  directory.value=[]
  loading.value=false
  error.value=''
  searchText=''
})
onBeforeUnmount(cancelSearch)
</script>

<template>
  <div class="practice-roster-field">
    <div class="practice-roster-field-head">
      <label :for="manual ? 'practice-roster-manual' : 'practice-roster-option'">{{ manual ? '来源期次标识' : '签到名单' }}</label>
      <a-button type="link" size="small" @click="manual=!manual">{{ manual ? '选择已同步名单' : '手动输入来源标识' }}</a-button>
    </div>
    <a-input v-if="manual" id="practice-roster-manual" v-model:value="value" placeholder="PRACTICE:1:1:123" :maxlength="80" allow-clear/>
    <a-select v-else id="practice-roster-option" :value="value || undefined" :options="options" :loading="loading"
      :filter-option="false" show-search allow-clear placeholder="按开班日期或来源标识查找"
      @search="search" @dropdown-visible-change="opened=>opened && search(searchText)" @change="picked">
      <template #notFoundContent>{{ loading ? '正在读取名单' : '没有匹配的已同步名单' }}</template>
    </a-select>
    <small v-if="!manual && error" class="practice-roster-error" role="alert">{{ error }}</small>
    <small v-if="manual" class="practice-roster-help">名单尚未同步时，可先登记标识，保存后到分润试算拉取。</small>
  </div>
</template>

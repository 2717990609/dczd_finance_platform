<script setup>
import { computed, ref, watch } from 'vue'

const props = defineProps({ rule: Object, api: Function, formatTime: Function })
const emit = defineEmits(['close', 'published'])
const sheet = ref()
const selected = ref()
const filter = ref('')
const effectiveFrom = ref('')
const edits = ref({})
const loading = ref(false)
const saving = ref(false)
const error = ref('')
let sequence = 0

const columns = [
  {title:'销售公司',key:'company',dataIndex:'company',width:190},
  {title:'销售关系',key:'relationship',dataIndex:'relationship',width:100},
  {title:'销售类别',key:'category',dataIndex:'category',width:170},
  {title:'比例（%）',key:'rate',width:130},
]
const componentOptions = computed(() => (sheet.value?.components || []).map(item => ({value:item.itemKey,label:item.label})))
const component = computed(() => sheet.value?.components.find(item => item.itemKey === selected.value))
const dateInput = value => value ? new Date(Date.parse(value)+8*60*60*1000).toISOString().slice(0,16) : undefined
const rows = computed(() => {
  const term = filter.value.trim().toLowerCase()
  return (component.value?.rows || []).filter(row => !term ||
    [row.company,row.relationship,row.category].some(value => value?.toLowerCase().includes(term)))
})
const cellKey = (item, row) => JSON.stringify([item,row || 0])
const displayRate = (rate, row) => {
  const key = cellKey(selected.value,row)
  return Object.hasOwn(edits.value,key) ? edits.value[key].value : (rate/100).toFixed(2)
}
function setRate(value, base, row) {
  const itemKey = selected.value
  const key = cellKey(itemKey,row)
  if (rateAsBasisPoints(value) === base) delete edits.value[key]
  else edits.value[key] = {itemKey, row, value}
}
function rateAsBasisPoints(value) {
  const match = /^(\d{1,3})(?:\.(\d{1,2}))?$/.exec(String(value ?? ''))
  if (!match) return undefined
  const rate = Number(match[1])*100 + Number((match[2] || '').padEnd(2,'0'))
  return rate <= 10000 ? rate : undefined
}

watch(() => props.rule, async rule => {
  const request = ++sequence
  sheet.value = undefined
  edits.value = {}
  filter.value = ''
  effectiveFrom.value = ''
  error.value = ''
  if (!rule) return
  loading.value = true
  try {
    const result = await props.api('/catalog-rule-rates?ruleId='+encodeURIComponent(rule.id))
    if (request !== sequence) return
    sheet.value = result
    selected.value = result.components[0]?.itemKey
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}, {immediate:true})
watch(selected, () => { filter.value = '' })

async function publish() {
  if (saving.value || !sheet.value?.mayPublish) return
  error.value = ''
  saving.value = true
  try {
    if (!effectiveFrom.value) throw new Error('请选择生效时间。')
    const changes = Object.values(edits.value).map(({itemKey,row,value}) => ({itemKey,row,rateBps:rateAsBasisPoints(value)}))
    if (changes.some(change => change.rateBps === undefined)) throw new Error('请填写 0 至 100 之间的比例，最多保留两位小数。')
    const result = await props.api('/catalog-rule-rates', {
      method:'POST',
      body:JSON.stringify({
        baseId:props.rule.id,
        effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
        changes,
      }),
    })
    emit('published',result)
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer :open="!!rule" :width="'min(760px, 100vw)'" title="产品分润费率"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-spin :spinning="loading">
      <template v-if="sheet">
        <h2>{{ sheet.productName }}</h2>
        <p class="draft-report-note">{{ sheet.label }} · v{{ sheet.version }}</p>
        <p class="draft-report-note">
          生效于 {{ formatTime(sheet.effectiveFrom) }}<span v-if="sheet.effectiveTo"> · 截止 {{ formatTime(sheet.effectiveTo) }}</span>
        </p>
        <a-form layout="vertical" @submit.prevent="publish">
          <a-form-item label="分润分项" html-for="catalog-rate-component">
            <a-select id="catalog-rate-component" v-model:value="selected" :options="componentOptions"/>
          </a-form-item>
          <a-form-item v-if="component?.rateBps != null" label="比例（%）" html-for="catalog-fixed-rate">
            <a-input-number id="catalog-fixed-rate" :value="displayRate(component.rateBps)" :disabled="!sheet.mayPublish || saving"
              :min="0" :max="100" :precision="2" string-mode @update:value="setRate($event,component.rateBps)"/>
          </a-form-item>
          <template v-if="component?.rows?.length">
            <a-form-item label="筛选费率表" html-for="catalog-rate-filter">
              <a-input id="catalog-rate-filter" v-model:value="filter" placeholder="输入销售公司、关系或类别" allow-clear/>
            </a-form-item>
            <a-table :key="selected + filter" :columns="columns" :data-source="rows" row-key="row" size="small"
              :scroll="{x:590}" :pagination="{pageSize:8,showSizeChanger:false}">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key==='company'">
                  {{ record.company }}<small v-if="record.companyFallback" class="order-version">公司兜底</small>
                </template>
                <template v-if="column.key==='category'">{{ record.category || '不限' }}</template>
                <template v-if="column.key==='rate'">
                  <a-input-number :aria-label="'第 '+record.row+' 行比例'"
                    :value="displayRate(record.rateBps,record.row)" :disabled="!sheet.mayPublish || saving || record.teamRate"
                    :min="0" :max="100" :precision="2" string-mode @update:value="setRate($event,record.rateBps,record.row)"/>
                  <small v-if="record.teamRate" class="order-version">内部团队拆分</small>
                </template>
              </template>
            </a-table>
          </template>
          <template v-if="sheet.mayPublish">
            <a-form-item label="生效时间（北京时间）" html-for="catalog-rate-effective-date"
              extra="请选择此版本适用区间内的时间。订单按付款时点匹配，已结算结果保留其规则版本。">
              <a-input id="catalog-rate-effective-date" v-model:value="effectiveFrom" type="datetime-local"
                :min="dateInput(sheet.effectiveFrom)" :max="dateInput(sheet.effectiveTo)" required/>
            </a-form-item>
            <div class="form-actions">
              <a-button :disabled="saving" @click="emit('close')">取消</a-button>
              <a-button html-type="submit" type="primary" :loading="saving" :disabled="!Object.keys(edits).length">保存并生效</a-button>
            </div>
          </template>
        </a-form>
      </template>
    </a-spin>
  </a-drawer>
</template>

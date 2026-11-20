<script setup>
import { computed, ref, watch } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import RuleImpactPreview from './RuleImpactPreview.vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'

const props = defineProps({ rule: Object, applied: Object, api: Function, formatTime: Function, formatMoney:Function, periods:Array })
const emit = defineEmits(['close', 'published'])
const sheet = ref()
const selected = ref()
const filter = ref('')
const effectiveFrom = ref('')
const edits = ref({})
const recipientEdits = ref(new Map())
const teamEdits = ref(new Map())
const selectedTeam = ref()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const impactInput = ref()
const impactBusy = ref(false)
const structure = ref({items:[],rateRows:[]})
const structureEditing = ref(false)
let sequence = 0

const columns = [
  {title:'销售公司',key:'company',dataIndex:'company',width:190},
  {title:'销售关系',key:'relationship',dataIndex:'relationship',width:100},
  {title:'销售类别',key:'category',dataIndex:'category',width:170},
  {title:'比例（%）',key:'rate',width:115,fixed:'right'},
]
const componentOptions = computed(() => (sheet.value?.components || []).map(item => ({value:item.itemKey,label:item.label})))
const component = computed(() => sheet.value?.components.find(item => item.itemKey === selected.value))
const targetKind = computed(() => component.value?.targetKind || (component.value?.recipient ? 'person' : ''))
const targetKindLabels = {seller:'订单成交销售',person:'指定个人',company:'指定公司',pool:'团队池'}
const teamSplit = computed(() => component.value?.teamSplit)
const splitRows = computed(() => {
  const split = teamSplit.value
  return split ? [
    {label:'成交销售（非团队负责人）', rate:split.sales},
    {label:'小组组长（按付款时任职）', rate:split.groupLead},
    {label:'团队负责人', rate:split.teamLead},
    {label:'服务人员', rate:split.service},
  ].filter(row=>row.rate > 0) : []
})
const teamOptions = computed(() => (sheet.value?.teams || []).map(item=>({value:item.key,label:item.label})))
const team = computed(() => sheet.value?.teams?.find(item=>item.key===selectedTeam.value))
const editable = computed(() => sheet.value?.mayPublish && !saving.value)
const peopleFilters = computed(() => ({ruleId:props.rule?.id}))
const recipientsChanged = computed(() => recipientEdits.value.size + teamEdits.value.size > 0)
const hasEdits = computed(() => recipientsChanged.value || Object.keys(edits.value).length > 0 ||
  structure.value.items.length || structure.value.rateRows.length)
const impactPeriods = computed(() => (props.periods || []).filter(period=>period.productId===sheet.value?.productId && !period.settled))
const dateInput = value => value ? new Date(Date.parse(value)+8*60*60*1000).toISOString().slice(0,16) : undefined
const percent = rateBps => (rateBps/100).toLocaleString('zh-CN',{maximumFractionDigits:2})+'%'
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
const recipient = computed(() => recipientEdits.value.get(selected.value) || component.value?.recipient)
function setRecipient(id, option) {
  if (id===component.value.recipient.id) recipientEdits.value.delete(selected.value)
  else recipientEdits.value.set(selected.value,{id,name:option.name})
}
const teamPerson = field => teamEdits.value.get(selectedTeam.value)?.[field] || team.value?.[field]
function setTeamPerson(field, id, option) {
  const key = selectedTeam.value
  const changes = {...teamEdits.value.get(key)}
  if (id===team.value[field].id) delete changes[field]
  else changes[field] = {id,name:option.name}
  if (Object.keys(changes).length) teamEdits.value.set(key,changes)
  else teamEdits.value.delete(key)
}

watch(() => [props.rule, props.applied], async ([rule, applied]) => {
  const request = ++sequence
  sheet.value = undefined
  edits.value = {}
  recipientEdits.value.clear()
  teamEdits.value.clear()
  selectedTeam.value = undefined
  filter.value = ''
  effectiveFrom.value = ''
  error.value = ''
  impactInput.value = undefined
  structure.value = {items:[],rateRows:[]}
  structureEditing.value = false
  if (applied) {
    const result = applied.catalog
    sheet.value = result
    selected.value = result.components.find(item=>item.itemKey===applied.component || applied.component.startsWith(item.itemKey+':'))?.itemKey
      || result.components[0]?.itemKey
    selectedTeam.value = result.teams?.[0]?.key
    loading.value = false
    return
  }
  if (!rule) return
  loading.value = true
  try {
    const result = await props.api('/catalog-rule-rates?ruleId='+encodeURIComponent(rule.id))
    if (request !== sequence) return
    sheet.value = result
    selected.value = result.components[0]?.itemKey
    selectedTeam.value = result.teams?.[0]?.key
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}, {immediate:true})
watch(selected, () => { filter.value = '' })

function publicationInput() {
  if (structureEditing.value) throw new Error('请先暂存正在编辑的配置。')
  if (!effectiveFrom.value) throw new Error('请选择生效时间。')
  const changes = Object.values(edits.value).map(({itemKey,row,value}) => ({itemKey,row,rateBps:rateAsBasisPoints(value)}))
  if (changes.some(change => change.rateBps === undefined)) throw new Error('请填写 0 至 100 之间的比例，最多保留两位小数。')
  return {
    baseId:props.rule.id,
    effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
    changes,
    recipientChanges:Array.from(recipientEdits.value,([itemKey, person])=>({itemKey,personId:person.id})),
    teamChanges:Array.from(teamEdits.value,([teamKey,changes])=>({
      teamKey,headPersonId:changes.head?.id,servicePersonId:changes.service?.id,
    })),
    newItems:structure.value.items.map(({personName,...item})=>item),
    newRateRows:structure.value.rateRows.map(({itemLabel,...row})=>row),
  }
}
function previewImpact() {
  error.value = ''
  try {
    impactInput.value = publicationInput()
  } catch (err) {
    error.value = err.message
  }
}
function compareCatalogRule(periodId, signal) {
  return props.api('/catalog-rule-impact',{method:'POST',body:JSON.stringify({periodId,rule:impactInput.value}),signal})
}
async function publish() {
  if (saving.value || !sheet.value?.mayPublish) return
  error.value = ''
  saving.value = true
  try {
    const result = await props.api('/catalog-rule-rates', {
      method:'POST',
      body:JSON.stringify(publicationInput()),
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
  <a-drawer :open="!!(rule || applied)" :width="'min(760px, 100vw)'" :title="applied ? '计提使用的规则' : '产品分润配置'"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <section :aria-label="applied ? '计提使用的规则' : '产品分润配置'">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-spin :spinning="loading">
      <template v-if="sheet">
        <h2>{{ sheet.productName }}</h2>
        <p class="draft-report-note">{{ sheet.label }} · v{{ sheet.version }}</p>
        <p class="draft-report-note">
          生效于 {{ formatTime(sheet.effectiveFrom) }}<span v-if="sheet.effectiveTo"> · 截止 {{ formatTime(sheet.effectiveTo) }}</span>
        </p>
        <p v-if="sheet.requiresCompletion" class="draft-report-note">规则按付款时间匹配。订单确认完成后计提。</p>
        <a-form v-show="!impactInput" layout="vertical" @submit.prevent="publish">
          <a-form-item label="分润分项" html-for="catalog-rate-component">
            <a-select id="catalog-rate-component" v-model:value="selected" :options="componentOptions"/>
          </a-form-item>
          <h3 v-if="component" class="form-subhead">发给谁</h3>
          <p v-if="targetKind && !recipient && !component?.targetLabel" class="rule-formula">
            {{ targetKindLabels[targetKind] }}<template v-if="teamSplit"> · 内部团队按岗位拆分</template>
          </p>
          <a-form-item v-if="recipient" label="指定收款人" html-for="catalog-benefit-person">
            <AppointmentPersonSelect v-if="sheet.mayPublish" id="catalog-benefit-person" :key="selected"
              :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters" :current="recipient" :value="recipient.id"
              :disabled="!editable" :placeholder="recipient.id?'输入姓名搜索':recipient.name" @change="setRecipient"/>
            <span v-else>{{ recipient.name }}</span>
          </a-form-item>
          <a-form-item v-if="component?.targetLabel" :label="targetKindLabels[targetKind] || '收款对象'">{{ component.targetLabel }}</a-form-item>
          <h3 v-if="component" class="form-subhead">提成怎么算</h3>
          <p v-if="component?.rateBps != null || component?.rows?.length" class="rule-formula">
            每笔付款实收 × {{ component.rows?.length ? '命中的费率' : '下方比例' }}
          </p>
          <a-form-item v-if="component?.rateBps != null" label="比例（%）" html-for="catalog-fixed-rate">
            <a-input-number v-if="!applied" id="catalog-fixed-rate" :value="displayRate(component.rateBps)" :disabled="!sheet.mayPublish || saving"
              :min="0" :max="100" :precision="2" string-mode @update:value="setRate($event,component.rateBps)"/>
            <span v-else>{{ displayRate(component.rateBps) }}%</span>
          </a-form-item>
          <template v-if="component?.rows?.length">
            <a-form-item label="筛选费率表" html-for="catalog-rate-filter">
              <a-input id="catalog-rate-filter" v-model:value="filter" placeholder="输入销售公司、关系或类别" allow-clear/>
            </a-form-item>
            <a-table :key="selected + filter" :columns="columns" :data-source="rows" row-key="row" size="small"
              :scroll="{x:575}" :pagination="{pageSize:8,showSizeChanger:false,hideOnSinglePage:true}">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key==='company'">
                  {{ record.company }}<small v-if="record.companyFallback" class="order-version">公司兜底</small>
                </template>
                <template v-if="column.key==='category'">{{ record.category || '不限' }}</template>
                <template v-if="column.key==='rate'">
                  <a-input-number v-if="!applied" :aria-label="'第 '+record.row+' 行比例'"
                    :value="displayRate(record.rateBps,record.row)" :disabled="!sheet.mayPublish || saving || record.teamRate"
                    :min="0" :max="100" :precision="2" string-mode @update:value="setRate($event,record.rateBps,record.row)"/>
                  <span v-else>{{ displayRate(record.rateBps,record.row) }}%</span>
                  <small v-if="record.teamRate" class="order-version">内部团队拆分</small>
                </template>
              </template>
            </a-table>
          </template>
          <a-collapse v-if="team" ghost>
            <a-collapse-panel key="team" header="内部团队收款人">
              <a-form-item label="销售公司" html-for="catalog-team-company">
                <a-select id="catalog-team-company" v-model:value="selectedTeam" :options="teamOptions" show-search option-filter-prop="label"/>
              </a-form-item>
              <a-form-item label="团队负责人" html-for="catalog-team-head">
                <AppointmentPersonSelect v-if="sheet.mayPublish" id="catalog-team-head" :key="selectedTeam"
                  :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters" :current="teamPerson('head')" :value="teamPerson('head')?.id"
                  :disabled="!editable" :placeholder="teamPerson('head')?.id?'输入姓名搜索':teamPerson('head')?.name" @change="(id,option)=>setTeamPerson('head',id,option)"/>
                <span v-else>{{ team.head.name }}</span>
              </a-form-item>
              <a-form-item v-if="team.service" label="服务人员" html-for="catalog-team-service">
                <AppointmentPersonSelect v-if="sheet.mayPublish" id="catalog-team-service" :key="selectedTeam"
                  :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters" :current="teamPerson('service')" :value="teamPerson('service')?.id"
                  :disabled="!editable" :placeholder="teamPerson('service')?.id?'输入姓名搜索':teamPerson('service')?.name" @change="(id,option)=>setTeamPerson('service',id,option)"/>
                <span v-else>{{ team.service.name }}</span>
              </a-form-item>
              <template v-if="teamSplit">
                <a-descriptions size="small" bordered :column="1">
                  <a-descriptions-item v-for="row in splitRows" :key="row.label" :label="row.label">
                    {{ percent(row.rate) }}
                  </a-descriptions-item>
                  <a-descriptions-item label="销售分项总比例">{{ percent(teamSplit.total) }}</a-descriptions-item>
                </a-descriptions>
                <p class="rule-formula">
                  团队负责人亲自成交时，成交份额为 {{ percent(teamSplit.headSales) }}，另计团队负责人及服务份额。
                </p>
              </template>
            </a-collapse-panel>
          </a-collapse>
          <template v-if="sheet.mayPublish">
            <CatalogRuleStructureEditor v-if="sheet.structureOptions" :key="rule.id"
              v-model="structure" v-model:editing="structureEditing" :options="sheet.structureOptions" :component="component"
              :api="api" :people-filters="peopleFilters" :parse-rate="rateAsBasisPoints"/>
            <a-form-item label="生效时间（北京时间）" html-for="catalog-rate-effective-date"
              :extra="recipientsChanged
                ? '请选择规则生效时间，并在计提任职核对有关人员的有效区间。已结算结果保留确认时的规则。'
                : '请选择此版本适用区间内的时间。订单按付款时点匹配，已结算结果保留其规则版本。'">
              <a-input id="catalog-rate-effective-date" v-model:value="effectiveFrom" type="datetime-local"
                :min="dateInput(sheet.effectiveFrom)" :max="dateInput(sheet.effectiveTo)" required/>
            </a-form-item>
            <div class="form-actions">
              <a-button :disabled="saving" @click="emit('close')">取消</a-button>
              <a-button :disabled="saving || structureEditing || !hasEdits" @click="previewImpact">预览差额</a-button>
              <a-button html-type="submit" type="primary" :loading="saving" :disabled="structureEditing || !hasEdits">保存并生效</a-button>
            </div>
          </template>
        </a-form>
        <template v-if="impactInput">
          <RuleImpactPreview v-model:busy="impactBusy" :periods="impactPeriods" :simulate="compareCatalogRule" :format-money="formatMoney"/>
          <div class="form-actions">
            <a-button :disabled="saving" @click="impactInput=undefined; error=''">返回编辑</a-button>
            <a-button type="primary" :loading="saving" :disabled="impactBusy" @click="publish">保存并生效</a-button>
          </div>
        </template>
      </template>
    </a-spin>
    <div v-if="applied" class="form-actions"><a-button @click="emit('close')">关闭</a-button></div>
    </section>
  </a-drawer>
</template>

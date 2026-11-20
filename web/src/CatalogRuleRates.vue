<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { DownOutlined } from '@ant-design/icons-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import RuleImpactPreview from './RuleImpactPreview.vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import { catalogFlatMethods, catalogPayeeComplete, catalogSalesPayees, moneyAsMinor, rateAsBasisPoints } from './rule-component-form'

const props = defineProps({ rule: Object, applied: Object, api: Function, formatTime: Function, formatMoney:Function, periods:Array, skus:Array })
const emit = defineEmits(['close', 'published'])
const sheet = ref()
const selected = ref()
const filter = ref('')
const effectiveFrom = ref('')
const edits = ref({})
const labelEdits = ref(new Map())
const targetEdits = ref(new Map())
const targetDraft = ref()
const teamEdits = ref(new Map())
const selectedTeam = ref()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const impactInput = ref()
const impactBusy = ref(false)
const structure = ref({items:[],rateRows:[],calculations:[]})
const structureEditing = ref(false)
const structureEditor = ref()
const removedItems = ref(new Set())
const removedRateRows = ref(new Map())
const copy = ref()
const more = ref([])
const targetSkus = computed(()=>(props.skus || []).filter(sku=>sku.code!==props.rule?.scopeId)
  .map(sku=>({value:sku.code,label:sku.label})))
let sequence = 0

const rateColumns = [
  {title:'销售公司',key:'company',dataIndex:'company',width:190},
  {title:'销售类别',key:'category',dataIndex:'category',width:170},
  {title:'销售关系',key:'relationship',dataIndex:'relationship',width:110},
  {title:'实收比例（%）',key:'rate',width:126},
  {title:'收款方',key:'payee',width:128},
]
const component = computed(() => sheet.value?.components.find(item => item.itemKey === selected.value))
const calculationFor = itemKey => structure.value.calculations.find(change=>change.itemKey===itemKey)
const selectedCalculation = computed(() => calculationFor(selected.value))
const hasRateTable = computed(() => !!(selectedCalculation.value || component.value)?.rows?.length)
const columns = computed(() => sheet.value?.mayPublish && !props.applied && !selectedCalculation.value
  ? [...rateColumns,{title:'操作',key:'operation',width:190}] : rateColumns)
const targetKind = computed(() => targetEdits.value.get(selected.value)?.target || component.value?.targetKind)
const targetKindLabels = {seller:'按下表结算',person:'指定个人',company:'指定公司',pool:'团队池',referrer:'订单直接推荐人'}
const targetShortLabels = {seller:'成交销售',person:'个人',company:'公司',pool:'团队池',referrer:'推荐人'}
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
const itemRemoved = computed(() => removedItems.value.has(selected.value))
const editable = computed(() => sheet.value?.mayPublish && !saving.value && !itemRemoved.value)
const peopleFilters = computed(() => ({ruleId:props.rule?.id}))
const recipientsChanged = computed(() => targetEdits.value.size + teamEdits.value.size > 0)
const hasEdits = computed(() => copy.value || recipientsChanged.value || Object.keys(edits.value).length > 0 || labelEdits.value.size ||
  structure.value.items.length || structure.value.rateRows.length || structure.value.calculations.length ||
  removedItems.value.size || removedRateRows.value.size)
const impactPeriods = computed(() => (props.periods || []).filter(period=>period.productId===sheet.value?.productId && !period.settled))
const dateInput = value => value ? new Date(Date.parse(value)+8*60*60*1000).toISOString().slice(0,16) : undefined
const percent = rateBps => (rateBps/100).toLocaleString('zh-CN',{maximumFractionDigits:2})+'%'
const canAddRates = computed(() => sheet.value?.mayPublish && !props.applied && !!sheet.value.structureOptions &&
  !itemRemoved.value && !selectedCalculation.value)
const rateNames = computed(() => Object.fromEntries(['companies','categories','relationships'].map(kind=>
  [kind,new Map((sheet.value?.structureOptions?.[kind] || []).map(option=>[option.value,option.label]))])))
function stagedRateRow(change, original) {
  const {companyIds,categoryIds,relationship,payee,rateBps,draftKey} = change
  const names = rateNames.value
  const labelList = (ids, labels, missing) => ids?.map(id=>labels.get(id) || missing).join('、')
  return {
    ...original,draftKey,companyIds,categoryIds,payee,rateBps,staged:true,
    scope:companyIds?.length ? 'company' : categoryIds?.length ? 'category' : 'general',
    company:labelList(companyIds,names.companies,'公司名称待补'),
    category:labelList(categoryIds,names.categories,'销售类别名称待补'),
    relationshipCode:relationship,limitsRelationship:relationship!=='ANY',
    relationship:names.relationships.get(relationship) || original?.relationship || relationship,
  }
}
const rateRows = computed(() => {
  if (selectedCalculation.value) {
    return (selectedCalculation.value.rows || []).map((row,index)=>stagedRateRow({...row,draftKey:'method-'+index}))
  }
  const stored = component.value?.rows || []
  const staged = structure.value.rateRows.filter(row=>row.itemKey===selected.value)
  if (!staged.length) return stored
  const byRow = new Map(stored.map(row=>[row.row,row]))
  const replaced = new Set(staged.map(row=>row.row))
  return [...staged.map(change=>stagedRateRow(change,byRow.get(change.row))),...stored.filter(row=>!replaced.has(row.row))]
})
const rateGroups = computed(() => {
  const mayAdd = canAddRates.value
  return [
    {key:'category',title:'销售类别默认',add:mayAdd && sheet.value.structureOptions.categories.length && '添加类别默认比例'},
    {key:'company',title:'公司特例',add:mayAdd && '添加公司特例'},
    {key:'inherited',title:'公司补充比例'},
    {key:'general',title:'通用比例',add:mayAdd && '添加通用比例'},
  ]
    .map(group=>({...group,rows:rateRows.value.filter(row=>row.scope===group.key)}))
    .filter(group=>group.add || group.rows.length)
})
const rateGroup = ref()
const rateTableRevision = ref(0)
const rateRowKey = row => row.draftKey || row.row
const rateEditorTarget = computed(() => '#'+CSS.escape('catalog-rates-edit-'+selected.value))
watch([selected,rateGroups], ([,groups]) => {
  if (!groups.some(group=>group.key===rateGroup.value)) rateGroup.value = groups[0]?.key
})
watch(selected, () => {
  rateGroup.value = rateGroups.value[0]?.key
  more.value = []
})
const ratePriority = computed(() => {
  const titles = ['company','category','inherited','general']
    .map(key=>rateGroups.value.find(group=>group.key===key))
    .filter(group=>group && (group.rows.length || group.key!=='general'))
    .map(group=>group.title)
  return titles.length>1 ? titles.join(' → ') : ''
})
const activeRateGroup = computed(() => rateGroups.value.find(group=>group.key===rateGroup.value))
const displayedRates = computed(() => {
  const term = filter.value.trim().toLowerCase()
  return (activeRateGroup.value?.rows || []).filter(row => !term ||
    [row.company,row.relationship,row.category].some(value => value?.toLowerCase().includes(term)))
})
const displayedColumns = computed(() => columns.value.filter(col=>
  (col.key!=='company' || ['company','inherited'].includes(rateGroup.value)) &&
  (col.key!=='category' || activeRateGroup.value?.rows.some(row=>row.category)) &&
  (col.key!=='relationship' || activeRateGroup.value?.rows.some(row=>row.limitsRelationship)) &&
  (col.key!=='payee' || component.value?.targetKind==='seller')))
const tableWidth = computed(() => displayedColumns.value.reduce((width,column)=>width+column.width,0))
async function addRecipient() {
  selected.value = undefined
  await nextTick()
  await structureEditor.value?.addRecipient()
}
async function editRateScope(row) {
  await nextTick()
  structureEditor.value?.editRateScope(row,row.staged ? String(row.rateBps/100) : displayRate(row.rateBps,row.row))
}
const pendingScope = (itemKey, row) => row && structure.value.rateRows.find(edit=>edit.itemKey===itemKey && edit.row===row)
const salesPayeeLabel = row => row.teamRate ? '团队岗位拆分'
  : catalogSalesPayees.find(choice=>choice.value===(row.payee || 'seller'))?.label
const cellKey = (item, row) => JSON.stringify([item,row || 0])
function toggleItemRemoval() {
  const key = selected.value
  if (removedItems.value.delete(key)) return
  removedItems.value.add(key)
}
function toggleRowRemoval(row) {
  const itemKey = selected.value
  const key = cellKey(itemKey,row)
  if (!removedRateRows.value.delete(key)) removedRateRows.value.set(key,{itemKey,row})
}
function showRateGroup(scope) {
  rateGroup.value = scope
  filter.value = ''
  rateTableRevision.value++
}
function discardStagedRate(row) {
  const index = structure.value.rateRows.findIndex(change=>change.itemKey===selected.value && rateRowKey(change)===rateRowKey(row))
  if (index<0) return
  structure.value.rateRows.splice(index,1)
  const original = component.value.rows.find(entry=>entry.row===row.row)
  if (original && original.scope!==row.scope) showRateGroup(original.scope)
}
function undoRateChange(row) {
  if (row.staged) discardStagedRate(row)
  else delete edits.value[cellKey(selected.value,row.row)]
}
const displayRate = (rate, row, item=selected.value) => {
  const key = cellKey(item,row)
  return Object.hasOwn(edits.value,key) ? edits.value[key].value : (rate/100).toFixed(2)
}
const savedMethod = item => item.fixedMinor == null ? 'rate' : 'fixed'
const flatMethod = item => edits.value[cellKey(item.itemKey)]?.method || savedMethod(item)
const flatValue = item => {
  const edit = edits.value[cellKey(item.itemKey)]
  return edit ? edit.value : ((item.fixedMinor ?? item.rateBps)/100).toFixed(2)
}
function setFlat(item, method, value) {
  const key = cellKey(item.itemKey)
  const parsed = method==='fixed' ? moneyAsMinor(value) : rateAsBasisPoints(value)
  if (method===savedMethod(item) && parsed===(item.fixedMinor ?? item.rateBps)) delete edits.value[key]
  else edits.value[key] = {itemKey:item.itemKey,method,value}
}
function componentStandard(item) {
  const calculation = calculationFor(item.itemKey)
  if (calculation) {
    if (calculation.rows?.length) return '按销售条件取比例'
    if (calculation.fixedMinor!=null) return (calculation.fixedMinor/100).toFixed(2)+' 元/笔'
    return percent(calculation.rateBps)
  }
  if (item.rows?.length) return '按销售条件取比例'
  if (item.fixedMinor == null && item.rateBps == null) return ''
  const value = flatValue(item)
  return value == null || value==='' ? '' : value+(flatMethod(item)==='fixed' ? ' 元/笔' : '%')
}
function undoCalculation() {
  const index = structure.value.calculations.indexOf(selectedCalculation.value)
  if (index>=0) structure.value.calculations.splice(index,1)
}
function setLabel(item, label) {
  if (label.trim()===item.label) labelEdits.value.delete(item.itemKey)
  else labelEdits.value.set(item.itemKey,label)
}
function setRate(value, base, row) {
  const itemKey = selected.value
  const key = cellKey(itemKey,row)
  if (rateAsBasisPoints(value) === base) delete edits.value[key]
  else edits.value[key] = {itemKey, row, value}
}
function targetName(item) {
  const edit = targetEdits.value.get(item.itemKey)
  if (!edit) return item.recipient?.name || item.targetLabel
  if (edit.target==='person') return edit.personName
  return sheet.value.structureOptions.organizations.find(org=>org.value===edit.organization)?.label
}
function originalTarget(item) {
  return {target:item.targetKind,role:item.targetRole,organization:item.targetOrganization,
    personId:item.recipient?.id,personName:item.recipient?.name}
}
function beginTarget() {
  error.value = ''
  targetDraft.value = {...(targetEdits.value.get(selected.value) || originalTarget(component.value))}
}
function stageTarget() {
  const payee = targetDraft.value
  if (!catalogPayeeComplete(payee)) {
    error.value = '请选择收款对象并填写对应岗位。'
    return
  }
  const original = originalTarget(component.value)
  if (['target','role','organization','personId'].every(field=>payee[field]===original[field])) {
    targetEdits.value.delete(selected.value)
  } else {
    targetEdits.value.set(selected.value,payee)
  }
  targetDraft.value = undefined
  error.value = ''
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
  copy.value = undefined
  more.value = []
  edits.value = {}
  labelEdits.value.clear()
  removedItems.value.clear()
  removedRateRows.value.clear()
  targetEdits.value.clear()
  targetDraft.value = undefined
  teamEdits.value.clear()
  selectedTeam.value = undefined
  filter.value = ''
  effectiveFrom.value = ''
  error.value = ''
  impactInput.value = undefined
  structure.value = {items:[],rateRows:[],calculations:[]}
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
    selected.value = result.components.length===1 ? result.components[0].itemKey : undefined
    selectedTeam.value = result.teams?.[0]?.key
  } catch (err) {
    if (request === sequence) error.value = err.message
  } finally {
    if (request === sequence) loading.value = false
  }
}, {immediate:true})
watch([selected, rateGroup], () => { filter.value = '' })

function publicationInput() {
  if (structureEditing.value || targetDraft.value) throw new Error('请先完成正在编辑的配置。')
  if (!effectiveFrom.value) throw new Error('请选择生效时间。')
  if (copy.value && (!copy.value.sku || !copy.value.label?.trim())) throw new Error('请选择目标 SKU，并填写规则名称。')
  const changes = Object.values(edits.value)
    .filter(({itemKey,row})=>!removedItems.value.has(itemKey) && !removedRateRows.value.has(cellKey(itemKey,row)) &&
      !pendingScope(itemKey,row) && !calculationFor(itemKey))
    .map(({itemKey,row,value,method}) => {
      if (method==='fixed') {
        const fixedMinor = moneyAsMinor(value)
        if (fixedMinor===undefined) throw new Error('请填写有效金额，最多保留两位小数。')
        return {itemKey,fixedMinor}
      }
      const rateBps = rateAsBasisPoints(value)
      if (rateBps===undefined) throw new Error('请填写 0 至 100 之间的比例，最多保留两位小数。')
      return {itemKey,row,rateBps}
    })
  for (const [itemKey,value] of labelEdits.value) {
    if (removedItems.value.has(itemKey)) continue
    const label = value.trim()
    if (!label) throw new Error('请填写收款安排名称。')
    changes.push({itemKey,label})
  }
  return {
    baseId:props.rule.id,
    copy:copy.value,
    effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
    changes,
    targetChanges:Array.from(targetEdits.value,([itemKey, {personName,...payee}])=>({itemKey,...payee}))
      .filter(({itemKey})=>!removedItems.value.has(itemKey)),
    teamChanges:Array.from(teamEdits.value,([teamKey,changes])=>({
      teamKey,headPersonId:changes.head?.id,servicePersonId:changes.service?.id,
    })),
    newItems:structure.value.items.map(({personName,...item})=>item),
    calculationChanges:structure.value.calculations.filter(({itemKey})=>!removedItems.value.has(itemKey)),
    newRateRows:structure.value.rateRows.filter(({itemKey,row})=>!removedItems.value.has(itemKey) &&
      !removedRateRows.value.has(cellKey(itemKey,row)) && !calculationFor(itemKey))
      .map(({draftKey,...row})=>row),
    removedItems:[...removedItems.value],
    removedRateRows:[...removedRateRows.value.values()].filter(row=>!removedItems.value.has(row.itemKey) && !calculationFor(row.itemKey)),
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
    emit('published',result,copy.value?.sku)
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer :open="!!(rule || applied)" :width="'min(1260px, 100vw)'" :title="applied ? '计提使用的规则' : '产品分润配置'" root-class-name="finance-rule-editor"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <section :aria-label="applied ? '计提使用的规则' : '产品分润配置'">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-spin :spinning="loading">
      <template v-if="sheet">
        <header class="catalog-rate-summary">
          <div>
            <h2>{{ rule?.scopeLabel || sheet.productName }}</h2>
            <p v-if="sheet.label!==(rule?.scopeLabel || sheet.productName)">{{ sheet.label }}</p>
          </div>
          <div class="catalog-rate-context">
            <a-tag>v{{ sheet.version }}</a-tag>
            <span>{{ formatTime(sheet.effectiveFrom) }} 生效<template v-if="sheet.effectiveTo"> · 截至 {{ formatTime(sheet.effectiveTo) }}</template></span>
            <span v-if="sheet.requiresCompletion">订单确认完成后计提</span>
          </div>
        </header>
        <a-form v-show="!impactInput" id="catalog-rate-form" layout="vertical" @submit.prevent="publish">
          <div class="rule-blocks" aria-label="收款安排">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">收款安排</h3>
            <a-button v-if="sheet.mayPublish && !applied && sheet.structureOptions" size="small" :disabled="saving || structureEditing || !!targetDraft"
              @click="addRecipient">添加收款安排</a-button>
          </div>
          <section v-for="item in sheet.components" :key="item.itemKey" class="rule-block">
          <button type="button" class="rule-block-header" :disabled="saving || structureEditing || !!targetDraft"
            :aria-expanded="selected===item.itemKey" :aria-controls="'catalog-rate-'+item.itemKey"
            @click="selected=selected===item.itemKey ? undefined : item.itemKey">
            <DownOutlined aria-hidden="true"/>
            <strong>{{ labelEdits.get(item.itemKey)?.trim() || item.label }}</strong>
            <span class="rule-block-badge">{{ removedItems.has(item.itemKey) ? '待删除' : targetShortLabels[targetEdits.get(item.itemKey)?.target || item.targetKind] }}</span>
            <small>{{ [targetName(item),componentStandard(item)].filter(Boolean).join(' · ') }}</small>
          </button>
          <div v-if="selected===item.itemKey" :id="'catalog-rate-'+item.itemKey" class="rule-block-body">
          <a-alert v-if="itemRemoved" type="warning" message="此分项将从新版移除" show-icon class="feedback"/>
          <div v-if="targetKind!=='seller' || !component?.rows?.length" class="rule-blocks-heading catalog-receipt">
            <span v-if="!targetDraft">{{ targetKindLabels[targetKind] }}<template v-if="targetName(item)"> · {{ targetName(item) }}</template></span>
            <a-space v-if="!applied && sheet.structureOptions && targetKind!=='seller' && !targetDraft" wrap :size="4">
              <a-button size="small" :disabled="!editable || structureEditing" @click="beginTarget">编辑收款对象</a-button>
              <a-button size="small" :disabled="!editable || structureEditing" @click="structureEditor?.editCalculation(selectedCalculation)">
                {{ selectedCalculation ? '编辑新计提方式' : '更换计提方式' }}
              </a-button>
              <a-button v-if="selectedCalculation" size="small" type="link" :disabled="!editable || structureEditing"
                @click="undoCalculation">撤销更换</a-button>
            </a-space>
          </div>
          <template v-if="targetDraft">
            <div class="rule-basics">
              <CatalogPayeeFields v-model="targetDraft" prefix="catalog-edit" :options="sheet.structureOptions" :api="api"
                :people-filters="peopleFilters" :disabled="saving"/>
            </div>
            <a-space class="feedback">
              <a-button @click="targetDraft=undefined; error=''">取消修改</a-button>
              <a-button type="primary" @click="stageTarget">确认收款对象</a-button>
            </a-space>
          </template>
          <p v-if="selectedCalculation && !hasRateTable" class="rule-formula">
            {{ selectedCalculation.fixedMinor!=null ? '每笔付款固定金额' : '统一比例' }} · {{ componentStandard(item) }} · 未保存
          </p>
          <template v-else-if="!selectedCalculation && (item.fixedMinor != null || item.rateBps != null)">
            <div v-if="!applied && sheet.mayPublish" class="rule-basics">
              <a-form-item label="计提方式" html-for="catalog-flat-method">
                <a-select id="catalog-flat-method" :value="flatMethod(item)" :options="catalogFlatMethods" :disabled="!editable"
                  @update:value="setFlat(item,$event,$event===savedMethod(item) ? (item.fixedMinor ?? item.rateBps)/100 : undefined)"/>
              </a-form-item>
              <a-form-item v-if="flatMethod(item)==='fixed'" label="金额（元/笔）" html-for="catalog-fixed-amount">
                <a-input-number id="catalog-fixed-amount" aria-label="金额（元/笔）" :value="flatValue(item)" :disabled="!editable"
                  :min="0" :precision="2" string-mode @update:value="setFlat(item,'fixed',$event)"/>
              </a-form-item>
              <a-form-item v-else label="比例（%）" html-for="catalog-fixed-rate">
                <a-input-number id="catalog-fixed-rate" :value="flatValue(item)" :disabled="!editable"
                  :min="0" :max="100" :precision="2" string-mode @update:value="setFlat(item,'rate',$event)"/>
              </a-form-item>
            </div>
            <p v-else class="rule-formula">{{ componentStandard(item) }}</p>
          </template>
          <div v-if="hasRateTable" class="catalog-rate-toolbar">
              <a-radio-group v-if="rateGroups.length>1" v-model:value="rateGroup" button-style="solid" size="small"
                :disabled="structureEditing" role="radiogroup" aria-label="查看比例">
                <a-radio-button v-for="group in rateGroups" :key="group.key" :value="group.key">
                  <span>{{ group.title }}</span><small class="finance-rate-count">{{ group.rows.length }}</small>
                </a-radio-button>
              </a-radio-group>
              <strong v-else-if="activeRateGroup">{{ activeRateGroup.title }}</strong>
              <small v-if="ratePriority" class="finance-rate-priority">优先匹配 {{ ratePriority }}</small>
              <a-input v-if="activeRateGroup?.rows.length>8" id="catalog-rate-filter" v-model:value="filter" size="small"
                placeholder="搜索公司或销售类别" aria-label="搜索比例表" allow-clear/>
              <a-button v-if="activeRateGroup?.add" class="catalog-rate-add" size="small" :disabled="!editable || structureEditing || !!targetDraft"
                @click="structureEditor?.addRateCondition(activeRateGroup.key)">{{ activeRateGroup.add }}</a-button>
          </div>
          <div v-if="sheet.mayPublish && !applied && !itemRemoved" :id="'catalog-rates-edit-'+item.itemKey"/>
          <template v-if="hasRateTable">
            <p v-if="!activeRateGroup?.rows.length" class="catalog-rate-empty">暂无{{ activeRateGroup?.title }}</p>
            <a-table v-else :key="selected + filter + rateGroup + rateTableRevision" class="catalog-rate-table"
              :columns="displayedColumns" :data-source="displayedRates" :row-key="rateRowKey" size="small"
              :scroll="{x:tableWidth}" :pagination="{pageSize:8,showSizeChanger:false,hideOnSinglePage:true}">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key==='company'">
                  {{ record.company }}
                </template>
                <template v-if="column.key==='category'">{{ record.category || '不限' }}</template>
                <template v-if="column.key==='rate'">
                  <a-input-number v-if="!applied && !record.staged" :aria-label="'第 '+record.row+' 行比例'" size="small"
                    :value="displayRate(record.rateBps,record.row)"
                    :disabled="!editable || record.teamRate || removedRateRows.has(cellKey(selected,record.row))"
                    :min="0" :max="100" :precision="2" string-mode @update:value="setRate($event,record.rateBps,record.row)"/>
                  <span v-else>{{ applied ? displayRate(record.rateBps,record.row)+'%' : percent(record.rateBps) }}</span>
                  <small v-if="record.staged || Object.hasOwn(edits,cellKey(selected,record.row))" class="order-version">未保存</small>
                  <small v-if="record.teamRate" class="order-version">内部团队拆分</small>
                </template>
                <span v-else-if="column.key==='payee'">{{ salesPayeeLabel(record) }}</span>
                <template v-if="column.key==='operation' && !itemRemoved">
                  <a-button v-if="removedRateRows.has(cellKey(selected,record.row))" type="link" size="small"
                    :disabled="saving || structureEditing" @click="toggleRowRemoval(record.row)">撤销删除</a-button>
                  <template v-else>
                    <a-button v-if="!record.teamRate && !record.companyFallback" type="link" size="small" :disabled="saving || structureEditing"
                      @click="editRateScope(record)">编辑范围</a-button>
                    <a-button v-if="record.row && (record.staged || Object.hasOwn(edits,cellKey(selected,record.row)))"
                      type="link" size="small" :disabled="saving || structureEditing" @click="undoRateChange(record)">撤销修改</a-button>
                    <a-button type="link" danger size="small" :disabled="saving || structureEditing || record.teamRate"
                      @click="record.draftKey ? discardStagedRate(record) : toggleRowRemoval(record.row)">删除</a-button>
                  </template>
                </template>
              </template>
            </a-table>
          </template>
          <a-collapse v-if="team && teamSplit" ghost class="catalog-more">
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
          </div>
          </section>
          </div>
          <template v-if="sheet.mayPublish && !applied">
            <Teleport v-if="sheet.structureOptions" :disabled="!component || itemRemoved"
              :to="component && !itemRemoved ? rateEditorTarget : 'body'">
              <CatalogRuleStructureEditor ref="structureEditor" :key="rule.id"
                v-model="structure" v-model:editing="structureEditing" :options="sheet.structureOptions" :component="itemRemoved ? undefined : component"
                :api="api" :people-filters="peopleFilters" :parse-rate="rateAsBasisPoints" @rate-staged="showRateGroup"/>
            </Teleport>
            <a-collapse v-model:active-key="more" ghost class="catalog-more">
              <a-collapse-panel key="more" header="更多配置">
                <a-form-item v-if="component && !itemRemoved" label="收款安排名称" html-for="catalog-rate-label" class="catalog-rate-label">
                  <a-input id="catalog-rate-label" :value="labelEdits.get(component.itemKey) ?? component.label" :maxlength="80"
                    :disabled="!editable || structureEditing || !!targetDraft" @update:value="setLabel(component,$event)"/>
                </a-form-item>
                <div class="topbar-actions">
                  <a-button v-if="component && targetKind!=='seller'" :danger="!itemRemoved" :disabled="structureEditing || saving"
                    @click="toggleItemRemoval">{{ itemRemoved ? '撤销删除分项' : '删除分项' }}</a-button>
                  <a-button v-if="targetSkus.length && !copy" :disabled="saving" @click="copy={}">复制到其他 SKU</a-button>
                </div>
              </a-collapse-panel>
            </a-collapse>
            <div v-if="copy" class="catalog-copy">
              <h3>复制到其他 SKU</h3>
              <p class="rule-formula">当前填写的分润比例和收款对象会用于目标 SKU。</p>
              <div class="field-grid">
                <a-form-item label="目标 SKU" html-for="catalog-copy-sku" required>
                  <a-select id="catalog-copy-sku" v-model:value="copy.sku" :options="targetSkus" show-search option-filter-prop="label"/>
                </a-form-item>
                <a-form-item label="新规则名称" html-for="catalog-copy-label" required>
                  <a-input id="catalog-copy-label" v-model:value="copy.label" :maxlength="80"/>
                </a-form-item>
              </div>
              <a-button type="link" :disabled="saving" @click="copy=undefined">取消复制</a-button>
            </div>
            <a-form-item v-if="hasEdits" class="catalog-rate-effective" label="生效时间（北京时间）" html-for="catalog-rate-effective-date"
              :extra="recipientsChanged
                ? '指定收款人需在订单付款时符合任职条件。已结算结果保留原规则。'
                : '订单按付款时间匹配规则。已结算结果保留原规则。'">
              <a-input id="catalog-rate-effective-date" v-model:value="effectiveFrom" type="datetime-local"
                :min="copy ? undefined : dateInput(sheet.effectiveFrom)" :max="copy ? undefined : dateInput(sheet.effectiveTo)" required/>
            </a-form-item>
          </template>
        </a-form>
        <RuleImpactPreview v-if="impactInput" v-model:busy="impactBusy" :periods="impactPeriods"
          :simulate="compareCatalogRule" :format-money="formatMoney"/>
      </template>
    </a-spin>
    </section>
    <template v-if="sheet" #footer>
      <div class="workspace-edit-footer" v-if="!sheet.mayPublish || applied">
        <a-button @click="emit('close')">关闭</a-button>
      </div>
      <div class="workspace-edit-footer" v-else-if="impactInput">
        <a-button :disabled="saving" @click="impactInput=undefined; error=''">返回编辑</a-button>
        <a-button type="primary" :loading="saving" :disabled="impactBusy" @click="publish">保存并生效</a-button>
      </div>
      <div class="workspace-edit-footer" v-else>
        <a-button :disabled="saving" @click="emit('close')">取消</a-button>
        <a-button :disabled="saving || structureEditing || !!targetDraft || !hasEdits" @click="previewImpact">预览差额</a-button>
        <a-button html-type="submit" form="catalog-rate-form" type="primary" :loading="saving"
          :disabled="structureEditing || !!targetDraft || !hasEdits">保存并生效</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.catalog-rate-toolbar {display:flex;flex-wrap:wrap;gap:6px 10px;align-items:center;margin-bottom:6px}
.catalog-rate-toolbar :deep(.ant-radio-group) {display:flex;flex-wrap:wrap}
.catalog-rate-toolbar :deep(.ant-input-affix-wrapper) {width:min(100%,190px)}
.catalog-rate-add {margin-inline-start:auto}
.catalog-rate-empty {margin:0;padding:5px 0;color:var(--muted);font-size:13px}
.catalog-rate-table :deep(.ant-table.ant-table-small .ant-table-cell) {padding-block:4px}
.catalog-rate-table :deep(.ant-table-pagination) {margin-block:6px}
.catalog-rate-summary {display:flex;flex-wrap:wrap;align-items:baseline;justify-content:space-between;gap:2px 12px;padding-bottom:6px;margin-bottom:6px;border-bottom:1px solid var(--line)}
.catalog-rate-summary h2 {font-size:16px;margin:0 0 2px}
.catalog-rate-summary p {margin:0;color:var(--muted);line-height:1.45}
.catalog-rate-context {display:flex;flex-wrap:wrap;align-items:center;gap:2px 8px;color:var(--muted);font-size:13px}
.catalog-rate-context :deep(.ant-tag) {margin-inline-end:0}
.catalog-rate-effective :deep(.ant-input) {width:min(100%,280px)}
.catalog-receipt {margin-bottom:6px}
.catalog-more {margin:6px 0}
.catalog-rate-label {max-width:380px}
.catalog-more :deep(.ant-collapse-header) {padding-block:6px}
.catalog-copy {padding:16px;margin:12px 0;border:1px solid var(--line);border-radius:8px}
</style>

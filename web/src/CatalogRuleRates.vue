<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { DownOutlined } from '@ant-design/icons-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import RuleImpactPreview from './RuleImpactPreview.vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogLeaderArrangement from './CatalogLeaderArrangement.vue'
import CatalogCommissionScopeFields from './CatalogCommissionScopeFields.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import RuleEditableTable from './RuleEditableTable.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import { catalogAppointmentLevels, catalogFlatMethods, catalogMembershipTypes, catalogOrderSalesPayees, catalogOrderScope, catalogPayeeComplete, catalogPercent as percent, catalogRateConditionBody, catalogRateConditionScope, catalogRateIndicators, catalogRateOrderScopeLabel, commissionRatePriority, catalogRuleImpactPeriods, catalogSalesPayees, emptyCatalogOrderScope, moneyAsMinor, newCatalogRateCondition, rateAsBasisPoints } from './rule-component-form'

const props = defineProps({ rule:Object, applied:Object, api:Function, formatTime:Function, formatMoney:Function,
  optionLabel:Function, periods:Array, skus:Array })
const emit = defineEmits(['close', 'published'])
const sheet = ref()
const ruleSection = ref()
function scrollToApplied(open) {
  if (open && props.applied) {
    ruleSection.value?.querySelector('.rule-block-header[aria-expanded="true"]')?.scrollIntoView({block:'start'})
  }
}
const selected = ref()
const filter = ref('')
const effectiveFrom = ref('')
const membershipEdits = ref(new Map())
const edits = ref({})
const labelEdits = ref(new Map())
const targetEdits = ref(new Map())
const teamEdits = ref(new Map())
const leaderEdits = ref(new Map())
const scopeEdits = ref(new Map())
const splitEdits = ref({})
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
const removedServiceReplacements = ref([])
const removedRateRows = ref(new Map())
const copy = ref()
const targetSkus = computed(()=>(props.skus || []).filter(sku=>sku.code!==props.rule?.scopeId)
  .map(sku=>({value:sku.code,label:sku.label})))
let sequence = 0
let nextDraftKey = 0

const rateColumns = [
  {title:'销售公司',key:'company',dataIndex:'company',width:190},
  {title:'销售类别',key:'category',dataIndex:'category',width:170},
  {title:'销售关系',key:'relationship',dataIndex:'relationship',width:110},
  {title:'比例（%）',key:'rate',width:126},
  {title:'收款方',key:'payee',width:128},
]
const component = computed(() => sheet.value?.components.find(item => item.itemKey === selected.value))
const calculationFor = itemKey => structure.value.calculations.find(change=>change.itemKey===itemKey)
const selectedCalculation = computed(() => calculationFor(selected.value))
const hasRateTable = computed(() => !!(selectedCalculation.value || component.value)?.rows?.length)
const columns = computed(() => sheet.value?.mayPublish && !props.applied && !selectedCalculation.value
  ? [...rateColumns,{title:'操作',key:'operation',width:133}] : rateColumns)
const targetKind = computed(() => targetEdits.value.get(selected.value)?.target || component.value?.targetKind)
const hasUniformRate = computed(() => {
  const item = component.value
  if (!item || hasRateTable.value || targetKind.value==='seller') return false
  return selectedCalculation.value ? selectedCalculation.value.fixedMinor == null : flatMethod(item)==='rate'
})
const targetKindLabels = {person:'指定个人',company:'指定公司',pool:'团队池',referrer:'订单直接推荐人',appointment:'销售组织的在任人员',...catalogOrderSalesPayees}
const targetShortLabels = {seller:'销售比例表',person:'个人',company:'公司',pool:'团队池',referrer:'推荐人',appointment:'在任人员',
  order_seller:'成交销售',order_sales_company:'销售公司'}
const teamSplit = computed(() => component.value?.teamSplit)
const serviceItemKey = 'internal-service'
const splitValue = field => Object.hasOwn(splitEdits.value,field) ? splitEdits.value[field] : (splitItem.value.teamSplit[field]/100).toFixed(2)
function setSplitRate(field,value) {
  if (rateAsBasisPoints(value)===splitItem.value.teamSplit[field]) delete splitEdits.value[field]
  else splitEdits.value[field] = value
}
const teamOptions = computed(() => (sheet.value?.teams || []).map(item=>({value:item.key,label:item.label})))
const team = computed(() => sheet.value?.teams?.find(item=>item.key===selectedTeam.value))
const orderTypeText = computed(() => (props.rule?.scopeLabel || sheet.value?.scopeLabel)?.replace(sheet.value?.productName+' · ','') || sheet.value?.productName)
function originalMembership(item) {
  if (item?.membershipNewOnly!=null) return item.membershipNewOnly
  if (item?.itemKey===serviceItemKey || item?.targetKind==='inherit' && !item.disabled) return originalMembership(splitItem.value)
  return Boolean(sheet.value?.membershipNewOnly)
}
const memberValue = item => (membershipEdits.value.get(item?.itemKey) ?? originalMembership(item)) ? 'new' : 'all'
const memberLabel = item => catalogMembershipTypes.find(option=>option.value===memberValue(item))?.label
function selectMemberType(item, value) {
  const newOnly = value==='new'
  if (newOnly===originalMembership(item)) membershipEdits.value.delete(item.itemKey)
  else membershipEdits.value.set(item.itemKey,newOnly)
}
const splitItem = computed(() => sheet.value?.components.find(item=>item.teamSplit))
const salesPayeeRemoved = computed(() => splitItem.value?.disabled || removedItems.value.has(splitItem.value?.itemKey))
const serviceVisible = computed(() => splitItem.value?.teamSplit.service>0 && !splitItem.value?.serviceDisabled)
const serviceReplacement = computed(() => structure.value.items.some(item=>item.replacesItemKey===serviceItemKey))
const serviceRemoved = computed(() => !serviceVisible.value || removedItems.value.has(serviceItemKey) || serviceReplacement.value)
const serviceArrangement = computed(() => ({itemKey:serviceItemKey,label:splitItem.value?.serviceLabel || '内部团队服务提成',
  membershipNewOnly:splitItem.value?.serviceMembershipNewOnly,recipientDuty:splitItem.value?.serviceRecipientDuty}))

const leaderArrangements = computed(() => sheet.value?.leaderArrangements || [])
const availableLeaders = computed(() => leaderArrangements.value.filter(leader=>leader.disabled && !leaderEdits.value.has(leader.itemKey)))
const visibleLeaders = computed(() => leaderArrangements.value.filter(leader=>!removedItems.value.has(leader.itemKey) &&
  (!leader.disabled || leaderEdits.value.has(leader.itemKey))))
const splitRemoved = computed(() => salesPayeeRemoved.value && serviceRemoved.value &&
  !visibleLeaders.value.some(leader=>leaderEdits.value.has(leader.itemKey) || leader.targetKind!=='inherit' || leader.rateBps>0))
const visibleComponents = computed(() => sheet.value?.components.filter(item=>!item.disabled && !removedItems.value.has(item.itemKey)) || [])
const editable = computed(() => sheet.value?.mayPublish && !props.applied && !saving.value)
const canEditArrangement = computed(() => !props.applied && sheet.value?.mayPublish && sheet.value?.structureOptions)
const editedComponent = computed(() => component.value && {...component.value,targetKind:targetKind.value})
const hasSales = computed(() => sheet.value?.components.some(item=>item.targetKind==='seller' || item.teamSplit))
const peopleFilters = computed(() => ({ruleId:props.rule?.id}))
const recipientsChanged = computed(() => targetEdits.value.size + teamEdits.value.size > 0)
const hasEdits = computed(() => sheet.value?.independentTeamSales || copy.value || membershipEdits.value.size || recipientsChanged.value || Object.keys(edits.value).length > 0 ||
  Object.keys(splitEdits.value).length > 0 || labelEdits.value.size || leaderEdits.value.size || scopeEdits.value.size ||
  structure.value.items.length || structure.value.sales || structure.value.rateRows.length || structure.value.calculations.length ||
  removedItems.value.size || removedRateRows.value.size)
const impactPeriods = computed(() => catalogRuleImpactPeriods(props.periods,sheet.value?.productId,impactInput.value?.effectiveFrom))
const dateInput = value => value ? new Date(Date.parse(value)+8*60*60*1000).toISOString().slice(0,16) : undefined
const canAddRates = computed(() => sheet.value?.mayPublish && !props.applied && !!sheet.value.structureOptions)
const rateOptions = computed(() => Object.fromEntries(['companies','categories','relationships'].map(kind=>[
  kind,(sheet.value?.structureOptions?.[kind] || []).filter(option=>kind!=='companies' || option.value!=='ANY'),
])))
const rateNames = computed(() => Object.fromEntries(['companies','categories','relationships'].map(kind=>
  [kind,new Map(rateOptions.value[kind].map(option=>[option.value,option.label]))])))
function stagedRateRow(condition, key, original) {
  const {scope,companyIds,categoryIds,relationship,payee,rate} = condition
  const names = rateNames.value
  const labelList = (ids, kind, missing) => ids?.map(id=>names[kind].get(id) || missing).join('、')
  return {
    ...original,...key,companyIds,categoryIds,payee,rateBps:rateAsBasisPoints(rate),staged:true,
    condition,scope,
    company:labelList(companyIds,'companies','公司名称待补'),
    category:labelList(categoryIds,'categories','销售类别名称待补'),
    relationshipCode:relationship,limitsRelationship:relationship!=='ANY',
    relationship:names.relationships.get(relationship) || original?.relationship || relationship,
  }
}
function configuredRateRows(item) {
  if (!item) return []
  const calculation = calculationFor(item.itemKey)
  if (calculation) {
    return (calculation.rows || []).map((row,index)=>stagedRateRow({
      ...row,scope:catalogRateConditionScope(row),rate:String(row.rateBps/100),
    },{draftKey:'method-'+index}))
  }
  const stored = item.rows || []
  const staged = structure.value.rateRows.filter(row=>row.itemKey===item.itemKey)
  if (!staged.length) return stored
  const replacements = new Map(staged.filter(edit=>edit.row).map(edit=>[edit.row,edit.condition]))
  return [
    ...staged.filter(edit=>!edit.row).map(({condition,draftKey})=>stagedRateRow(condition,{draftKey})),
    ...stored.map(row=>replacements.has(row.row) ? stagedRateRow(replacements.get(row.row),{},row) : row),
  ]
}
const rateRows = computed(() => configuredRateRows(component.value))
const singleSalesRate = computed(() => {
  const [row,...others] = rateRows.value
  if (targetKind.value==='seller' && !teamSplit.value && row?.scope==='general' && !row.limitsRelationship && !others.length) return row
})
const rateGroups = computed(() => {
  const mayAdd = canAddRates.value
  return [
    {key:'category',add:mayAdd && sheet.value.structureOptions.categories.length && '添加销售类别'},
    {key:'company',add:mayAdd && '添加指定公司'},
    {key:'inherited'},
    {key:'external'},
    {key:'general',add:mayAdd && !hasUniformRate.value && '添加通用比例'},
  ]
    .map(group=>{
      const rows = rateRows.value.filter(row=>row.scope===group.key)
      const activeCount = group.key==='general' && hasUniformRate.value
        ? Number((selectedCalculation.value?.rateBps ?? rateAsBasisPoints(flatValue(component.value)))!==undefined)
        : rows.filter(row=>!removedRateRows.value.has(cellKey(selected.value,row.row))).length
      return {...group,rows,activeCount}
    })
    .filter(group=>group.add || group.rows.length || group.key==='general' && hasUniformRate.value)
})
const rateGroup = ref()
const standardTabs = computed(() => rateGroups.value.map(group=>({value:group.key,count:group.activeCount})))
const rateTableRevision = ref(0)
const rateRowKey = row => row.draftKey || row.row
const rateFieldId = (row,field) => 'catalog-rate-'+rateRowKey(row)+'-'+field
const rateEditorTarget = computed(() => '#'+CSS.escape('catalog-rates-edit-'+selected.value))
const editorInCard = computed(() => component.value || selected.value===serviceItemKey && serviceReplacement.value)
watch([selected,rateGroups], ([item,groups],[previousItem]) => {
  const changedItem = item!==previousItem
  if (changedItem || !groups.some(group=>group.key===rateGroup.value)) {
    const populated = groups.filter(group=>group.rows.length)
    const positive = populated.filter(group=>group.rows.some(row=>row.rateBps>0))
    const candidates = positive.length ? positive : populated
    rateGroup.value = (candidates.find(group=>group.key==='category') ||
      candidates.find(group=>group.key==='general') || candidates[0] ||
      groups.find(group=>group.key==='general') || groups[0])?.key
  }
})
const ratePriority = computed(() => commissionRatePriority(rateGroups.value.filter(group=>group.activeCount).map(group=>group.key)))
const activeRateGroup = computed(() => rateGroups.value.find(group=>group.key===rateGroup.value))
const displayedRates = computed(() => {
  const term = filter.value.trim().toLowerCase()
  return (activeRateGroup.value?.rows || []).filter(row => !term || row.staged ||
    [row.company,row.relationship,row.category].some(value => value?.toLowerCase().includes(term)))
})
const showRateGroups = computed(() => hasRateTable.value || hasUniformRate.value && canAddRates.value)
const tableEditable = computed(() => canAddRates.value && !selectedCalculation.value)
const displayedColumns = computed(() => columns.value.filter(col=>
  (col.key!=='company' || ['company','inherited'].includes(rateGroup.value)) &&
  (col.key!=='category' || activeRateGroup.value?.rows.some(row=>row.category) ||
    tableEditable.value && ['company','category'].includes(rateGroup.value) && rateOptions.value.categories.length) &&
  (col.key!=='relationship' || activeRateGroup.value?.rows.some(row=>row.limitsRelationship) ||
    tableEditable.value && rateGroup.value!=='external' && rateOptions.value.relationships.length>1) &&
  (col.key!=='payee' || targetKind.value==='seller')))
async function addRecipient() {
  selected.value = undefined
  await nextTick()
  await structureEditor.value?.addRecipient()
}
async function makeServiceIndependent() {
  error.value = ''
  const label = (labelEdits.value.get(serviceItemKey) ?? serviceArrangement.value.label).trim()
  const rateBps = rateAsBasisPoints(splitValue('service'))
  if (!label || rateBps===undefined) {
    error.value = '请填写收款安排名称和有效比例。'
    return
  }
  const units = new Map(sheet.value.structureOptions.companies.map(option=>[option.value,option.unitId]))
  const recipients = new Map()
  for (const team of sheet.value.teams) {
    const unitId = units.get(team.key)
    const person = teamRecipient(team,'service')
    if (!unitId || !person?.id) {
      error.value = '请先核对「'+team.label+'」的销售公司和服务收款人。'
      return
    }
    const item = recipients.get(person.id) ?? {label,target:'person',personId:person.id,personName:person.name,rateBps,
      salesScopeUnitIds:[],membershipNewOnly:sheet.value.membershipSku ? memberValue(serviceArrangement.value)==='new' : undefined,
      replacesItemKey:serviceItemKey}
    if (!item.salesScopeUnitIds.includes(unitId)) item.salesScopeUnitIds.push(unitId)
    recipients.set(person.id,item)
  }
  const first = structure.value.items.length
  structure.value.items.push(...recipients.values())
  await nextTick()
  await structureEditor.value?.editRecipient(first)
}
function restoreService() {
  structureEditor.value?.cancel()
  structure.value.items = structure.value.items.filter(item=>item.replacesItemKey!==serviceItemKey)
}
function addStandard(item, scope) {
  if (selectedCalculation.value?.rows?.length) {
    structureEditor.value?.editCalculation(selectedCalculation.value,'table',scope)
    return
  }
  if (hasRateTable.value) {
    const draftKey = 'draft-'+(++nextDraftKey)
    structure.value.rateRows.unshift({itemKey:item.itemKey,draftKey,condition:newCatalogRateCondition(sheet.value.structureOptions,scope)})
    filter.value = ''
    rateTableRevision.value++
    nextTick(()=>document.getElementById('catalog-rate-first-'+draftKey)?.focus())
    return
  }
  const rateBps = selectedCalculation.value?.rateBps ?? rateAsBasisPoints(flatValue(item))
  const rows = rateBps===undefined ? [] : [{company:'ANY',relationship:'ANY',rateBps}]
  structureEditor.value?.editCalculation({rows},'table',scope)
}
const pendingScope = (itemKey, row) => row && structure.value.rateRows.find(edit=>edit.itemKey===itemKey && edit.row===row)
const salesPayeeLabel = row => row.teamRate ? '团队岗位拆分'
  : catalogSalesPayees.find(choice=>choice.value===(row.payee || 'seller'))?.label
const cellKey = (item, row) => JSON.stringify([item,row || 0])
function rowOptions(kind, values) {
  const missing = (values || []).filter(value=>value!=null && !rateNames.value[kind].has(value))
  return missing.length ? [...rateOptions.value[kind],...missing.map(value=>({value,label:value}))] : rateOptions.value[kind]
}
function rowEditable(row) {
  return tableEditable.value && !row.teamRate && !removedRateRows.value.has(cellKey(selected.value,row.row))
}
function setInlineCondition(row, field, value) {
  if (row.condition) {
    row.condition[field] = value
    return
  }
  const itemKey = selected.value
  const condition = {
    scope:row.scope,companyIds:[...(row.companyIds || [])],categoryIds:[...(row.categoryIds || [])],
    relationship:row.relationshipCode,payee:row.payee,rate:displayRate(row.rateBps,row.row),
    [field]:value,
  }
  structure.value.rateRows.push({itemKey,row:row.row,condition})
  delete edits.value[cellKey(itemKey,row.row)]
}
function removeArrangement(key) {
  if (key===serviceItemKey && serviceReplacement.value) {
    removedServiceReplacements.value = structure.value.items.filter(item=>item.replacesItemKey===key)
    structure.value.items = structure.value.items.filter(item=>item.replacesItemKey!==key)
  }
  removedItems.value.add(key)
  selected.value = undefined
  nextTick(()=>document.getElementById('catalog-undo-'+key)?.focus())
}
function undoArrangementRemoval(key) {
  removedItems.value.delete(key)
  if (key===serviceItemKey && removedServiceReplacements.value.length) {
    structure.value.items.push(...removedServiceReplacements.value)
    removedServiceReplacements.value = []
  }
  selected.value = key
}
function toggleRowRemoval(row) {
  const itemKey = selected.value
  const key = cellKey(itemKey,row)
  if (!removedRateRows.value.delete(key)) removedRateRows.value.set(key,{itemKey,row})
}
function discardStagedRate(row) {
  const index = structure.value.rateRows.findIndex(change=>change.itemKey===selected.value && rateRowKey(change)===rateRowKey(row))
  if (index<0) return
  structure.value.rateRows.splice(index,1)
}
function undoRateChange(row) {
  if (row.staged) discardStagedRate(row)
  else delete edits.value[cellKey(selected.value,row.row)]
}
const displayRate = (rate, row, item=selected.value) => {
  const key = cellKey(item,row)
  return Object.hasOwn(edits.value,key) ? edits.value[key].value : (rate/100).toFixed(2)
}
function currentRate(item,row) {
  if (row.staged) return row.rateBps
  const edit = edits.value[cellKey(item.itemKey,row.row)]
  return edit ? rateAsBasisPoints(edit.value) : row.rateBps
}
const activeRateRows = item => configuredRateRows(item).filter(row=>!removedRateRows.value.has(cellKey(item.itemKey,row.row)))
const rateIndicators = computed(() => Object.fromEntries((sheet.value?.components || []).map(item=>[
  item.itemKey,catalogRateIndicators(activeRateRows(item),row=>currentRate(item,row)),
])))
const savedMethod = item => item.fixedMinor == null ? 'rate' : 'fixed'
function flatMethod(item) {
  return edits.value[cellKey(item.itemKey)]?.method || savedMethod(item)
}
function calculationMethod(item) {
  if (targetKind.value==='seller') return singleSalesRate.value ? 'rate' : 'table'
  const staged = calculationFor(item.itemKey)
  if (staged) return staged.rows?.length ? 'table' : staged.fixedMinor != null ? 'fixed' : 'rate'
  return item.rows?.length ? 'table' : flatMethod(item)
}
function chooseCalculation(item, method) {
  if (method===calculationMethod(item)) return
  if (!item.rows?.length && !selectedCalculation.value && method!=='table') {
    setFlat(item,method,method===savedMethod(item) ? (item.fixedMinor ?? item.rateBps)/100 : undefined)
  } else {
    structureEditor.value?.editCalculation(undefined,method)
  }
}
function flatValue(item) {
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
  const standard = rateIndicators.value[item.itemKey]?.join('、')
  if (calculation) {
    if (calculation.rows?.length) return standard
    if (calculation.fixedMinor!=null) return (calculation.fixedMinor/100).toFixed(2)+' 元/笔'
    return percent(calculation.rateBps)
  }
  if (item.rows?.length) return standard || '未配置比例'
  if (item.fixedMinor == null && item.rateBps == null) return ''
  const value = flatValue(item)
  return value == null || value==='' ? '' : value+(flatMethod(item)==='fixed' ? ' 元/笔' : '%')
}
function readOnlyCalculation(item) {
  if (hasRateTable.value) return '比例 · 按计提标准'
  return [flatMethod(item)==='fixed' ? '固定金额' : '比例',componentStandard(item)].filter(Boolean).join(' · ')
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
  const target = edit?.target || item.targetKind
  const duty = edit ? edit.recipientDuty : item.recipientDuty
  const orderPayee = catalogOrderSalesPayees[target] || (target==='referrer' ? '订单直接推荐人' : '')
  const withDuty = (name,duty) => [name,duty && (props.optionLabel('organizationDuty',duty) || duty)].filter(Boolean).join(' · ')
  if (orderPayee) return withDuty(orderPayee,duty)
  if (target==='appointment') {
    return withDuty([appointmentLevelLabel(edit?.appointmentLevel || item.appointmentLevel),roleLabel(edit?.role || item.targetRole)]
      .filter(Boolean).join(' · '),duty)
  }
  if (!edit) {
    const name = withDuty(item.recipient?.name || item.targetLabel,item.recipientDuty)
    return item.targetKind==='pool' ? [name,item.poolOwner?.name || roleLabel(item.targetRole)].filter(Boolean).join(' · ') : name
  }
  if (edit.target==='person') return withDuty(edit.personName,edit.recipientDuty)
  const pool = edit.poolId && sheet.value.structureOptions.teamPools?.find(pool=>pool.id===edit.poolId)
  const organization = pool?.name || sheet.value.structureOptions.organizations.find(org=>org.value===edit.organization)?.label
  return edit.target==='pool' ? [organization,edit.ownerName || roleLabel(edit.role)].filter(Boolean).join(' · ') : organization
}
const roleLabel = role => props.optionLabel('catalogAppointmentRole',role) || role
const appointmentLevelLabel = level => catalogAppointmentLevels.find(option=>option.value===level)?.label
function originalTarget(item) {
  return {target:item.targetKind,role:item.targetRole,memberRole:item.memberRole,organization:item.targetOrganization,poolId:item.poolId,
    personId:item.recipient?.id,personName:item.recipient?.name,recipientDuty:item.recipientDuty,appointmentUnitId:item.appointmentUnitId,
    ownerPersonId:item.poolOwner?.id,ownerName:item.poolOwner?.name,appointmentLevel:item.appointmentLevel}
}
function setTarget(item, payee) {
  const original = originalTarget(item)
  if (['target','role','memberRole','organization','personId','recipientDuty','ownerPersonId','poolId','appointmentUnitId','appointmentLevel'].every(field=>payee[field]===original[field])) {
    targetEdits.value.delete(item.itemKey)
  } else {
    targetEdits.value.set(item.itemKey,payee)
  }
  if (payee.target!=='person' && commissionScope(item)==='own') setCommissionScope(item,null)
  error.value = ''
}
const teamRecipient = (current,field) => teamEdits.value.get(current.key)?.[field] ?? current[field]
const teamPerson = field => team.value && teamRecipient(team.value,field)
const serviceRecipients = computed(() => [...new Set((sheet.value?.teams || [])
  .map(team=>teamRecipient(team,'service')?.name || '收款人待配置'))])
const serviceScope = computed(() => formatScope(teamOptions.value.map(option=>option.label)))
function setTeamPerson(field, id, option) {
  const key = selectedTeam.value
  const changes = {...teamEdits.value.get(key)}
  if (id===team.value[field]?.id) delete changes[field]
  else changes[field] = {id,name:option.name}
  if (Object.keys(changes).length) teamEdits.value.set(key,changes)
  else teamEdits.value.delete(key)
}

const hasLeaderSalesScope = scope => Boolean(scope.companies.length || scope.groups.length || scope.categoryIds.length)
function originalLeader(item) {
  const scope = {orders:item.leaderScope.orders,companies:item.leaderScope.companies || [],groups:item.leaderScope.groups || [],
    categoryIds:item.leaderScope.categoryIds || []}
  return {
    label:item.label,target:item.targetKind,method:item.fixedMinor==null ? 'rate' : 'fixed',
    amount:((item.fixedMinor ?? item.rateBps)/100).toFixed(2),
    personId:item.recipient?.id,personName:item.recipient?.name,
    scope,restrictScope:hasLeaderSalesScope(scope),
  }
}
const leaderForm = item => leaderEdits.value.get(item.itemKey) || originalLeader(item)
const pendingRemovals = computed(() => [...removedItems.value].map(key=>{
  if (key===serviceItemKey) return {key,label:labelEdits.value.get(key)?.trim() || serviceArrangement.value.label}
  const leader = leaderArrangements.value.find(item=>item.itemKey===key)
  if (leader) return {key,label:leaderForm(leader).label.trim() || leader.label}
  return {key,label:labelEdits.value.get(key)?.trim() || sheet.value.components.find(item=>item.itemKey===key)?.label}
}))
function addLeader(item) {
  leaderEdits.value.set(item.itemKey,{...originalLeader(item),target:'appointment',amount:undefined})
  selected.value = item.itemKey
}
function removeLeader(item) {
  if (!item.disabled) {
    removeArrangement(item.itemKey)
    return
  }
  leaderEdits.value.delete(item.itemKey)
  selected.value = undefined
}
function setLeader(item, form) {
  const original = originalLeader(item)
  const normalize = value=>JSON.stringify({
    ...value,personName:undefined,label:value.label.trim(),
    amount:value.method==='fixed' ? moneyAsMinor(value.amount) : rateAsBasisPoints(value.amount),
    scope:{...value.scope,companies:[...value.scope.companies].sort(),groups:[...value.scope.groups].sort(),
      categoryIds:[...value.scope.categoryIds].sort()},
  })
  if (normalize(form)===normalize(original)) leaderEdits.value.delete(item.itemKey)
  else leaderEdits.value.set(item.itemKey,form)
}
function leaderSummary(item) {
  const form = leaderForm(item)
  const payee = form.target==='person' ? form.personName : item.itemKey==='internal-group-leader' ? '各小组的在任负责人' :
    ['inherit','registered'].includes(form.target) ? '规则登记的团队负责人' : '各团队的在任负责人'
  return [payee,form.amount==null || form.amount==='' ? '' : form.amount+(form.method==='fixed' ? ' 元/笔' : '%')].filter(Boolean).join(' · ')
}
const commissionUnitNames = computed(() => ({...sheet.value?.scopeUnitNames,
  ...Object.fromEntries((sheet.value?.structureOptions?.salesUnits || []).map(unit=>[unit.value,unit.label]))}))
const leaderScopeNames = computed(() => new Map((sheet.value?.teams || []).flatMap(team=>[
  [team.key,team.label],...(team.groups || []).map(group=>[group.value,team.label+' / '+group.label]),
])))
function formatScope(names, order) {
  return [names.length>2 ? names.slice(0,2).join('、')+' 等 '+names.length+' 个' : names.join('、'),order].filter(Boolean).join(' · ')
}
function leaderScopeText(item) {
  const {scope,restrictScope} = leaderForm(item)
  if (restrictScope && !hasLeaderSalesScope(scope)) return '待选择销售范围'
  const {companies,groups,orders,categoryIds} = scope
  const identifiers = groups.length ? groups : companies.length ? companies : (sheet.value?.teams || []).map(team=>team.key)
  const names = identifiers.map(id=>leaderScopeNames.value.get(id) || '已选组织')
  const groupScope = item.itemKey==='internal-group-leader' && !groups.length ? '各团队下属小组' : ''
  const categories = categoryIds.map(id=>sheet.value.scopeCategoryNames?.[id] || '已选类别名称待核').join('、')
  return formatScope(names,[groupScope,categories,{all:'范围内全部成交',members:'范围内其他团队成员成交',head:'范围内负责人亲自成交'}[orders]]
    .filter(Boolean).join(' · '))
}
const originalOrderScope = catalogOrderScope
const commissionScope = item => scopeEdits.value.has(item.itemKey) ? scopeEdits.value.get(item.itemKey) : originalOrderScope(item)
function setCommissionScope(item, scope) {
  const normalize = value => JSON.stringify(!value || value==='own' ? value : [[...value.unitIds].sort(),[...value.categoryIds].sort()])
  if (normalize(scope)===normalize(originalOrderScope(item))) {
    scopeEdits.value.delete(item.itemKey)
  } else {
    scopeEdits.value.set(item.itemKey,scope)
  }
}
function commissionScopeNames(item) {
  const scope = commissionScope(item)
  if (scope==='own') return ['本人销售']
  const rows = configuredRateRows(item)
  if (scope==null && rows?.length) {
    const active = activeRateRows(item)
    const positive = active.filter(row=>currentRate(item,row)>0)
    if (!positive.length) return ['当前无正比例']
    if (positive.some(row=>row.scope==='general')) return [catalogRateOrderScopeLabel(active,row=>currentRate(item,row))]
    return [...new Set(positive.map(row=>row.scope==='external' ? '未命中指定公司或类别的外部公司' :
      [row.scope==='category' ? '' : row.company,row.category].filter(Boolean).join(' / ')))].filter(Boolean)
  }
  if (scope==null) return ['本规则全部订单']
  if (emptyCatalogOrderScope(scope)) return ['待选择销售范围']
  return [...scope.unitIds.map(id=>commissionUnitNames.value[id] || '已选组织名称待核'),
    ...scope.categoryIds.map(id=>sheet.value.scopeCategoryNames?.[id] || '已选类别名称待核')]
}

watch(() => [props.rule, props.applied], async ([rule, applied]) => {
  const request = ++sequence
  sheet.value = undefined
  copy.value = undefined
  edits.value = {}
  labelEdits.value.clear()
  removedItems.value.clear()
  removedServiceReplacements.value = []
  removedRateRows.value.clear()
  targetEdits.value.clear()
  teamEdits.value.clear()
  leaderEdits.value.clear()
  scopeEdits.value.clear()
  membershipEdits.value.clear()
  splitEdits.value = {}
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
    const leaderKey = applied.component.endsWith(':DCAI_GROUP_LEAD') ? 'internal-group-leader' :
      applied.component.endsWith(':DCAI_TEAM_HEAD') ? 'internal-team-leader' : applied.component
    selected.value = applied.component.endsWith(':DCAI_SERVICE') && serviceVisible.value ? serviceItemKey :
      result.leaderArrangements?.find(item=>item.itemKey===leaderKey)?.itemKey ||
      result.components.find(item=>item.itemKey===applied.component || applied.component.startsWith(item.itemKey+':'))?.itemKey
      || result.components[0]?.itemKey
    selectedTeam.value = result.teams?.find(team=>team.key===applied.teamKey)?.key
    loading.value = false
    return
  }
  if (!rule) return
  loading.value = true
  try {
    const result = await props.api('/catalog-rule-rates?ruleId='+encodeURIComponent(rule.id)+'&edit=true')
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
  if (structureEditing.value) throw new Error('请先完成正在编辑的配置。')
  const removed = new Set(removedItems.value)
  if (serviceReplacement.value) removed.add(serviceItemKey)
  for (const [itemKey,payee] of targetEdits.value) {
    if (!removedItems.value.has(itemKey) && !catalogPayeeComplete(payee,sheet.value.structureOptions.roles)) {
      const item = sheet.value.components.find(component=>component.itemKey===itemKey)
      selected.value = itemKey
      throw new Error('请补齐「'+(labelEdits.value.get(itemKey)?.trim() || item.label)+'」的收款对象和适用岗位。')
    }
  }
  if (!effectiveFrom.value) throw new Error('请选择生效时间。')
  if (copy.value && (!copy.value.sku || !copy.value.label?.trim())) throw new Error('请选择目标 SKU，并填写规则名称。')
  let teamSplitRates
  if (!splitRemoved.value && !serviceRemoved.value && Object.hasOwn(splitEdits.value,'service')) {
    const service = rateAsBasisPoints(splitEdits.value.service)
    if (service===undefined) throw new Error('请填写 0 至 100 之间的服务比例。')
    teamSplitRates = {...splitItem.value.teamSplit,service}
  }
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
    if (removed.has(itemKey)) continue
    const label = value.trim()
    if (!label) throw new Error('请填写收款安排名称。')
    changes.push({itemKey,label})
  }
  const leaderChanges = splitRemoved.value ? [] : [...leaderEdits.value].filter(([itemKey])=>!removedItems.value.has(itemKey)).map(([itemKey,form])=>{
    const label = form.label.trim()
    const value = form.method==='fixed' ? moneyAsMinor(form.amount) : rateAsBasisPoints(form.amount)
    if (!label || value===undefined || form.target==='person' && !form.personId) {
      selected.value = itemKey
      throw new Error('请补齐「'+(label || '负责人提成')+'」的名称、收款人和计提标准。')
    }
    if (form.restrictScope && !hasLeaderSalesScope(form.scope)) {
      selected.value = itemKey
      throw new Error('请为「'+label+'」选择适用的销售组织或销售类别。')
    }
    return {itemKey,label,target:form.target,personId:form.personId,scope:form.scope,
      ...(form.method==='fixed' ? {fixedMinor:value} : {rateBps:value})}
  })
  const scopeChanges = [...scopeEdits.value].filter(([itemKey])=>!removedItems.value.has(itemKey)).map(([itemKey,scope])=>{
    if (scope==='own') return {itemKey,ownSales:true}
    if (emptyCatalogOrderScope(scope)) {
      selected.value = itemKey
      throw new Error('请为收款安排选择适用的销售组织或销售类别。')
    }
    return {itemKey,unitIds:scope?.unitIds || [],categoryIds:scope?.categoryIds || []}
  })
  return {
    baseId:props.rule.id,
    copy:copy.value,
    effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
    membershipArrangements:[...membershipEdits.value]
      .filter(([itemKey])=>!removed.has(itemKey) &&
        (!splitRemoved.value || itemKey!==serviceItemKey && !sheet.value.leaderArrangements?.some(item=>item.itemKey===itemKey)))
      .map(([itemKey,newOnly])=>({itemKey,newOnly})),
    changes,
    targetChanges:Array.from(targetEdits.value,([itemKey, {personName,ownerName,organization,...payee}])=>
      ({itemKey,...payee,...(payee.poolId ? {role:undefined,ownerPersonId:undefined} : {organization})}))
      .filter(({itemKey})=>!removedItems.value.has(itemKey)),
    teamChanges:splitRemoved.value ? [] : Array.from(teamEdits.value,([teamKey,changes])=>({
      teamKey,headPersonId:changes.head?.id,servicePersonId:serviceRemoved.value ? undefined : changes.service?.id,
    })).filter(change=>change.headPersonId || change.servicePersonId),
    independentTeamSales:sheet.value.independentTeamSales,
    teamSplitRates,
    leaderChanges,
    scopeChanges,
    newItems:structure.value.items.map(({personName,ownerName,replacesItemKey,...item})=>item),
    newSalesRates:structure.value.sales?.rows,
    newSalesLabel:structure.value.sales?.label,
    newSalesMembership:structure.value.sales?.membershipNewOnly,
    newSalesScope:structure.value.sales?.salesScopeUnitIds,
    newSalesCategoryScope:structure.value.sales?.salesScopeCategoryIds,
    calculationChanges:structure.value.calculations.filter(({itemKey})=>!removedItems.value.has(itemKey)),
    newRateRows:structure.value.rateRows.filter(({itemKey,row})=>!removedItems.value.has(itemKey) &&
      !removedRateRows.value.has(cellKey(itemKey,row)) && !calculationFor(itemKey))
      .map(({itemKey,row,condition})=>({
        itemKey,row,...catalogRateConditionBody(condition,condition.scope,
          (targetEdits.value.get(itemKey)?.target || sheet.value.components.find(item=>item.itemKey===itemKey)?.targetKind)==='seller'),
      })),
    removedItems:[...removed],
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
function compareCatalogRule(periodId, signal, matchedOrders) {
  return props.api('/catalog-rule-impact',{method:'POST',body:JSON.stringify({periodId,rule:impactInput.value,matchedOrders}),signal})
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
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @after-open-change="scrollToApplied" @close="emit('close')">
    <section ref="ruleSection" :aria-label="applied ? '计提使用的规则' : '产品分润配置'">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-spin :spinning="loading">
      <template v-if="sheet">
        <header class="catalog-rate-summary">
          <div>
            <h2>{{ rule?.scopeLabel || sheet.scopeLabel || sheet.productName }}</h2>
            <p v-if="sheet.label!==(rule?.scopeLabel || sheet.scopeLabel || sheet.productName)">{{ sheet.label }}</p>
          </div>
          <div class="catalog-rate-context">
            <a-tag>v{{ sheet.version }}</a-tag>
            <span>{{ formatTime(sheet.effectiveFrom) }} 生效<template v-if="sheet.effectiveTo"> · 截至 {{ formatTime(sheet.effectiveTo) }}</template></span>
            <a-tag v-if="sheet.independentTeamSales" color="blue">待发布 · 普通销售比例</a-tag>
            <span v-if="sheet.requiresCompletion">订单确认完成后计提</span>
          </div>
        </header>
        <a-form v-show="!impactInput" id="catalog-rate-form" layout="vertical" size="small" @submit.prevent="publish">
          <div class="rule-blocks" aria-label="收款安排">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">收款安排</h3>
            <a-space v-if="sheet.mayPublish && !applied && sheet.structureOptions" wrap :size="4">
              <a-button v-if="targetSkus.length && !copy" size="small" :disabled="saving || structureEditing" @click="copy={}">复制到其他 SKU</a-button>
              <a-dropdown v-if="availableLeaders.length" :trigger="['click']" :disabled="saving || structureEditing">
                <a-button size="small">添加收款安排 <DownOutlined aria-hidden="true"/></a-button>
                <template #overlay>
                  <a-menu>
                    <a-menu-item @click="addRecipient">通用收款安排</a-menu-item>
                    <a-menu-item v-for="leader in availableLeaders" :key="leader.itemKey" @click="addLeader(leader)">{{ leader.label }}</a-menu-item>
                  </a-menu>
                </template>
              </a-dropdown>
              <a-button v-else size="small" :disabled="saving || structureEditing" @click="addRecipient">添加收款安排</a-button>
            </a-space>
          </div>
          <section v-if="pendingRemovals.length" aria-label="待移除的收款安排" class="catalog-pending-removals">
            <strong>已移除，保存后生效</strong>
            <div v-for="{key,label} in pendingRemovals" :key="key">
              <span>{{ label }}</span>
              <a-button :id="'catalog-undo-'+key" type="link" size="small" :disabled="saving || structureEditing"
                :aria-label="'撤销移除 '+label" @click="undoArrangementRemoval(key)">撤销</a-button>
            </div>
          </section>
          <section v-for="item in visibleComponents" :key="item.itemKey" class="rule-block">
          <button type="button" class="rule-block-header" :disabled="saving || structureEditing"
            :aria-expanded="selected===item.itemKey" :aria-controls="'catalog-rate-'+item.itemKey"
            @click="selected=selected===item.itemKey ? undefined : item.itemKey">
            <DownOutlined aria-hidden="true"/>
            <strong>{{ labelEdits.get(item.itemKey)?.trim() || item.label }}</strong>
            <span class="rule-block-badge">{{ targetShortLabels[targetEdits.get(item.itemKey)?.target || item.targetKind] }}</span>
            <span class="rule-block-recipient" :title="commissionScopeNames(item).join('、')">
              提成范围 · {{ formatScope(commissionScopeNames(item)) }}
            </span>
            <span v-if="rateIndicators[item.itemKey]?.length" class="rule-rate-indicators">
              <span v-for="indicator in rateIndicators[item.itemKey]" :key="indicator" class="rule-rate-indicator">{{ indicator }}</span>
            </span>
            <small>{{ [targetName(item),sheet.membershipSku && memberLabel(item),!rateIndicators[item.itemKey]?.length && componentStandard(item)].filter(Boolean).join(' · ') }}</small>
          </button>
          <div v-if="selected===item.itemKey" :id="'catalog-rate-'+item.itemKey" class="rule-block-body">
          <RuleArrangementFields :name-id="canEditArrangement ? 'catalog-rate-label' : undefined" :name="labelEdits.get(item.itemKey) ?? item.label"
              :order-type-text="canEditArrangement ? orderTypeText : undefined"
              :order-type-id="canEditArrangement && sheet.membershipSku ? 'catalog-member-type' : undefined"
              :order-types="catalogMembershipTypes" :order-type="memberValue(item)" @update:order-type="selectMemberType(item,$event)"
              :disabled="!editable || structureEditing" @update:name="setLabel(item,$event)">
              <CatalogPayeeFields v-if="canEditArrangement" :model-value="targetEdits.get(item.itemKey) || originalTarget(item)"
                prefix="catalog-edit" :options="sheet.structureOptions" :api="api" :people-filters="peopleFilters"
                :unit-names="sheet.scopeUnitNames" :sales-available="item.canChooseSalesTable && (!selectedCalculation || !!selectedCalculation.rows?.length)"
                :saved-pool-label="item.targetLabel" :existing-pool="item.targetKind==='pool' && !item.poolId"
                :disabled="!editable || structureEditing" @update:model-value="setTarget(item,$event)"/>
            <a-descriptions v-if="!canEditArrangement" class="catalog-readonly-arrangement" size="small" :column="{xs:1,sm:2}">
              <a-descriptions-item label="收款对象">{{ targetKind==='seller' ? '按销售比例表确定收款方' : targetName(item) || targetKindLabels[targetKind] }}</a-descriptions-item>
              <a-descriptions-item label="计提方式">{{ readOnlyCalculation(item) }}</a-descriptions-item>
              <a-descriptions-item label="计提订单类型">{{ sheet.membershipSku ? memberLabel(item) : orderTypeText }}</a-descriptions-item>
              <a-descriptions-item label="计提订单范围">
                <span :title="commissionScopeNames(item).join('、')">{{ commissionScope(item)==null ? formatScope(commissionScopeNames(item)) : commissionScopeNames(item).join('、') }}</span>
              </a-descriptions-item>
              <a-descriptions-item v-if="item.targetRole" :label="targetKind==='pool' ? '负责人岗位' : '收款人任职岗位'">
                {{ roleLabel(item.targetRole) }}
              </a-descriptions-item>
              <a-descriptions-item v-if="item.appointmentUnitId" label="任职组织">
                {{ sheet.scopeUnitNames?.[item.appointmentUnitId] || '已选组织' }}
              </a-descriptions-item>
              <a-descriptions-item v-if="item.poolOwner" label="分配负责人">{{ item.poolOwner.name }}</a-descriptions-item>
              <a-descriptions-item v-if="item.memberRole" label="可参与分配的成员岗位">
                {{ roleLabel(item.memberRole) }}
              </a-descriptions-item>
            </a-descriptions>
              <template #calculation>
                <a-form-item v-if="canEditArrangement" label="计提方式" html-for="catalog-flat-method">
                  <a-input v-if="targetKind==='seller'" id="catalog-flat-method" value="比例" readonly/>
                  <a-select v-else id="catalog-flat-method" :value="calculationMethod(item)==='table' ? 'rate' : calculationMethod(item)"
                    :options="catalogFlatMethods" :disabled="!editable || structureEditing" @update:value="chooseCalculation(item,$event)"/>
                </a-form-item>
              </template>
            <template #standards>
              <RuleCommissionStandards v-if="hasRateTable || canEditArrangement"
                v-model="rateGroup" :groups="showRateGroups ? standardTabs : undefined" :priority="hasRateTable ? ratePriority : undefined" :disabled="structureEditing">
                <template #tools>
                  <a-button v-if="activeRateGroup?.add" size="small" :disabled="!editable || structureEditing"
                    @click="addStandard(item,activeRateGroup.key)">{{ activeRateGroup.add }}</a-button>
                  <a-input v-if="activeRateGroup?.rows.length>8" id="catalog-rate-filter" v-model:value="filter" size="small"
                    class="catalog-rate-filter" placeholder="搜索公司或销售类别" aria-label="搜索比例表" allow-clear/>
                </template>
                <template v-if="canEditArrangement && (!showRateGroups || rateGroup==='general')" #scope>
                  <CatalogCommissionScopeFields prefix="catalog-edit" :model-value="commissionScope(item)"
                    :options="sheet.structureOptions.salesUnits" :unit-names="commissionUnitNames" :categories="sheet.structureOptions.categories"
                    :category-names="sheet.scopeCategoryNames" :disabled="!editable || structureEditing"
                    :can-own="targetKind==='person'" @update:model-value="setCommissionScope(item,$event)"/>
                </template>
              <a-space v-if="selectedCalculation" wrap :size="4">
                <span v-if="!hasRateTable">{{ componentStandard(item) }}</span>
                <a-button size="small" type="link" :disabled="!editable || structureEditing"
                  @click="structureEditor?.editCalculation(selectedCalculation,calculationMethod(item))">编辑计算条件</a-button>
                <a-button size="small" type="link" :disabled="!editable || structureEditing" @click="undoCalculation">撤销更换</a-button>
              </a-space>
              <RuleStandardValue v-if="!hasRateTable && targetKind!=='seller' && !selectedCalculation && (!showRateGroups || rateGroup==='general')"
                :id="flatMethod(item)==='fixed' ? 'catalog-fixed-amount' : 'catalog-fixed-rate'" :method="flatMethod(item)"
                :model-value="flatValue(item)" :disabled="!editable" string-mode @update:model-value="setFlat(item,flatMethod(item),$event)"/>
              <p v-if="showRateGroups && !hasRateTable && rateGroup!=='general' && !structureEditing" class="finance-rate-empty">未配置</p>
              <template v-if="hasRateTable">
                <p v-if="!displayedRates.length" class="finance-rate-empty">{{ filter ? '未找到匹配的计提标准' : '未配置' }}</p>
                <RuleEditableTable v-else :key="selected + filter + rateGroup + rateTableRevision" class="catalog-rate-table"
                  label="计提标准明细" :columns="displayedColumns" :rows="displayedRates" :row-key="rateRowKey" :page-size="8" wide-first-column>
                  <template #cell="{ column, record }">
                    <template v-if="column.key==='company'">
                      <template v-if="rowEditable(record)">
                        <label class="sr-only" :for="rateFieldId(record,'company')">销售公司</label>
                        <a-select :id="rateFieldId(record,'company')" mode="multiple" size="small"
                        :value="record.companyIds || []" :options="rowOptions('companies',record.companyIds)"
                        :max-tag-count="1" :max-tag-text-length="12" :disabled="!editable || structureEditing" option-filter-prop="label"
                        placeholder="选择公司" @update:value="setInlineCondition(record,'companyIds',$event)"/>
                      </template>
                      <span v-else>{{ record.company }}</span>
                    </template>
                    <template v-if="column.key==='category'">
                      <template v-if="rowEditable(record) && ['company','category'].includes(record.scope)">
                        <label class="sr-only" :for="rateFieldId(record,'category')">销售类别</label>
                        <a-select :id="rateFieldId(record,'category')" mode="multiple" size="small" :value="record.categoryIds || []"
                          :options="rowOptions('categories',record.categoryIds)" :max-tag-count="1"
                          :disabled="!editable || structureEditing" option-filter-prop="label"
                          :placeholder="record.scope==='category' ? '选择销售类别' : '不限'" @update:value="setInlineCondition(record,'categoryIds',$event)"/>
                      </template>
                      <span v-else>{{ record.category || '不限' }}</span>
                    </template>
                    <template v-if="column.key==='relationship'">
                      <template v-if="rowEditable(record) && record.scope!=='external'">
                        <label class="sr-only" :for="rateFieldId(record,'relationship')">销售关系</label>
                        <a-select :id="rateFieldId(record,'relationship')" size="small"
                          :value="record.relationshipCode" :options="rowOptions('relationships',[record.relationshipCode])"
                          :disabled="!editable || structureEditing" @update:value="setInlineCondition(record,'relationship',$event)"/>
                      </template>
                      <span v-else>{{ record.relationship }}</span>
                    </template>
                    <template v-if="column.key==='rate'">
                      <a-input-number v-if="rowEditable(record)" size="small" :aria-label="record.row ? '第 '+record.row+' 行比例' : '新增比例'"
                        :id="record.draftKey ? 'catalog-rate-first-'+record.draftKey : undefined"
                        :value="record.condition ? record.condition.rate : displayRate(record.rateBps,record.row)"
                        :disabled="!editable || structureEditing"
                        :min="0" :max="100" :precision="2" string-mode
                        @update:value="record.condition ? setInlineCondition(record,'rate',$event) : setRate($event,record.rateBps,record.row)"/>
                      <span v-else>{{ applied ? displayRate(record.rateBps,record.row)+'%' : percent(record.rateBps) }}</span>
                      <small v-if="record.staged || Object.hasOwn(edits,cellKey(selected,record.row))"
                        class="order-version">未保存</small>
                      <small v-if="record.teamRate" class="order-version">内部团队拆分</small>
                    </template>
                    <template v-if="column.key==='payee'">
                      <template v-if="rowEditable(record) && record.scope!=='external'">
                        <label class="sr-only" :for="rateFieldId(record,'payee')">收款方</label>
                        <a-select :id="rateFieldId(record,'payee')" size="small"
                          :value="record.payee || 'seller'" :options="catalogSalesPayees" :disabled="!editable || structureEditing"
                          @update:value="setInlineCondition(record,'payee',$event)"/>
                      </template>
                      <span v-else>{{ salesPayeeLabel(record) }}</span>
                    </template>
                    <template v-if="column.key==='operation'">
                      <a-button v-if="removedRateRows.has(cellKey(selected,record.row))" type="link" size="small"
                        :disabled="saving || structureEditing" @click="toggleRowRemoval(record.row)">撤销删除</a-button>
                      <template v-else>
                        <a-button v-if="record.row && (record.staged || Object.hasOwn(edits,cellKey(selected,record.row)))"
                          type="link" size="small" :disabled="saving || structureEditing" @click="undoRateChange(record)">撤销修改</a-button>
                        <a-button type="link" danger size="small" :disabled="saving || structureEditing || record.teamRate"
                          @click="record.draftKey ? discardStagedRate(record) : toggleRowRemoval(record.row)">删除</a-button>
                      </template>
                    </template>
                  </template>
                </RuleEditableTable>
              </template>
              </RuleCommissionStandards>
            </template>
          </RuleArrangementFields>
          <div v-if="sheet.mayPublish && !applied" :id="'catalog-rates-edit-'+item.itemKey"/>
          <div v-if="sheet.mayPublish && !applied" class="form-actions">
            <a-button size="small" type="link" danger :disabled="structureEditing || saving"
              @click="removeArrangement(item.itemKey)">移除此安排</a-button>
          </div>
          </div>
          </section>
          <section v-if="serviceVisible && !removedItems.has(serviceItemKey)" class="rule-block">
            <button type="button" class="rule-block-header" :disabled="saving || structureEditing" :aria-expanded="selected===serviceItemKey"
              aria-controls="catalog-internal-service" @click="selected=selected===serviceItemKey ? undefined : serviceItemKey">
              <DownOutlined aria-hidden="true"/>
              <strong>{{ labelEdits.get(serviceItemKey)?.trim() || serviceArrangement.label }}</strong>
              <span v-if="serviceReplacement" class="rule-block-badge">待替换</span>
              <span v-if="!serviceReplacement" class="rule-block-recipient" :title="teamOptions.map(option=>option.label).join('、')">提成范围 · {{ serviceScope }}</span>
              <small v-if="!serviceReplacement" :title="serviceRecipients.join('、')">{{ [formatScope(serviceRecipients),sheet.membershipSku && memberLabel(serviceArrangement),splitValue('service')+'%'].filter(Boolean).join(' · ') }}</small>
            </button>
            <div v-if="selected===serviceItemKey" id="catalog-internal-service" class="rule-block-body">
              <RuleArrangementFields v-if="!serviceRemoved" name-id="catalog-service-label" :name="labelEdits.get(serviceItemKey) ?? serviceArrangement.label"
                :disabled="!editable || structureEditing" calculation-text="比例"
                :order-type-text="orderTypeText" :order-type-id="sheet.membershipSku ? 'catalog-service-member-type' : undefined"
                :order-types="catalogMembershipTypes" :order-type="memberValue(serviceArrangement)"
                :scope-text="serviceScope" @update:order-type="selectMemberType(serviceArrangement,$event)"
                @update:name="setLabel(serviceArrangement,$event)">
                <a-form-item label="查看销售公司" html-for="catalog-team-company">
                  <a-select id="catalog-team-company" v-model:value="selectedTeam" :options="teamOptions" show-search option-filter-prop="label"/>
                </a-form-item>
                <a-form-item v-if="team" label="收款对象" html-for="catalog-team-service">
                  <AppointmentPersonSelect v-if="editable" id="catalog-team-service" :key="selectedTeam"
                    :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters" :current="teamPerson('service')" :value="teamPerson('service')?.id"
                    @change="(id,option)=>setTeamPerson('service',id,option)"/>
                  <span v-else>{{ team.service?.name }}</span>
                </a-form-item>
                <template #standards>
                  <RuleCommissionStandards>
                    <RuleStandardValue v-if="!salesPayeeRemoved" id="catalog-split-service" :model-value="splitValue('service')"
                      :disabled="!editable || structureEditing" string-mode @update:model-value="setSplitRate('service',$event)"/>
                    <span v-else>{{ splitValue('service') }}%</span>
                  </RuleCommissionStandards>
                </template>
              </RuleArrangementFields>
              <template v-if="serviceReplacement">
                <small class="order-version">切换后按独立安排计提，可预览差额。</small>
                <div id="catalog-rates-edit-internal-service"></div>
              </template>
              <div v-if="sheet.mayPublish && !applied" class="form-actions">
                <a-button v-if="serviceReplacement" size="small" type="link" @click="restoreService">恢复原安排</a-button>
                <a-button v-else-if="!serviceRemoved && sheet.structureOptions" size="small" :disabled="structureEditing || saving"
                  @click="makeServiceIndependent">改为独立配置</a-button>
                <a-button size="small" type="link" danger :disabled="structureEditing || saving"
                  @click="removeArrangement(serviceItemKey)">移除此安排</a-button>
              </div>
            </div>
          </section>
          <section v-for="leader in visibleLeaders" :key="leader.itemKey" class="rule-block">
            <button type="button" class="rule-block-header" :disabled="saving || structureEditing"
              :aria-expanded="selected===leader.itemKey" :aria-controls="'catalog-rate-'+leader.itemKey"
              @click="selected=selected===leader.itemKey ? undefined : leader.itemKey">
              <DownOutlined aria-hidden="true"/>
              <strong>{{ leaderForm(leader).label.trim() || leader.label }}</strong>
              <span class="rule-block-recipient">提成范围 · {{ leaderScopeText(leader) }}</span>
              <small>{{ [sheet.membershipSku && memberLabel(leader),leaderSummary(leader)].filter(Boolean).join(' · ') }}</small>
            </button>
            <div v-if="selected===leader.itemKey" :id="'catalog-rate-'+leader.itemKey" class="rule-block-body">
              <CatalogLeaderArrangement :item-key="leader.itemKey" :model-value="leaderForm(leader)" :teams="sheet.teams || []"
                :api="api" :people-filters="peopleFilters" :disabled="!editable || structureEditing"
                :order-type-text="orderTypeText"
                :membership-editable="sheet.membershipSku" :order-type="memberValue(leader)"
                @update:order-type="selectMemberType(leader,$event)"
                :sales-categories="sheet.structureOptions?.categories" :category-names="sheet.scopeCategoryNames"
                :inherited="leader.targetKind==='inherit' && !leader.disabled"
                @update:model-value="setLeader(leader,$event)">
                <template v-if="leader.itemKey==='internal-team-leader' && ['inherit','registered'].includes(leaderForm(leader).target) && teamOptions.length" #registered>
                  <div class="rule-basics">
                    <a-form-item label="所属团队" html-for="catalog-head-company">
                      <a-select id="catalog-head-company" v-model:value="selectedTeam" :options="teamOptions" show-search option-filter-prop="label"/>
                    </a-form-item>
                    <a-form-item v-if="team" label="登记负责人" html-for="catalog-team-head">
                      <AppointmentPersonSelect v-if="!applied && sheet.mayPublish" id="catalog-team-head" :key="selectedTeam"
                        :api="api" endpoint="/catalog-rule-people" :filters="peopleFilters" :current="teamPerson('head')" :value="teamPerson('head')?.id"
                        :disabled="!editable" @change="(id,option)=>setTeamPerson('head',id,option)"/>
                      <span v-else>{{ team.head.name }}</span>
                    </a-form-item>
                  </div>
                </template>
              </CatalogLeaderArrangement>
              <div v-if="sheet.mayPublish && !applied" class="form-actions">
                <a-button size="small" type="link" danger :disabled="structureEditing || saving"
                  @click="removeLeader(leader)">移除此安排</a-button>
              </div>
            </div>
          </section>
          </div>
          <template v-if="sheet.mayPublish && !applied">
            <Teleport v-if="sheet.structureOptions" defer :disabled="!editorInCard"
              :to="editorInCard ? rateEditorTarget : 'body'">
              <CatalogRuleStructureEditor ref="structureEditor" :key="rule.id"
                :order-type-text="orderTypeText" :membership-sku="sheet.membershipSku" :default-membership-new-only="sheet.membershipNewOnly"
                :replacement-item-key="selected===serviceItemKey && serviceReplacement ? serviceItemKey : undefined"
                v-model="structure" v-model:editing="structureEditing" :options="sheet.structureOptions" :component="editedComponent"
                :api="api" :people-filters="peopleFilters" :parse-rate="rateAsBasisPoints" :sales-available="!hasSales && !structure.sales"/>
            </Teleport>
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
              extra="订单按付款时间匹配规则版本。">
              <a-input id="catalog-rate-effective-date" v-model:value="effectiveFrom" type="datetime-local"
                :min="copy ? undefined : dateInput(sheet.effectiveFrom)" :max="copy ? undefined : dateInput(sheet.effectiveTo)" required/>
            </a-form-item>
          </template>
        </a-form>
        <RuleImpactPreview v-if="impactInput" v-model:busy="impactBusy" :periods="impactPeriods" show-matched-orders
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
        <a-button :disabled="saving || structureEditing || !hasEdits" @click="previewImpact">预览差额</a-button>
        <a-button html-type="submit" form="catalog-rate-form" type="primary" :loading="saving"
          :disabled="structureEditing || !hasEdits">保存并生效</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.catalog-pending-removals { display:flex; flex-wrap:wrap; align-items:center; gap:4px 12px; padding:4px 8px; border:1px solid var(--line); border-radius:5px }
.catalog-pending-removals > div { display:flex; align-items:center; gap:2px }
.catalog-rate-filter {width:190px;max-width:100%}
.catalog-rate-table :deep(.ant-select) {width:100%}
.catalog-rate-summary {display:flex;flex-wrap:wrap;align-items:baseline;justify-content:space-between;gap:2px 12px;padding-bottom:6px;margin-bottom:6px;border-bottom:1px solid var(--line)}
.catalog-rate-summary h2 {font-size:16px;margin:0 0 2px}
.catalog-rate-summary p {margin:0;color:var(--muted);line-height:1.45}
.catalog-rate-context {display:flex;flex-wrap:wrap;align-items:center;gap:2px 8px;color:var(--muted);font-size:13px}
.catalog-rate-context :deep(.ant-tag) {margin-inline-end:0}
.catalog-rate-effective :deep(.ant-input) {width:min(100%,280px)}
.catalog-more {margin:6px 0}
.catalog-more :deep(.ant-collapse-header) {padding-block:6px}
.catalog-more :deep(.ant-collapse-content-box) {padding-block:4px}
.catalog-copy {padding:16px;margin:12px 0;border:1px solid var(--line);border-radius:8px}
</style>

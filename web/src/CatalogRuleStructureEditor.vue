<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogCommissionScopeFields from './CatalogCommissionScopeFields.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import { catalogAppointmentLevels, catalogFlatMethods, catalogMembershipTypes, catalogOrderSalesPayees, catalogOrderScope, catalogPayeeComplete, catalogPercent, catalogRateConditionBody, catalogRateConditionScope, catalogRateIndicators, catalogRateOrderScopeLabel, commissionRatePriority, catalogSalesPayees, emptyCatalogOrderScope, moneyAsMinor, newCatalogRateCondition } from './rule-component-form'

const props = defineProps({options:Object,component:Object,api:Function,peopleFilters:Object,parseRate:Function,salesAvailable:Boolean,
  orderTypeText:String,membershipSku:Boolean,defaultMembershipNewOnly:Boolean,replacementItemKey:String})
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const conditionGroup = ref('general')
const revising = computed(() => form.value?.kind==='calculation' || form.value?.itemIndex!=null ||
  form.value?.editingSales)
const isSales = computed(() => form.value?.kind==='item' && form.value.target==='seller')
const ratePriority = computed(() => commissionRatePriority((form.value?.rows || [])
  .filter(row=>props.parseRate(row.rate)!==undefined).map(row=>row.scope)))
const conditionRows = computed(() => form.value?.format==='table' ? form.value.rows : [])
const conditionTabs = computed(() => ['category','company','general','inherited','external'].flatMap(value=>{
  const count = conditionRows.value.filter(row=>row.scope===value).length
  if (['inherited','external'].includes(value) && !count) return []
  return [{value,count:value==='general' && form.value?.format==='rate' ? Number(props.parseRate(form.value.rate)!==undefined) : count}]
}))
const visibleConditions = computed(() => conditionRows.value.map((row,index)=>({row,index}))
  .filter(({row})=>row.scope===conditionGroup.value))
const conditionActions = {category:'添加销售类别',company:'添加指定公司',general:'添加通用比例'}
const stagedEntries = computed(() => [
  ...(draft.value.sales && !props.replacementItemKey ? [{item:{...draft.value.sales,target:'seller'},index:null}] : []),
  ...draft.value.items.map((item,index)=>({item,index})).filter(({item})=>item.replacesItemKey===props.replacementItemKey),
].map(({item,index})=>({index,...stagedSummary(item)})))
const itemName = ref()
const error = ref('')
const blankCondition = scope => newCatalogRateCondition(props.options,scope)
const labelFor = (choices,value) => choices.find(option=>option.value===value)?.label || value
function stagedSummary(item) {
  const rows = item.rows || []
  const recipient = catalogOrderSalesPayees[item.target] || (item.target==='referrer' ? '订单直接推荐人'
    : item.target==='seller' ? [...new Set(rows.map(row=>labelFor(catalogSalesPayees,row.payee || 'seller')))].join('、')
    : item.target==='person' ? item.personName
    : item.target==='pool' ? props.options.teamPools?.find(pool=>pool.id===item.poolId)?.name
    : item.target==='appointment' ? [labelFor(catalogAppointmentLevels,item.appointmentLevel),labelFor(props.options.roles,item.role)].join(' · ')
    : labelFor(props.options.organizations,item.organization))
  const standard = rows.length ? '' : item.fixedMinor != null ? (item.fixedMinor/100).toFixed(2)+' 元/笔' : catalogPercent(item.rateBps)
  const scopeNames = [
    ...(item.salesScopeUnitIds || []).map(id=>labelFor(props.options.salesUnits,id)),
    ...(item.salesScopeCategoryIds || []).map(id=>labelFor(props.options.categories,id)),
  ]
  const positive = rows.filter(row=>row.rateBps>0)
  if (!item.ownSales && !scopeNames.length && !positive.some(row=>catalogRateConditionScope(row)==='general')) {
    for (const row of positive) {
      const names = row.externalCompanyDefault ? '其他外部公司' : [
        ...(row.companyIds || []).map(id=>labelFor(props.options.companies,id)),
        ...(row.categoryIds || []).map(id=>labelFor(props.options.categories,id)),
      ].join('、')
      if (names && !scopeNames.includes(names)) scopeNames.push(names)
    }
  }
  const scope = item.ownSales ? '本人销售' : scopeNames.length>2 ? scopeNames.slice(0,2).join('、')+' 等 '+scopeNames.length+' 个'
    : scopeNames.join('、') || (rows.length ? positive.length ? catalogRateOrderScopeLabel(rows) : '当前无正比例' : '本规则全部订单')
  const orderType = props.membershipSku && catalogMembershipTypes.find(option=>option.value===(item.membershipNewOnly ? 'new' : 'all'))?.label
  const duty = item.recipientDuty && labelFor(props.options.duties,item.recipientDuty)
  return {text:[item.label,recipient,duty,standard,'提成范围 '+scope,orderType].filter(Boolean).join(' · '),
    indicators:catalogRateIndicators(rows),scopeTitle:scopeNames.join('、')}
}
async function addRecipient() {
  error.value = ''
  form.value = {kind:'item',target:'person',format:'rate',rows:[]}
  await nextTick()
  itemName.value?.focus()
}
async function editSales() {
  error.value = ''
  const {label,rows,format,membershipNewOnly} = draft.value.sales
  form.value = {kind:'item',target:'seller',editingSales:true,label,membershipNewOnly,...calculationForm({rows},format),
    format,orderScope:catalogOrderScope(draft.value.sales),rate:format==='rate' ? String(rows[0].rateBps/100) : undefined,salesPayee:rows[0].payee}
  await nextTick()
  itemName.value?.focus()
}
function changePayee(payee) {
  const previous = form.value.target
  form.value = payee
  if (payee.target!=='person' && form.value.orderScope==='own') form.value.orderScope = null
  if (payee.target==='seller' && previous!=='seller') {
    form.value.format = 'table'
    if (!form.value.rows.length) form.value.rows.push(blankCondition())
    conditionGroup.value = form.value.rows[0].scope
  }
}
async function editRecipient(index) {
  const {fixedMinor,rateBps,rows,...payee} = draft.value.items[index]
  error.value = ''
  form.value = {
    ...payee,orderScope:catalogOrderScope(payee),kind:'item',itemIndex:index,
    ...calculationForm({fixedMinor,rateBps,rows}),
  }
  await nextTick()
  itemName.value?.focus()
}
function calculationForm({fixedMinor,rateBps,rows}, defaultFormat='rate') {
  return {
    format:rows?.length ? 'table' : fixedMinor!=null ? 'fixed' : rateBps!=null ? 'rate' : defaultFormat,
    rate:rateBps==null ? undefined : String(rateBps/100),
    amount:fixedMinor==null ? undefined : String(fixedMinor/100),
    rows:rows?.map(row=>({
      scope:catalogRateConditionScope(row),
      companyIds:[...(row.companyIds || [])],categoryIds:[...(row.categoryIds || [])],
      relationship:row.relationship,payee:row.payee,rate:String(row.rateBps/100),
    })) || (defaultFormat==='table' ? [blankCondition()] : []),
  }
}
function editCalculation(pending, format, appendScope) {
  error.value = ''
  form.value = {kind:'calculation',itemKey:props.component.itemKey,...calculationForm(pending || {},format),format,initialGroup:appendScope}
  if (appendScope) form.value.rows.push(blankCondition(appendScope))
}
function cancel() {
  form.value = undefined
  error.value = ''
}
function addCondition() {
  const current = form.value
  if (current.format==='rate') {
    current.rows = props.parseRate(current.rate)===undefined ? [] :
      [{...blankCondition('general'),rate:current.rate,payee:current.salesPayee}]
    current.format = 'table'
  }
  current.rows.push(blankCondition(conditionGroup.value))
}
function removeCondition(index) {
  form.value.rows.splice(index,1)
  if (!conditionActions[conditionGroup.value] && !visibleConditions.value.length) conditionGroup.value = 'general'
}
function calculationBody(staged) {
  if (staged.format==='rate') {
    const rateBps = props.parseRate(staged.rate)
    if (rateBps===undefined) throw new Error('请填写有效比例。')
    return {rateBps}
  }
  if (staged.format==='fixed') {
    const fixedMinor = moneyAsMinor(staged.amount)
    if (fixedMinor===undefined) throw new Error('请填写有效金额，最多保留两位小数。')
    return {fixedMinor}
  }
  return {rows:staged.rows.map(row=>catalogRateConditionBody(row,row.scope,staged.target==='seller'))}
}
function stage() {
  error.value = ''
  const staged = form.value
  try {
    if (staged.kind==='calculation') {
      const change = {itemKey:staged.itemKey,...calculationBody(staged)}
      const existing = draft.value.calculations.findIndex(row=>row.itemKey===change.itemKey)
      if (existing>=0) draft.value.calculations.splice(existing,1,change)
      else draft.value.calculations.push(change)
    } else {
      const label = staged.label?.trim()
      if (!label || !catalogPayeeComplete(staged,props.options.roles)) {
        throw new Error('请填写收款安排名称，并补齐收款对象。')
      }
      const ownSales = staged.orderScope==='own'
      if (emptyCatalogOrderScope(staged.orderScope)) throw new Error('请选择适用的销售组织或销售类别。')
      const scope = {salesScopeUnitIds:staged.orderScope?.unitIds,salesScopeCategoryIds:staged.orderScope?.categoryIds}
      const membershipNewOnly = props.membershipSku ? staged.membershipNewOnly ?? props.defaultMembershipNewOnly : undefined
      if (isSales.value) {
        const calculation = calculationBody(staged)
        const rows = calculation.rows || [{company:'ANY',relationship:'ANY',rateBps:calculation.rateBps,payee:staged.salesPayee}]
        draft.value.sales = {label,format:staged.format,rows,...scope,membershipNewOnly}
        cancel()
        return
      }
      const item = {label,target:staged.target,role:staged.poolId ? undefined : staged.role,personId:staged.personId,ownSales,membershipNewOnly,
        replacesItemKey:staged.replacesItemKey,appointmentUnitId:staged.appointmentUnitId,appointmentLevel:staged.appointmentLevel,
        personName:staged.personName,organization:staged.poolId ? undefined : staged.organization,memberRole:staged.memberRole,poolId:staged.poolId,
        ownerPersonId:staged.poolId ? undefined : staged.ownerPersonId,ownerName:staged.poolId ? undefined : staged.ownerName,...scope,...calculationBody(staged)}
      if (staged.itemIndex==null) draft.value.items.push(item)
      else draft.value.items.splice(staged.itemIndex,1,item)
    }
    cancel()
  } catch (err) {
    error.value = err.message
  }
}
watch(form, value => {
  editing.value = !!value
  conditionGroup.value = value?.initialGroup || (value?.format==='table' ? value.rows[0]?.scope : undefined) || 'general'
})
defineExpose({addRecipient,editRecipient,editCalculation,cancel})
</script>

<template>
  <section class="catalog-rule-structure" aria-label="分润分项和费率条件">
    <div v-if="form" class="catalog-add-form">
      <a-alert v-if="error" :message="error" type="error" show-icon class="feedback"/>
      <RuleArrangementFields ref="itemName" v-model:name="form.label" :name-id="form.kind==='item' ? 'catalog-add-label' : undefined"
        :calculation-text="form.kind==='item' && isSales ? '比例' : undefined"
        :order-type-text="form.kind==='item' ? orderTypeText : undefined"
        :order-type-id="form.kind==='item' && membershipSku ? 'catalog-add-member-type' : undefined"
        :order-types="catalogMembershipTypes" :order-type="(form.membershipNewOnly ?? defaultMembershipNewOnly) ? 'new' : 'all'"
        @update:order-type="form.membershipNewOnly=$event==='new'">
        <CatalogPayeeFields v-if="form.kind==='item'" :model-value="form" prefix="catalog-add" :options="options" :api="api"
          :people-filters="peopleFilters"
          :sales-available="!form.editingSales && form.itemIndex==null && salesAvailable" @update:model-value="changePayee"/>
        <template #calculation>
          <a-form-item v-if="form.kind==='item' && !isSales" label="计提方式" html-for="catalog-add-format">
            <a-select id="catalog-add-format" :value="form.format==='table' ? 'rate' : form.format" :options="catalogFlatMethods"
              @update:value="form.format=$event"/>
          </a-form-item>
          <a-form-item v-if="isSales && form.format==='rate'" label="比例收款方" html-for="catalog-add-sales-payee">
            <a-select id="catalog-add-sales-payee" :value="form.salesPayee || 'seller'" :options="catalogSalesPayees" @change="form.salesPayee=$event"/>
          </a-form-item>
        </template>
        <template #standards>
          <RuleCommissionStandards v-model="conditionGroup" :groups="form.format==='fixed' ? undefined : conditionTabs"
            :priority="form.format==='table' ? ratePriority : undefined" :embedded="form.kind==='calculation'">
            <template #tools>
              <a-button v-if="conditionActions[conditionGroup] && (form.format==='table' || conditionGroup!=='general')"
                size="small" :disabled="conditionGroup==='category' && !options.categories.length" @click="addCondition">
                {{ conditionActions[conditionGroup] }}
              </a-button>
            </template>
            <template v-if="form.kind==='item' && (form.format==='fixed' || conditionGroup==='general')" #scope>
              <CatalogCommissionScopeFields prefix="catalog-add" v-model="form.orderScope" :options="options.salesUnits"
                :categories="options.categories" :can-own="form.target==='person'"/>
            </template>
            <RuleStandardValue v-if="form.format==='rate' && conditionGroup==='general'" id="catalog-add-fixed" v-model="form.rate" string-mode/>
            <RuleStandardValue v-else-if="form.format==='fixed'" id="catalog-add-amount" v-model="form.amount" method="fixed" string-mode/>
            <template v-else-if="visibleConditions.length">
              <div v-for="({index}) in visibleConditions" :key="index" class="catalog-add-condition">
                <CatalogRateCondition v-model="form.rows[index]" :scope="conditionGroup" :options="options" :suffix="'-'+index" :sales-payee="isSales">
                  <a-button v-if="form.rows.length>1" type="link" danger size="small" @click="removeCondition(index)">移除</a-button>
                </CatalogRateCondition>
              </div>
            </template>
            <p v-else class="finance-rate-empty">未配置</p>
          </RuleCommissionStandards>
        </template>
      </RuleArrangementFields>
      <div class="form-actions">
        <a-button @click="cancel">{{ revising ? '取消编辑' : '取消添加' }}</a-button>
        <a-button type="primary" @click="stage">
          {{ revising ? '应用修改' : '加入规则' }}
        </a-button>
      </div>
    </div>
    <ul v-if="stagedEntries.length" class="catalog-staged-entries" aria-label="待发布新增配置">
      <li v-for="{index,text,indicators,scopeTitle} in stagedEntries" :key="index ?? 'sales'">
        <div>
          <span :title="scopeTitle">{{ text }}</span>
          <small class="order-version">未保存</small>
          <div v-if="indicators.length" class="rule-rate-indicators">
            <span v-for="indicator in indicators" :key="indicator" class="rule-rate-indicator">{{ indicator }}</span>
          </div>
        </div>
        <a-space :size="0">
          <a-button type="link" size="small" :disabled="!!form" @click="index==null ? editSales() : editRecipient(index)">编辑</a-button>
          <a-button type="link" size="small" danger :disabled="!!form" @click="index==null ? draft.sales=undefined : draft.items.splice(index,1)">移除</a-button>
        </a-space>
      </li>
    </ul>
  </section>
</template>

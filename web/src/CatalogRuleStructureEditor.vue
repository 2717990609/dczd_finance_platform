<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogCommissionScopeFields from './CatalogCommissionScopeFields.vue'
import { catalogAppointmentLevels, catalogFlatMethods, catalogMembershipTypes, catalogOrderSalesPayees, catalogOrderScope, catalogPayeeComplete, catalogPercent, catalogRateConditionBody, catalogRateConditionScope, catalogRateIndicators, catalogRatePriority, catalogSalesPayees, emptyCatalogOrderScope, moneyAsMinor, newCatalogRateCondition } from './rule-component-form'

const props = defineProps({options:Object,component:Object,api:Function,peopleFilters:Object,parseRate:Function,salesAvailable:Boolean,
  orderTypeText:String,productId:Number,membershipSku:Boolean,defaultMembershipNewOnly:Boolean,replacementItemKey:String})
const emit = defineEmits(['rate-staged'])
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const revising = computed(() => form.value?.kind==='calculation' || form.value?.itemIndex!=null ||
  form.value?.rowNumber || form.value?.draftKey || form.value?.editingSales)
const isSales = computed(() => form.value?.kind==='item' && form.value.target==='seller')
const ratePriority = computed(() => catalogRatePriority((form.value?.rows || [])
  .filter(row=>props.parseRate(row.rate)!==undefined).map(row=>row.scope)))
const stagedEntries = computed(() => [
  ...(draft.value.sales && !props.replacementItemKey ? [{item:{...draft.value.sales,target:'seller'},index:null}] : []),
  ...draft.value.items.map((item,index)=>({item,index})).filter(({item})=>item.replacesItemKey===props.replacementItemKey),
].map(({item,index})=>({index,...stagedSummary(item)})))
const itemName = ref()
const error = ref('')
let nextDraftKey = 0
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
    : scopeNames.join('、') || (rows.length ? positive.length ? '按比例条件匹配' : '当前无正比例' : '本规则全部订单')
  const orderType = props.membershipSku && catalogMembershipTypes.find(option=>option.value===(item.membershipNewOnly ? 'new' : 'all'))?.label
  const duty = item.recipientDuty && labelFor(props.options.duties,item.recipientDuty)
  return {text:[item.label,recipient,duty,standard,'提成范围 '+scope,orderType].filter(Boolean).join(' · '),
    indicators:catalogRateIndicators(rows),scopeTitle:scopeNames.join('、')}
}
function begin(kind,scope) {
  error.value = ''
  form.value = kind==='item'
    ? {kind,target:'person',format:'rate',rows:[blankCondition()]}
    : {kind,scope,itemKey:props.component.itemKey,
      row:blankCondition(scope)}
}
async function addRecipient() {
  begin('item')
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
  } else if (previous==='seller' && payee.target!=='seller') {
    form.value.rows = form.value.rows.filter(row=>row.scope!=='external')
    form.value.rows.forEach(row=>{ delete row.payee })
    if (!form.value.rows.length) form.value.rows.push(blankCondition())
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
    })) || [blankCondition()],
  }
}
function editCalculation(pending, format) {
  error.value = ''
  form.value = {kind:'calculation',itemKey:props.component.itemKey,...calculationForm(pending || {},format),format}
}
function editRateScope(entry, rate) {
  error.value = ''
  form.value = {kind:'row',itemKey:props.component.itemKey,rowNumber:entry.row,draftKey:entry.draftKey,
    row:{scope:entry.scope,companyIds:[...(entry.companyIds || [])],categoryIds:[...(entry.categoryIds || [])],
      relationship:entry.relationshipCode,payee:entry.payee,rate}}
}
function cancel() {
  form.value = undefined
  error.value = ''
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
  return {rows:staged.rows.map(row=>catalogRateConditionBody(row))}
}
function stage() {
  error.value = ''
  const staged = form.value
  try {
    if (staged.kind==='row') {
      const scope = staged.scope ?? staged.row.scope
      const change = {itemKey:staged.itemKey,row:staged.rowNumber,...catalogRateConditionBody(staged.row,scope)}
      if (!change.row) change.draftKey = staged.draftKey || 'draft-'+(++nextDraftKey)
      const existing = draft.value.rateRows.findIndex(row=>row.itemKey===change.itemKey &&
        (change.row ? row.row===change.row : row.draftKey===change.draftKey))
      if (existing>=0) draft.value.rateRows.splice(existing,1,change)
      else draft.value.rateRows.push(change)
      emit('rate-staged',scope)
    } else if (staged.kind==='calculation') {
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
      const item = {label,target:staged.target,role:staged.role,personId:staged.personId,recipientDuty:staged.recipientDuty,ownSales,membershipNewOnly,
        replacesItemKey:staged.replacesItemKey,appointmentUnitId:staged.appointmentUnitId,appointmentLevel:staged.appointmentLevel,
        personName:staged.personName,organization:staged.poolId ? undefined : staged.organization,memberRole:staged.memberRole,poolId:staged.poolId,
        ownerPersonId:staged.ownerPersonId,ownerName:staged.ownerName,...scope,...calculationBody(staged)}
      if (staged.itemIndex==null) draft.value.items.push(item)
      else draft.value.items.splice(staged.itemIndex,1,item)
    }
    cancel()
  } catch (err) {
    error.value = err.message
  }
}
watch(form, value => { editing.value = !!value })
defineExpose({addRecipient,editRecipient,editCalculation,editRateScope,cancel,addRateCondition:scope=>begin('row',scope)})
</script>

<template>
  <section class="catalog-rule-structure" aria-label="分润分项和费率条件">
    <div v-if="form" class="catalog-add-form" :class="{'catalog-add-rate-row':form.kind==='row'}">
      <a-alert v-if="error" :message="error" type="error" show-icon class="feedback"/>
      <template v-if="form.kind==='row'">
        <CatalogRateCondition v-model="form.row" :options="options" :scope="form.scope" :sales-payee="component?.targetKind==='seller'"/>
      </template>
      <template v-else>
        <RuleArrangementFields ref="itemName" v-model:name="form.label" :name-id="form.kind==='item' ? 'catalog-add-label' : undefined"
          :calculation-text="form.kind==='item' && isSales ? '比例' : undefined"
          :order-type-text="form.kind==='item' ? orderTypeText : undefined"
          :order-type-id="form.kind==='item' && membershipSku ? 'catalog-add-member-type' : undefined"
          :order-types="catalogMembershipTypes" :order-type="(form.membershipNewOnly ?? defaultMembershipNewOnly) ? 'new' : 'all'"
          @update:order-type="form.membershipNewOnly=$event==='new'">
          <CatalogPayeeFields v-if="form.kind==='item'" :model-value="form" prefix="catalog-add" :options="options" :api="api"
            :product-id="productId" :people-filters="peopleFilters"
            :sales-available="!form.editingSales && form.itemIndex==null && salesAvailable" @update:model-value="changePayee"/>
          <template #calculation>
          <a-form-item v-if="form.kind==='item' && !isSales" label="计提方式" html-for="catalog-add-format">
            <a-select id="catalog-add-format" :value="form.format==='table' ? 'rate' : form.format" :options="catalogFlatMethods"
              @update:value="form.format=$event"/>
          </a-form-item>
          <a-form-item v-if="isSales && form.format==='rate'" label="比例收款方" html-for="catalog-add-sales-payee">
            <a-select id="catalog-add-sales-payee" :value="form.salesPayee || 'seller'" :options="catalogSalesPayees" @change="form.salesPayee=$event"/>
          </a-form-item>
          <a-form-item v-if="form.format==='rate'" label="比例（%）" html-for="catalog-add-fixed" required>
            <a-input-number id="catalog-add-fixed" aria-label="比例（%）" v-model:value="form.rate"
              :min="0" :max="100" :precision="2" string-mode/>
          </a-form-item>
          <a-form-item v-else-if="form.format==='fixed'" label="金额（元）" html-for="catalog-add-amount" required>
            <a-input-number id="catalog-add-amount" v-model:value="form.amount"
              :min="0" :precision="2" string-mode/>
          </a-form-item>
          </template>
          <template #scope>
            <CatalogCommissionScopeFields v-if="form.kind==='item'" prefix="catalog-add" v-model="form.orderScope"
              :options="options.salesUnits" :categories="options.categories" :can-own="form.target==='person'"/>
          </template>
        </RuleArrangementFields>
        <a-button v-if="form.kind==='item' && form.format!=='fixed'" size="small"
          @click="form.format=form.format==='table' ? 'rate' : 'table'">
          {{ form.format==='table' ? '改用统一比例' : '按类别或公司设比例' }}
        </a-button>
        <template v-if="form.format==='table'">
          <small v-if="ratePriority" class="finance-rate-priority">匹配顺序 {{ ratePriority }}</small>
          <div v-for="(_,index) in form.rows" :key="index" class="catalog-add-condition">
            <CatalogRateCondition v-model="form.rows[index]" :options="options" :suffix="'-'+index" :sales-payee="isSales">
              <a-button v-if="form.rows.length>1" type="link" danger size="small" @click="form.rows.splice(index,1)">移除</a-button>
            </CatalogRateCondition>
          </div>
          <a-button type="dashed" @click="form.rows.push(blankCondition())">再添一个条件</a-button>
        </template>
      </template>
      <div class="form-actions">
        <a-button :size="form.kind==='row' ? 'small' : undefined" @click="cancel">{{ revising ? '取消编辑' : '取消添加' }}</a-button>
        <a-button :size="form.kind==='row' ? 'small' : undefined" type="primary" @click="stage">
          {{ revising ? '应用修改' : form.kind==='row' ? '加入比例表' : '加入规则' }}
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

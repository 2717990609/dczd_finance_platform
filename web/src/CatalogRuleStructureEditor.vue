<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import CatalogCommissionScopeFields from './CatalogCommissionScopeFields.vue'
import { catalogAppointmentLevels, catalogFlatMethods, catalogMembershipTypes, catalogOrderScope, catalogPayeeComplete, catalogRateConditionBody, catalogRatePriorityLabel, catalogSalesPayees, emptyCatalogOrderScope, moneyAsMinor, newCatalogRateCondition } from './rule-component-form'

const props = defineProps({options:Object,component:Object,api:Function,peopleFilters:Object,parseRate:Function,salesAvailable:Boolean,
  orderTypeText:String,productId:Number,membershipSku:Boolean,defaultMembershipNewOnly:Boolean,replacementItemKey:String})
const emit = defineEmits(['rate-staged'])
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const revising = computed(() => form.value?.kind==='calculation' || form.value?.itemIndex!=null ||
  form.value?.rowNumber || form.value?.draftKey || form.value?.editingSales)
const isSales = computed(() => form.value?.kind==='item' && form.value.target==='seller')
const visibleItems = computed(() => draft.value.items.map((item,index)=>({item,index}))
  .filter(({item})=>item.replacesItemKey===props.replacementItemKey))
const itemName = ref()
const error = ref('')
let nextDraftKey = 0
const blankCondition = scope => newCatalogRateCondition(props.options,scope)
const labelFor = (choices,value) => choices.find(option=>option.value===value)?.label || value
const percent = basisPoints => (basisPoints/100)+'%'
function itemLabel(item) {
  const recipient = item.target==='referrer' ? '订单直接推荐人'
    : item.target==='person' ? item.personName
    : item.target==='pool' ? props.options.teamPools?.find(pool=>pool.id===item.poolId)?.name
    : item.target==='appointment' ? [labelFor(catalogAppointmentLevels,item.appointmentLevel),labelFor(props.options.roles,item.role)].join(' · ')
    : labelFor(props.options.organizations,item.organization)
  const standard = item.rows?.length ? item.rows.length+' 条费率条件'
    : item.fixedMinor != null ? (item.fixedMinor/100).toFixed(2)+' 元/笔' : percent(item.rateBps)
  const scope = item.ownSales ? '本人销售' : [
    ...(item.salesScopeUnitIds || []).map(id=>labelFor(props.options.salesUnits,id)),
    ...(item.salesScopeCategoryIds || []).map(id=>labelFor(props.options.categories,id)),
  ].join('、') || '本规则全部订单'
  const orderType = props.membershipSku && catalogMembershipTypes.find(option=>option.value===(item.membershipNewOnly ? 'new' : 'all'))?.label
  return [item.label,recipient,standard,scope,orderType].filter(Boolean).join(' · ')
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
      scope:row.externalCompanyDefault ? 'external' : row.companyIds?.length ? 'company' : row.categoryIds?.length ? 'category' : 'general',
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
          <small v-if="options.categories.length" class="finance-rate-priority">
            匹配顺序 {{ catalogRatePriorityLabel }}{{ isSales ? ' → 外部公司默认比例' : '' }} → 通用比例
          </small>
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
    <ul v-if="visibleItems.length || draft.sales && !replacementItemKey" class="catalog-staged-entries" aria-label="待发布新增配置">
      <li v-if="draft.sales && !replacementItemKey">
        <div><span>{{ draft.sales.label }} · {{ draft.sales.rows.length }} 条费率条件</span><small class="order-version">未保存</small></div>
        <a-space :size="0">
          <a-button type="link" size="small" :disabled="!!form" @click="editSales">编辑</a-button>
          <a-button type="link" danger size="small" :disabled="!!form" @click="draft.sales=undefined">移除</a-button>
        </a-space>
      </li>
      <li v-for="{item,index} in visibleItems" :key="'item-'+index">
        <div>
          <span>{{ itemLabel(item) }}</span>
          <small class="order-version">未保存</small>
        </div>
        <a-space :size="0">
          <a-button type="link" size="small" :disabled="!!form" @click="editRecipient(index)">编辑</a-button>
          <a-button type="link" size="small" danger :disabled="!!form" @click="draft.items.splice(index,1)">移除</a-button>
        </a-space>
      </li>
    </ul>
  </section>
</template>

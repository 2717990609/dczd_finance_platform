<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import { catalogFlatMethods, catalogPayeeComplete, catalogRateConditionBody, moneyAsMinor, newCatalogRateCondition } from './rule-component-form'

const props = defineProps({options:Object, component:Object, api:Function, peopleFilters:Object, parseRate:Function})
const emit = defineEmits(['rate-staged'])
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const revising = computed(() => form.value?.kind==='calculation' || form.value?.itemIndex!=null || form.value?.rowNumber || form.value?.draftKey)
const itemName = ref()
const error = ref('')
let nextDraftKey = 0
const formats = [...catalogFlatMethods,{value:'table',label:'按类别或公司设置比例'}]
const calculationFormats = computed(() => {
  if (form.value?.kind!=='calculation') return formats
  const savedFormat = props.component.rows?.length ? 'table' : props.component.fixedMinor!=null ? 'fixed' : 'rate'
  return formats.filter(option=>option.value!==savedFormat)
})
const blankCondition = scope => newCatalogRateCondition(props.options,scope)
const labelFor = (choices,value) => choices.find(option=>option.value===value)?.label || value
const percent = basisPoints => (basisPoints/100)+'%'
function itemLabel(item) {
  const recipient = item.target==='referrer' ? '订单直接推荐人'
    : item.target==='person' ? item.personName : labelFor(props.options.organizations,item.organization)
  const standard = item.rows?.length ? item.rows.length+' 条费率条件'
    : item.fixedMinor != null ? (item.fixedMinor/100).toFixed(2)+' 元/笔' : percent(item.rateBps)
  return [item.label,recipient,standard].join(' · ')
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
async function editRecipient(index) {
  const {fixedMinor,rateBps,rows,...payee} = draft.value.items[index]
  error.value = ''
  form.value = {
    ...payee,kind:'item',itemIndex:index,
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
      scope:row.companyIds?.length ? 'company' : row.categoryIds?.length ? 'category' : 'general',
      companyIds:[...(row.companyIds || [])],categoryIds:[...(row.categoryIds || [])],
      relationship:row.relationship,payee:row.payee,rate:String(row.rateBps/100),
    })) || [blankCondition()],
  }
}
function editCalculation(pending) {
  error.value = ''
  const defaultFormat = props.component.rows?.length ? 'rate' : 'table'
  form.value = {kind:'calculation',itemKey:props.component.itemKey,...calculationForm(pending || {},defaultFormat)}
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
      if (!label || !catalogPayeeComplete(staged)) {
        throw new Error('请填写分项名称，并选好收款对象及适用任职。')
      }
      const item = {label,target:staged.target,role:staged.role,personId:staged.personId,
        personName:staged.personName,organization:staged.organization,...calculationBody(staged)}
      if (staged.itemIndex==null) draft.value.items.push(item)
      else draft.value.items.splice(staged.itemIndex,1,item)
    }
    cancel()
  } catch (err) {
    error.value = err.message
  }
}
watch(form, value => { editing.value = !!value })
defineExpose({addRecipient,editCalculation,editRateScope,addRateCondition:scope=>begin('row',scope)})
</script>

<template>
  <section class="catalog-rule-structure" aria-label="分润分项和费率条件">
    <div v-if="form" class="catalog-add-form" :class="{'catalog-add-rate-row':form.kind==='row'}">
      <a-alert v-if="error" :message="error" type="error" show-icon class="feedback"/>
      <template v-if="form.kind==='row'">
        <CatalogRateCondition v-model="form.row" :options="options" :scope="form.scope" :sales-payee="component?.targetKind==='seller'"/>
      </template>
      <template v-else>
        <template v-if="form.kind==='item'">
          <a-form-item label="收款安排名称" html-for="catalog-add-label" required>
            <a-input id="catalog-add-label" ref="itemName" v-model:value="form.label" :maxlength="80"/>
          </a-form-item>
          <h3 class="form-subhead">发给谁</h3>
          <CatalogPayeeFields v-model="form" prefix="catalog-add" :options="options" :api="api" :people-filters="peopleFilters"/>
          <h3 class="form-subhead">提成怎么算</h3>
        </template>
        <a-form-item :label="form.kind==='calculation' ? '新计提方式' : '计提方式'" html-for="catalog-add-format">
          <a-select id="catalog-add-format" v-model:value="form.format" :options="calculationFormats"/>
        </a-form-item>
        <a-form-item v-if="form.format==='rate'" label="比例（%）" html-for="catalog-add-fixed" required>
          <a-input-number id="catalog-add-fixed" aria-label="比例（%）" v-model:value="form.rate"
            :min="0" :max="100" :precision="2" string-mode/>
        </a-form-item>
        <a-form-item v-else-if="form.format==='fixed'" label="金额（元/笔）" html-for="catalog-add-amount" required>
          <a-input-number id="catalog-add-amount" aria-label="金额（元/笔）" v-model:value="form.amount"
            :min="0" :precision="2" string-mode/>
        </a-form-item>
        <template v-else>
          <div v-for="(_,index) in form.rows" :key="index" class="catalog-add-condition">
            <CatalogRateCondition v-model="form.rows[index]" :options="options" :suffix="'-'+index">
              <a-button v-if="form.rows.length>1" type="link" danger size="small" @click="form.rows.splice(index,1)">移除</a-button>
            </CatalogRateCondition>
          </div>
          <a-button type="dashed" @click="form.rows.push(blankCondition())">再添一个条件</a-button>
        </template>
      </template>
      <div class="form-actions">
        <a-button :size="form.kind==='row' ? 'small' : undefined" @click="cancel">{{ revising ? '取消编辑' : '取消添加' }}</a-button>
        <a-button :size="form.kind==='row' ? 'small' : undefined" type="primary" @click="stage">
          {{ revising ? '应用修改' : form.kind==='item' ? '加入规则' : '加入比例表' }}
        </a-button>
      </div>
    </div>
    <ul v-if="draft.items.length" class="catalog-staged-entries" aria-label="待发布新增配置">
      <li v-for="(item,index) in draft.items" :key="'item-'+index">
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

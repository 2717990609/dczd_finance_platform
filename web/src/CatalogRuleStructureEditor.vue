<script setup>
import { nextTick, ref, watch } from 'vue'
import CatalogPayeeFields from './CatalogPayeeFields.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import { catalogFlatMethods, catalogPayeeComplete, catalogRateConditionBody, catalogSalesPayees, moneyAsMinor, newCatalogRateCondition } from './rule-component-form'

const props = defineProps({options:Object, component:Object, api:Function, peopleFilters:Object, parseRate:Function})
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const itemName = ref()
const error = ref('')
const formats = [...catalogFlatMethods,{value:'table',label:'按类别或公司设置比例'}]
const blankCondition = scope => newCatalogRateCondition(props.options,scope)
const labelFor = (choices,value) => choices.find(option=>option.value===value)?.label || value
const percent = basisPoints => (basisPoints/100)+'%'
function conditionLabel(condition) {
  const company = condition.companyIds?.map(id=>labelFor(props.options.companies,id)).join('、')
  const category = condition.categoryIds?.map(id=>labelFor(props.options.categories,id)).join('、')
  return [company || category || '通用比例',company && category,
    condition.relationship!=='ANY' && labelFor(props.options.relationships,condition.relationship),percent(condition.rateBps),
    condition.payee && labelFor(catalogSalesPayees,condition.payee)].filter(Boolean).join(' · ')
}
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
    : {kind,scope,itemKey:props.component.itemKey,itemLabel:props.component.label,
      row:blankCondition(scope)}
}
async function addRecipient() {
  begin('item')
  await nextTick()
  itemName.value?.focus()
}
function editRateScope(entry, rate) {
  const pending = draft.value.rateRows.find(row=>row.itemKey===props.component.itemKey && row.row===entry.row)
  error.value = ''
  form.value = {kind:'row',itemKey:props.component.itemKey,itemLabel:props.component.label,rowNumber:entry.row,
    row:{scope:pending ? (pending.companyIds?.length ? 'company' : pending.categoryIds?.length ? 'category' : 'general') : entry.scope,
      companyIds:[...(pending?.companyIds || entry.companyIds || [])],categoryIds:[...(pending?.categoryIds || entry.categoryIds || [])],
      relationship:pending?.relationship || entry.relationshipCode,payee:pending?.payee || entry.payee,
      rate:pending ? String(pending.rateBps/100) : rate}}
}
function cancel() {
  form.value = undefined
  error.value = ''
}
function stage() {
  error.value = ''
  const staged = form.value
  try {
    if (staged.kind==='row') {
      const change = {itemKey:staged.itemKey,itemLabel:staged.itemLabel,row:staged.rowNumber,...catalogRateConditionBody(staged.row,staged.scope)}
      const existing = change.row ? draft.value.rateRows.findIndex(row=>row.itemKey===change.itemKey && row.row===change.row) : -1
      if (existing>=0) draft.value.rateRows.splice(existing,1,change)
      else draft.value.rateRows.push(change)
    } else {
      const label = staged.label?.trim()
      if (!label || !catalogPayeeComplete(staged)) {
        throw new Error('请填写分项名称，并选好收款对象及适用任职。')
      }
      const item = {label,target:staged.target,role:staged.role,personId:staged.personId,
        personName:staged.personName,organization:staged.organization}
      if (staged.format==='rate') {
        item.rateBps = props.parseRate(staged.rate)
        if (item.rateBps===undefined) throw new Error('请填写有效比例。')
      } else if (staged.format==='fixed') {
        item.fixedMinor = moneyAsMinor(staged.amount)
        if (item.fixedMinor===undefined) throw new Error('请填写有效金额，最多保留两位小数。')
      } else item.rows = staged.rows.map(row=>catalogRateConditionBody(row))
      draft.value.items.push(item)
    }
    cancel()
  } catch (err) {
    error.value = err.message
  }
}
watch(form, value => { editing.value = !!value })
defineExpose({addRecipient,editRateScope,addRateCondition:scope=>begin('row',scope)})
</script>

<template>
  <section class="catalog-rule-structure" aria-label="分润分项和费率条件">
    <div v-if="form" class="catalog-add-form">
      <a-alert v-if="error" :message="error" type="error" show-icon class="feedback"/>
      <template v-if="form.kind==='row'">
        <CatalogRateCondition v-model="form.row" :options="options" :scope="form.scope" :sales-payee="component?.targetKind==='seller'"/>
      </template>
      <template v-else>
        <a-form-item label="收款安排名称" html-for="catalog-add-label" required>
          <a-input id="catalog-add-label" ref="itemName" v-model:value="form.label" :maxlength="80"/>
        </a-form-item>
        <h3 class="form-subhead">发给谁</h3>
        <CatalogPayeeFields v-model="form" prefix="catalog-add" :options="options" :api="api" :people-filters="peopleFilters"/>
        <h3 class="form-subhead">提成怎么算</h3>
        <a-form-item label="计提方式" html-for="catalog-add-format">
          <a-select id="catalog-add-format" v-model:value="form.format" :options="formats"/>
        </a-form-item>
        <a-form-item v-if="form.format==='rate'" label="新增比例（%）" html-for="catalog-add-fixed" required>
          <a-input-number id="catalog-add-fixed" aria-label="新增比例（%）" v-model:value="form.rate"
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
        <a-button @click="cancel">取消添加</a-button>
        <a-button type="primary" @click="stage">{{ form.kind==='item' ? '加入规则' : form.rowNumber ? '应用修改' : '加入比例表' }}</a-button>
      </div>
    </div>
    <ul v-if="draft.items.length || draft.rateRows.length" class="catalog-staged-entries" aria-label="待发布新增配置">
      <li v-for="(item,index) in draft.items" :key="'item-'+index">
        <span>{{ itemLabel(item) }}</span><a-button type="link" size="small" @click="draft.items.splice(index,1)">移除</a-button>
      </li>
      <li v-for="(row,index) in draft.rateRows" :key="'row-'+index">
        <span>{{ row.itemLabel }} · {{ conditionLabel(row) }}</span><a-button type="link" size="small" @click="draft.rateRows.splice(index,1)">{{ row.row ? '撤销修改' : '移除' }}</a-button>
      </li>
    </ul>
  </section>
</template>

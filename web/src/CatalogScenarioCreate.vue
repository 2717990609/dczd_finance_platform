<script setup>
import { computed, ref, watch } from 'vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'
import RuleImpactPreview from './RuleImpactPreview.vue'
import { catalogRuleImpactPeriods, rateAsBasisPoints } from './rule-component-form'

const props = defineProps({products:Array,initialProduct:Number,initialSku:String,periods:Array,api:Function,formatMoney:Function})
const emit = defineEmits(['close','published'])
const productId = ref(props.initialProduct || (props.products.length===1 ? props.products[0].value : undefined))
const sku = ref()
const name = ref('')
const effectiveFrom = ref('')
const options = ref()
const skus = ref([])
const structure = ref({items:[],rateRows:[]})
const structureEditor = ref()
const editing = ref(false)
const loading = ref(false)
const publishing = ref(false)
const error = ref('')
const impactInput = ref()
const impactBusy = ref(false)
const impactPeriods = computed(() => catalogRuleImpactPeriods(props.periods,productId.value,impactInput.value?.effectiveFrom))
const peopleFilters = computed(() => ({productId:productId.value}))
const recognition = computed(() => {
  const selected = skus.value.find(option=>option.value===sku.value)
  return selected ? (selected.requiresCompletion ? '付款后，订单完成时计提' : '付款后计提') : undefined
})
let optionsRequest = 0

watch(productId, async id=>{
  const request = ++optionsRequest
  options.value = undefined
  skus.value = []
  sku.value = undefined
  structure.value = {items:[],rateRows:[]}
  editing.value = false
  error.value = ''
  if (!id) return
  loading.value = true
  try {
    const result = await props.api('/catalog-rule-rates?'+new URLSearchParams({productId:id}))
    if (request!==optionsRequest) return
    options.value = result.options
    skus.value = result.skus.map(row=>({value:row.code,label:row.label,requiresCompletion:row.requiresCompletion}))
    sku.value = skus.value.some(row=>row.value===props.initialSku) && id===props.initialProduct
      ? props.initialSku : skus.value.length===1 ? skus.value[0].value : undefined
  } catch (err) {
    if (request===optionsRequest) error.value = err.message
  } finally {
    if (request===optionsRequest) loading.value = false
  }
},{immediate:true})

function publicationInput() {
  if (!sku.value || !name.value.trim() || !effectiveFrom.value) throw new Error('请选择业务 SKU、生效时间并填写规则名称。')
  const {sales,items} = structure.value
  if (!sales && !items.length) throw new Error('请至少添加一项收款安排。')
  return {
    effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
    create:{productId:productId.value,sku:sku.value,label:name.value.trim(),salesRates:sales?.rows,salesLabel:sales?.label,
      salesScopeUnitIds:sales?.salesScopeUnitIds},
    newItems:items.map(({personName,ownerName,...item})=>item),
  }
}
function preview() {
  error.value = ''
  try {
    impactInput.value = publicationInput()
  } catch (err) {
    error.value = err.message
  }
}
function compareRule(periodId, signal) {
  return props.api('/catalog-rule-impact',{method:'POST',body:JSON.stringify({periodId,rule:impactInput.value}),signal})
}
async function publish() {
  if (publishing.value || impactBusy.value || editing.value) return
  error.value = ''
  try {
    const input = impactInput.value || publicationInput()
    publishing.value = true
    const result = await props.api('/catalog-rule-rates',{method:'POST',body:JSON.stringify(input)})
    emit('published',result,productId.value,sku.value)
  } catch (err) {
    error.value = err.message
  } finally {
    publishing.value = false
  }
}
</script>

<template>
  <a-drawer open title="新增逐单规则" width="min(1120px, 100vw)" root-class-name="finance-rule-editor"
    :closable="!publishing" :mask-closable="!publishing" :keyboard="!publishing"
    @close="emit('close')">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-form v-show="!impactInput" layout="vertical" size="small" :disabled="publishing">
      <div class="rule-basics">
        <a-form-item label="所属产品" html-for="scenario-create-product" required>
          <a-select id="scenario-create-product" v-model:value="productId" :options="products" placeholder="选择产品"/>
        </a-form-item>
        <a-form-item label="业务 SKU" html-for="scenario-create-sku" :extra="recognition" required>
          <a-select id="scenario-create-sku" v-model:value="sku" :options="skus" :loading="loading" placeholder="选择业务 SKU" show-search option-filter-prop="label"/>
        </a-form-item>
        <a-form-item label="规则名称" html-for="scenario-create-name" required>
          <a-input id="scenario-create-name" v-model:value="name" :maxlength="80"/>
        </a-form-item>
        <a-form-item label="生效时间" html-for="scenario-create-date" required>
          <a-input id="scenario-create-date" v-model:value="effectiveFrom" type="datetime-local"/>
        </a-form-item>
      </div>
      <template v-if="options && sku">
        <div class="rule-blocks-heading">
          <h3 class="form-subhead">收款安排</h3>
          <a-button size="small" :disabled="editing" @click="structureEditor?.addRecipient()">添加收款安排</a-button>
        </div>
        <CatalogRuleStructureEditor ref="structureEditor" v-model="structure" v-model:editing="editing" :product-id="productId"
          :order-type-text="skus.find(option=>option.value===sku)?.label"
          :options="options" :api="api" :people-filters="peopleFilters" :parse-rate="rateAsBasisPoints" :sales-available="!structure.sales"/>
      </template>
    </a-form>
    <RuleImpactPreview v-if="impactInput" v-model:busy="impactBusy" :periods="impactPeriods" :simulate="compareRule" :format-money="formatMoney"/>
    <template #footer>
      <div class="form-actions">
        <a-button v-if="impactInput" :disabled="publishing" @click="impactInput=undefined; error=''">返回编辑</a-button>
        <template v-else>
          <a-button :disabled="publishing" @click="emit('close')">取消</a-button>
          <a-button :disabled="publishing || !options || !sku || editing" @click="preview">预览差额</a-button>
        </template>
        <a-button type="primary" :loading="publishing" :disabled="!options || !sku || editing || impactBusy" @click="publish">保存并生效</a-button>
      </div>
    </template>
  </a-drawer>
</template>

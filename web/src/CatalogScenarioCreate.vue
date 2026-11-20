<script setup>
import { computed, ref, watch } from 'vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'
import { catalogRateConditionBody, catalogRatePriorityLabel, newCatalogRateCondition, rateAsBasisPoints } from './rule-component-form'

const props = defineProps({products:Array,initialProduct:Number,initialSku:String,api:Function})
const emit = defineEmits(['close','published'])
const productId = ref(props.initialProduct || (props.products.length===1 ? props.products[0].value : undefined))
const sku = ref()
const name = ref('')
const effectiveFrom = ref('')
const options = ref()
const skus = ref([])
const rates = ref([])
const structure = ref({items:[],rateRows:[]})
const structureEditor = ref()
const editing = ref(false)
const loading = ref(false)
const publishing = ref(false)
const error = ref('')
const peopleFilters = computed(() => ({productId:productId.value}))
const recognition = computed(() => {
  const selected = skus.value.find(option=>option.value===sku.value)
  return selected ? (selected.requiresCompletion ? '付款后，订单完成时计提' : '付款后计提') : undefined
})
const ratePriority = computed(() => {
  const hasCategories = options.value?.categories.length
  if (!rates.value.some(row=>row.scope==='general')) return hasCategories ? catalogRatePriorityLabel : ''
  return hasCategories ? '公司特例 → 销售类别默认 → 通用比例' : '公司特例 → 通用比例'
})
const blankRate = scope => newCatalogRateCondition(options.value,scope)
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
    rates.value = [blankRate()]
  } catch (err) {
    if (request===optionsRequest) error.value = err.message
  } finally {
    if (request===optionsRequest) loading.value = false
  }
},{immediate:true})

async function publish() {
  if (publishing.value) return
  error.value = ''
  try {
    if (!sku.value || !name.value.trim() || !effectiveFrom.value) throw new Error('请选择业务 SKU、生效时间并填写规则名称。')
    if (!rates.value.length && !structure.value.items.length) throw new Error('请至少添加一项收款安排。')
    const salesRates = rates.value.map(row=>catalogRateConditionBody(row))
    publishing.value = true
    const result = await props.api('/catalog-rule-rates',{method:'POST',body:JSON.stringify({
      effectiveFrom:new Date(effectiveFrom.value+'+08:00').toISOString(),
      create:{productId:productId.value,sku:sku.value,label:name.value.trim(),salesRates},
      newItems:structure.value.items.map(({personName,...item})=>item),
    })})
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
    <a-form layout="vertical" :disabled="publishing">
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
        <section v-if="rates.length" class="rule-block rule-block-body catalog-first-rates" aria-label="成交销售">
          <div class="rule-blocks-heading">
            <h3 class="form-subhead">成交销售</h3>
            <small v-if="ratePriority" class="finance-rate-priority">{{ ratePriority }}</small>
            <a-button type="link" danger size="small" @click="rates=[]">移除成交销售</a-button>
          </div>
          <div v-for="(_,index) in rates" :key="index" class="catalog-add-condition">
            <CatalogRateCondition v-model="rates[index]" :options="options" :suffix="'-first-'+index" sales-payee>
              <a-button v-if="rates.length>1" type="link" danger size="small" @click="rates.splice(index,1)">移除</a-button>
            </CatalogRateCondition>
          </div>
          <a-space wrap>
            <a-button v-if="options.categories.length" size="small" @click="rates.push(blankRate('category'))">添加类别默认比例</a-button>
            <a-button size="small" @click="rates.push(blankRate('company'))">添加公司特例</a-button>
            <a-button size="small" @click="rates.push(blankRate('general'))">添加通用比例</a-button>
          </a-space>
        </section>
        <div class="rule-blocks-heading">
          <h3 class="form-subhead">{{ rates.length ? '其他收款安排' : '收款安排' }}</h3>
          <a-space wrap :size="4">
            <a-button v-if="!rates.length" size="small" :disabled="editing" @click="rates=[blankRate()]">添加成交销售</a-button>
            <a-button size="small" :disabled="editing" @click="structureEditor?.addRecipient()">添加收款安排</a-button>
          </a-space>
        </div>
        <CatalogRuleStructureEditor ref="structureEditor" v-model="structure" v-model:editing="editing"
          :options="options" :api="api" :people-filters="peopleFilters" :parse-rate="rateAsBasisPoints"/>
      </template>
    </a-form>
    <template #footer>
      <div class="form-actions">
        <a-button :disabled="publishing" @click="emit('close')">取消</a-button>
        <a-button type="primary" :loading="publishing" :disabled="!options || !sku || editing" @click="publish">保存并生效</a-button>
      </div>
    </template>
  </a-drawer>
</template>

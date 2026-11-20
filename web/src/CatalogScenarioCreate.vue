<script setup>
import { computed, ref, watch } from 'vue'
import CatalogRateCondition from './CatalogRateCondition.vue'
import CatalogRuleStructureEditor from './CatalogRuleStructureEditor.vue'
import { rateAsBasisPoints } from './rule-component-form'

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
const blankRate = () => ({company:'ANY',relationship:'ANY'})
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
    skus.value = result.skus.map(row=>({value:row.code,label:row.label}))
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
    const salesRates = rates.value.map(({rate,...row})=>{
      const value = rateAsBasisPoints(rate)
      if (!row.company || !row.relationship || value===undefined) throw new Error('请核对每行的销售范围和比例。')
      return {...row,rateBps:value}
    })
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
  <a-drawer open title="新增逐单规则" width="min(1120px, 100vw)" :closable="!publishing" :mask-closable="!publishing"
    @close="emit('close')">
    <a-alert v-if="error" type="error" :message="error" show-icon class="feedback"/>
    <a-form layout="vertical" :disabled="publishing">
      <div class="rule-basics">
        <a-form-item label="所属产品" html-for="scenario-create-product" required>
          <a-select id="scenario-create-product" v-model:value="productId" :options="products" placeholder="选择产品"/>
        </a-form-item>
        <a-form-item label="业务 SKU" html-for="scenario-create-sku" required>
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
        <section class="rule-block rule-block-body" aria-label="成交销售">
          <h3 class="form-subhead">成交销售</h3>
          <div v-for="(_,index) in rates" :key="index" class="catalog-add-condition">
            <CatalogRateCondition v-model="rates[index]" :options="options" :suffix="'-first-'+index"/>
            <a-button v-if="rates.length>1" type="link" @click="rates.splice(index,1)">移除此条件</a-button>
          </div>
          <a-button type="dashed" @click="rates.push(blankRate())">添加比例条件</a-button>
        </section>
        <div class="rule-blocks-heading">
          <h3 class="form-subhead">其他收款安排</h3>
          <a-button :disabled="editing" @click="structureEditor?.addRecipient()">添加收款安排</a-button>
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

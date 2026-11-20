<script setup>
import { computed, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'

const props = defineProps({component:Object,index:Number,companies:Array,salesCategories:Array,recipients:Array,amounts:Array,
  sourceRoles:Array,payeeRoles:Array,people:Array,units:Array,pools:Array,createPool:Function})
const categories = computed(() => props.component.companySales.categories)
const standards = computed(() => categories.value.map((group,index)=>({group,index}))
  .sort((left,right)=>Number(left.group.scope==='companies')-Number(right.group.scope==='companies')))
const payee = computed(() => props.component.companySales.payee)
const payeeKinds = [
  {value:'sales_company',label:'销售所属公司'},
  {value:'order_person',label:'订单上的人员'},
  {value:'person',label:'指定个人'},
  {value:'company',label:'指定公司'},
  {value:'pool',label:'团队池'},
]
const hasRecommendations = computed(() => props.component.classMember==='new' && categories.value.some(category=>
  category.recommendationRate>0 || category.overrides.some(override=>override.recommendationRate>0)))
const internalOptions = computed(() => props.companies.filter(option=>!categories.value.some(c=>
  (c.scope==='companies' && c.companies.includes(option.value)) || c.overrides.some(row=>row.companyIds?.includes(option.value)))))
const amounts = computed(() => props.amounts.filter(option =>
  props.component.classMember==='retrain' || option.value==='rate'))
const field = name => 'practice-company-'+props.index+'-'+name
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())

function availableCompanies(category) {
  return props.companies.filter(option =>
    !props.component.companySales.internalCompanies.includes(option.value) &&
    !categories.value.some(row=>row!==category &&
      ((row.scope==='companies' && row.companies.includes(option.value)) || row.overrides.some(override=>override.companyIds?.includes(option.value)))))
}
function selectCompanies(category, companies) {
  category.companies = companies
  for (const override of category.overrides) override.companyIds = override.companyIds.filter(id=>companies.includes(id))
  category.overrides = category.overrides.filter(row=>row.companyIds.length)
}
function overrideOptions(category, override) {
  return availableCompanies(category).filter(option=>(category.scope==='category' || category.companies.includes(option.value)) &&
    !category.overrides.some(row=>row!==override && row.companyIds?.includes(option.value)))
}
function salesCategoryOptions(category) {
  return props.salesCategories.filter(option=>!categories.value.some(other=>
    other!==category && other.scope==='category' && other.salesCategoryIds.includes(option.value)))
}
function addStandard(scope) {
  categories.value.push({scope,mode:amounts.value[0]?.value,companies:[],salesCategoryIds:[],overrides:[]})
}
function modeChanged(item, mode) {
  item.mode = mode
  item.value = undefined
}
watch(amounts, choices => {
  for (const category of categories.value) {
    for (const row of [category,...category.overrides]) {
      if (!choices.some(choice=>choice.value===row.mode)) modeChanged(row,choices[0]?.value)
    }
  }
})
</script>

<template>
  <div class="company-value company-meta">
    <a-form-item label="收款安排名称" :html-for="field('label')" required>
      <a-input :id="field('label')" v-model:value="component.companySales.label" placeholder="输入安排名称" :maxlength="80"/>
    </a-form-item>
    <a-form-item label="结算对象">
      {{ payee.kind==='sales_company' ? '销售所属外部公司' : payeeKinds.find(option=>option.value===payee.kind)?.label }}
    </a-form-item>
  </div>
  <div class="company-rates-heading">
    <h4 class="rule-item-section">提成怎么算</h4>
    <small>公司特例优先</small>
  </div>
  <section v-for="({group:category,index:i}) in standards" :key="i" class="company-category">
    <div class="company-category-title">
      <strong>{{ category.scope==='category' ? '类别默认' : '公司特例' }}</strong>
      <a-button v-if="categories.length>1" size="small" type="link" danger @click="categories.splice(i,1)">移除</a-button>
    </div>
    <div class="company-value company-standard-fields">
    <a-form-item v-if="category.scope==='category'" label="销售类别" :html-for="field('sales-category-'+i)" required>
      <a-select :id="field('sales-category-'+i)" v-model:value="category.salesCategoryIds" :options="salesCategoryOptions(category)"
        mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择销售类别"/>
    </a-form-item>
      <a-form-item v-else label="特例公司" :html-for="field('companies-'+i)" required>
        <a-select :id="field('companies-'+i)" :value="category.companies" :options="availableCompanies(category)"
          mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择公司" @change="selectCompanies(category,$event)"/>
      </a-form-item>
      <a-form-item v-if="amounts.length>1" label="计算方式" :html-for="field('method-'+i)">
        <a-select :id="field('method-'+i)" :value="category.mode" :options="amounts" @change="modeChanged(category,$event)"/>
      </a-form-item>
      <a-form-item :label="category.mode==='rate' ? '按报名净实收（%）' : '每份报名（元）'" :html-for="field('value-'+i)" required>
        <a-input-number :id="field('value-'+i)" v-model:value="category.value" :min="0"
          :max="category.mode==='rate'?100:undefined" :precision="category.mode==='rate'?4:2"/>
      </a-form-item>
    </div>
    <a-collapse v-if="component.classMember==='new' || category.overrides.length" ghost class="company-advanced">
      <a-collapse-panel v-if="component.classMember==='new'" key="recommendation"
        :header="category.recommendationRate > 0 ? '推荐公司分润 · '+category.recommendationRate+'%' : '推荐公司分润'">
        <a-form-item label="新报推荐比例（%）" :html-for="field('recommendation-'+i)">
          <a-input-number :id="field('recommendation-'+i)" v-model:value="category.recommendationRate" :min="0" :max="100" :precision="4"/>
        </a-form-item>
      </a-collapse-panel>
      <a-collapse-panel v-if="category.overrides.length" key="overrides" :header="'组内公司特例（'+category.overrides.length+'）'">
        <div class="company-override" v-for="(override,j) in category.overrides" :key="j">
          <a-form-item label="公司" :html-for="field('override-'+i+'-'+j)" required>
            <a-select :id="field('override-'+i+'-'+j)" v-model:value="override.companyIds"
              :options="overrideOptions(category,override)" mode="multiple" :max-tag-count="3" :filter-option="byName"/>
          </a-form-item>
          <div class="company-value">
            <a-form-item v-if="amounts.length>1" label="计算方式" :html-for="field('override-method-'+i+'-'+j)">
              <a-select :id="field('override-method-'+i+'-'+j)" :value="override.mode" :options="amounts" @change="modeChanged(override,$event)"/>
            </a-form-item>
            <a-form-item :label="override.mode==='rate' ? '按报名净实收（%）' : '每份报名（元）'" :html-for="field('override-value-'+i+'-'+j)" required>
              <a-input-number :id="field('override-value-'+i+'-'+j)" v-model:value="override.value" :min="0"
                :max="override.mode==='rate'?100:undefined" :precision="override.mode==='rate'?4:2"/>
            </a-form-item>
          </div>
          <a-form-item v-if="component.classMember==='new'" label="新报单独推荐比例（%）" :html-for="field('override-recommendation-'+i+'-'+j)"
            extra="留空沿用类别比例">
            <a-input-number :id="field('override-recommendation-'+i+'-'+j)" v-model:value="override.recommendationRate"
              :min="0" :max="100" :precision="4"/>
          </a-form-item>
          <a-button type="link" danger @click="category.overrides.splice(j,1)">移除单独标准</a-button>
        </div>
      </a-collapse-panel>
    </a-collapse>
  </section>
  <a-space wrap class="company-actions">
    <a-button v-if="salesCategories.length" type="dashed" @click="addStandard('category')">添加类别标准</a-button>
    <a-button type="dashed" @click="addStandard('companies')">添加公司特例</a-button>
  </a-space>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse ghost>
    <a-collapse-panel key="advanced" header="更多配置">
      <div class="company-value">
        <a-form-item label="收款对象" :html-for="field('payee')" required>
          <a-select :id="field('payee')" v-model:value="payee.kind" :options="payeeKinds"/>
        </a-form-item>
        <a-form-item v-if="payee.kind==='order_person'" label="从订单取谁" :html-for="field('payee-role')" required>
          <a-select :id="field('payee-role')" v-model:value="payee.role" :options="payeeRoles" show-search :filter-option="byName"/>
        </a-form-item>
        <a-form-item v-if="payee.kind==='person'" label="收款人" :html-for="field('payee-person')" required>
          <a-select :id="field('payee-person')" v-model:value="payee.personId" :options="people" show-search :filter-option="byName"/>
        </a-form-item>
        <a-form-item v-if="payee.kind==='company'" label="收款公司" :html-for="field('payee-company')" required>
          <a-select :id="field('payee-company')" v-model:value="payee.companyId" :options="recipients" show-search :filter-option="byName"/>
        </a-form-item>
      </div>
      <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
        :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
      <a-form-item label="业绩归给订单里的谁" :html-for="field('role')" required>
        <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles"/>
      </a-form-item>
      <a-form-item label="沿用个人提成的内部公司" :html-for="field('internal')">
        <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
          mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
      </a-form-item>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.company-category {border:1px solid var(--line);border-radius:6px;padding:8px 12px;margin-bottom:8px}
.company-category-title,.company-rates-heading {display:flex;align-items:center;justify-content:space-between;gap:8px}
.company-category-title {margin-bottom:6px;min-height:24px}
.company-rates-heading {margin-bottom:6px}
.company-rates-heading h4 {margin:0}
.company-rates-heading small {color:var(--muted)}
.company-value {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:4px 12px}
.company-standard-fields {grid-template-columns:minmax(180px,2fr) repeat(2,minmax(90px,1fr))}
.company-meta :deep(.ant-form-item),.company-standard-fields :deep(.ant-form-item) {margin-bottom:6px}
.company-value :deep(.ant-form-item-label) {padding-bottom:4px}
.company-advanced :deep(.ant-collapse-header) {padding:4px 0}
.company-advanced :deep(.ant-collapse-content-box) {padding:4px 0}
.company-value :deep(.ant-input-number) {width:100%}
.company-actions {margin:2px 0 8px}
.company-override {border-bottom:1px solid var(--line);padding-bottom:8px;margin-bottom:10px}
@media (max-width:520px) { .company-value {grid-template-columns:minmax(0,1fr)} }
</style>

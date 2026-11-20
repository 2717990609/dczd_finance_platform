<script setup>
import { computed, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import { catalogRatePriorityLabel } from './rule-component-form'

const props = defineProps({component:Object,index:Number,companies:Array,salesCategories:Array,recipients:Array,amounts:Array,
  sourceRoles:Array,payeeRoles:Array,people:Array,units:Array,pools:Array,createPool:Function,memberChoices:Array})
const emit = defineEmits(['member-change'])
const categories = computed(() => props.component.companySales.categories)
const standardGroups = computed(() => [
  {scope:'category',label:'按销售类别',action:'添加类别标准'},
  {scope:'companies',label:'公司特例',action:'添加公司特例'},
].map(section=>({...section,rows:categories.value.map((standard,index)=>({standard,index}))
  .filter(({standard})=>standard.scope===section.scope)}))
  .filter(section=>section.scope==='companies' || props.salesCategories.length || section.rows.length))
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
    <a-form-item label="适用报名" :html-for="'rule-member-'+index">
      <a-select :id="'rule-member-'+index" v-model:value="component.classMember" :options="memberChoices" @change="emit('member-change')"/>
    </a-form-item>
    <a-form-item label="收款安排名称" :html-for="field('label')" required>
      <a-input :id="field('label')" v-model:value="component.companySales.label" placeholder="输入安排名称" :maxlength="80"/>
    </a-form-item>
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
    <slot name="actions"/>
  </div>
  <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
    :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
  <div v-if="standardGroups.length>1" class="company-priority finance-rate-priority">{{ catalogRatePriorityLabel }}</div>
  <section v-for="group in standardGroups" :key="group.scope" class="company-standard-group">
    <div class="company-category-title">
      <h4 class="rule-item-section">{{ group.label }}</h4>
      <a-button v-if="group.scope==='companies' || salesCategories.length" size="small" @click="addStandard(group.scope)">{{ group.action }}</a-button>
    </div>
    <div v-for="({standard:category,index:i}) in group.rows" :key="i" class="company-category">
      <div class="company-standard-fields">
        <a-form-item v-if="category.scope==='category'" class="company-scope-field" label="销售类别" :html-for="field('sales-category-'+i)" required>
          <a-select :id="field('sales-category-'+i)" v-model:value="category.salesCategoryIds" :options="salesCategoryOptions(category)"
            mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择销售类别"/>
        </a-form-item>
        <a-form-item v-else class="company-scope-field" label="适用公司" :html-for="field('companies-'+i)" required>
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
        <a-form-item v-if="component.classMember==='new'" label="推荐比例（%）" :html-for="field('recommendation-'+i)">
          <a-input-number :id="field('recommendation-'+i)" v-model:value="category.recommendationRate" :min="0" :max="100" :precision="4"/>
        </a-form-item>
        <a-button v-if="categories.length>1" size="small" type="link" danger class="company-standard-remove" @click="categories.splice(i,1)">移除</a-button>
      </div>
      <a-collapse v-if="category.overrides.length" ghost class="company-advanced">
        <a-collapse-panel key="overrides" :header="'组内公司特例（'+category.overrides.length+'）'">
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
    </div>
  </section>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse ghost class="company-advanced">
    <a-collapse-panel key="advanced" header="更多配置">
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
.company-standard-group {border-top:1px solid var(--line);margin:4px 0}
.company-category-title {display:flex;align-items:center;justify-content:space-between;gap:8px;min-height:32px;padding:3px 0}
.company-category-title h4 {margin:0}
.company-category {padding:4px 8px 6px;background:#f6f8fb;border-radius:4px;margin-bottom:4px}
.company-priority {margin:4px 0 6px}
.company-value {display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:4px 12px}
.company-meta {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px;margin-bottom:6px}
.company-meta :deep(.ant-form-item) {flex:1 1 140px;min-width:0;margin-bottom:0}
.company-meta :deep(.rule-remove) {margin-bottom:4px}
.company-standard-fields {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px}
.company-category .company-standard-fields :deep(.ant-form-item) {flex:1 1 130px;min-width:0;margin:0}
.company-standard-fields :deep(.company-scope-field) {flex:3 1 220px}
.company-value :deep(.ant-form-item-label),.company-standard-fields :deep(.ant-form-item-label) {padding-bottom:2px}
.company-standard-remove {margin:0 0 4px auto}
.company-advanced :deep(.ant-collapse-header) {padding:4px 0}
.company-advanced :deep(.ant-collapse-content-box) {padding:4px 0}
.company-value :deep(.ant-input-number),.company-standard-fields :deep(.ant-input-number) {width:100%}
.company-override {border-bottom:1px solid var(--line);padding-bottom:8px;margin-bottom:10px}
@media (max-width:520px) { .company-value {grid-template-columns:minmax(0,1fr)} }
</style>

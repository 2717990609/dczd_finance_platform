<script setup>
import { computed, ref, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import { catalogRatePriorityLabel } from './rule-component-form'

const props = defineProps({component:Object,index:Number,companies:Array,salesCategories:Array,recipients:Array,amounts:Array,
  sourceRoles:Array,payeeRoles:Array,people:Array,units:Array,pools:Array,createPool:Function,memberChoices:Array})
const emit = defineEmits(['member-change'])
const categories = computed(() => props.component.companySales.categories)
const standardScope = ref('category')
const overrides = computed(() => categories.value.flatMap((category,index)=>
  category.overrides.map((override,position)=>({category,index,override,position}))))
const standardGroups = computed(() => [
  {scope:'category',label:'销售类别默认',action:'添加类别标准'},
  {scope:'companies',label:'公司特例',action:'添加公司特例'},
].map(section=>({...section,rows:categories.value.map((standard,index)=>({standard,index}))
  .filter(({standard})=>standard.scope===section.scope)}))
  .filter(section=>section.scope==='companies' || props.salesCategories.length || section.rows.length))
const activeGroup = computed(() => standardGroups.value.find(group=>group.scope===standardScope.value) || standardGroups.value[0])
const standardCount = group => group.rows.length + (group.scope==='companies' ? overrides.value.length : 0)
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
  <div class="company-meta">
    <a-form-item label="适用报名" :html-for="'rule-member-'+index">
      <a-select :id="'rule-member-'+index" v-model:value="component.classMember" :options="memberChoices" size="small" @change="emit('member-change')"/>
    </a-form-item>
    <a-form-item label="收款安排名称" :html-for="field('label')" required>
      <a-input :id="field('label')" v-model:value="component.companySales.label" size="small" :maxlength="80"/>
    </a-form-item>
    <a-form-item label="收款对象" :html-for="field('payee')" required>
      <a-select :id="field('payee')" v-model:value="payee.kind" :options="payeeKinds" size="small"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='order_person'" label="从订单取谁" :html-for="field('payee-role')" required>
      <a-select :id="field('payee-role')" v-model:value="payee.role" :options="payeeRoles" size="small" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='person'" label="收款人" :html-for="field('payee-person')" required>
      <a-select :id="field('payee-person')" v-model:value="payee.personId" :options="people" size="small" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='company'" label="收款公司" :html-for="field('payee-company')" required>
      <a-select :id="field('payee-company')" v-model:value="payee.companyId" :options="recipients" size="small" show-search :filter-option="byName"/>
    </a-form-item>
    <slot name="actions"/>
  </div>
  <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
    :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
  <div class="company-standard-toolbar">
    <a-radio-group v-if="standardGroups.length>1" :value="activeGroup.scope" role="radiogroup" aria-label="查看计提标准"
      button-style="solid" size="small" @update:value="standardScope=$event">
      <a-radio-button v-for="group in standardGroups" :key="group.scope" :value="group.scope">
        <span>{{ group.label }}</span><small v-if="standardCount(group)" class="company-standard-count">{{ standardCount(group) }}</small>
      </a-radio-button>
    </a-radio-group>
    <strong v-else>{{ activeGroup.label }}</strong>
    <small v-if="standardGroups.length>1" class="finance-rate-priority">{{ catalogRatePriorityLabel }}</small>
    <a-button v-if="activeGroup.scope==='companies' || salesCategories.length" class="company-standard-add" size="small"
      @click="addStandard(activeGroup.scope)">{{ activeGroup.action }}</a-button>
  </div>
  <section v-for="group in standardGroups" v-show="group.scope===activeGroup.scope" :key="group.scope" :aria-label="group.label"
    class="company-standard-table" :class="{'has-method':amounts.length>1,'has-recommendation':component.classMember==='new'}">
    <p v-if="!standardCount(group)" class="company-standard-empty">{{ group.scope==='category' ? '暂无类别标准' : '暂无公司特例' }}</p>
    <div v-else class="company-standard-fields company-standard-column-labels" aria-hidden="true">
      <span>{{ group.scope==='category' ? '销售类别' : '适用公司' }}</span>
      <span v-if="amounts.length>1">计算方式</span>
      <span>{{ amounts.length>1 ? '计提标准' : '按报名净实收（%）' }}</span>
      <span v-if="component.classMember==='new'">推荐比例（%）</span>
    </div>
    <div v-for="({standard:category,index:i}) in group.rows" :key="i" class="company-category company-standard-fields">
      <a-form-item v-if="category.scope==='category'" class="company-scope-field" label="销售类别" :html-for="field('sales-category-'+i)" required>
        <a-select :id="field('sales-category-'+i)" v-model:value="category.salesCategoryIds" :options="salesCategoryOptions(category)" size="small"
          mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择销售类别"/>
      </a-form-item>
      <a-form-item v-else class="company-scope-field" label="适用公司" :html-for="field('companies-'+i)" required>
        <a-select :id="field('companies-'+i)" :value="category.companies" :options="availableCompanies(category)" size="small"
          mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择公司" @change="selectCompanies(category,$event)"/>
      </a-form-item>
      <a-form-item v-if="amounts.length>1" label="计算方式" :html-for="field('method-'+i)">
        <a-select :id="field('method-'+i)" :value="category.mode" :options="amounts" size="small" @change="modeChanged(category,$event)"/>
      </a-form-item>
      <a-form-item :label="category.mode==='rate' ? '按报名净实收（%）' : '每份报名（元）'" :html-for="field('value-'+i)" required>
        <a-input-number :id="field('value-'+i)" v-model:value="category.value" size="small" :min="0"
          :addon-after="amounts.length>1 ? (category.mode==='rate' ? '%' : '元') : undefined"
          :max="category.mode==='rate'?100:undefined" :precision="category.mode==='rate'?4:2"/>
      </a-form-item>
      <a-form-item v-if="component.classMember==='new'" label="推荐比例（%）" :html-for="field('recommendation-'+i)">
        <a-input-number :id="field('recommendation-'+i)" v-model:value="category.recommendationRate" size="small" :min="0" :max="100" :precision="4"/>
      </a-form-item>
      <a-button v-if="categories.length>1" size="small" type="link" danger class="company-standard-remove" @click="categories.splice(i,1)">移除</a-button>
    </div>
    <template v-if="group.scope==='companies'">
      <div class="company-category company-standard-fields" v-for="({category,index:i,override,position:j}) in overrides" :key="i+'-'+j">
        <a-form-item class="company-scope-field" label="特例公司" :html-for="field('override-'+i+'-'+j)" required>
          <a-select :id="field('override-'+i+'-'+j)" v-model:value="override.companyIds"
            :options="overrideOptions(category,override)" size="small" mode="multiple" :max-tag-count="3" :filter-option="byName"/>
        </a-form-item>
        <a-form-item v-if="amounts.length>1" label="计算方式" :html-for="field('override-method-'+i+'-'+j)">
          <a-select :id="field('override-method-'+i+'-'+j)" :value="override.mode" :options="amounts" size="small" @change="modeChanged(override,$event)"/>
        </a-form-item>
        <a-form-item :label="override.mode==='rate' ? '按报名净实收（%）' : '每份报名（元）'" :html-for="field('override-value-'+i+'-'+j)" required>
          <a-input-number :id="field('override-value-'+i+'-'+j)" v-model:value="override.value" size="small" :min="0"
            :addon-after="amounts.length>1 ? (override.mode==='rate' ? '%' : '元') : undefined"
            :max="override.mode==='rate'?100:undefined" :precision="override.mode==='rate'?4:2"/>
        </a-form-item>
        <a-form-item v-if="component.classMember==='new'" label="推荐比例（%）" :html-for="field('override-recommendation-'+i+'-'+j)">
          <a-input-number :id="field('override-recommendation-'+i+'-'+j)" v-model:value="override.recommendationRate"
            size="small" :min="0" :max="100" :precision="4" :placeholder="'沿用 '+(category.recommendationRate ?? 0)+'%'"/>
        </a-form-item>
        <a-button type="link" danger size="small" class="company-standard-remove" @click="category.overrides.splice(j,1)">移除</a-button>
      </div>
    </template>
  </section>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse ghost class="company-advanced">
    <a-collapse-panel key="advanced" header="更多配置">
      <div class="rule-basics">
        <a-form-item label="业绩归给订单里的谁" :html-for="field('role')" required>
          <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles" size="small"/>
        </a-form-item>
        <a-form-item label="沿用个人提成的内部公司" :html-for="field('internal')">
          <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
            mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" size="small"/>
        </a-form-item>
      </div>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.company-standard-toolbar {display:flex;flex-wrap:wrap;align-items:center;gap:6px 10px;border-top:1px solid var(--line);padding:6px 0}
.company-standard-add {margin-inline-start:auto}
.company-standard-count {margin-inline-start:5px;font-size:12px}
.company-standard-empty {margin:0;padding:3px 0 7px;color:var(--muted);font-size:13px}
.company-standard-table {container-type:inline-size}
.company-standard-table.has-method {--company-method-column:minmax(0,1.25fr)}
.company-standard-table.has-recommendation {--company-recommendation-column:minmax(0,1fr)}
.company-category {padding:4px 8px 6px;background:#f6f8fb;border-radius:4px;margin-bottom:4px}
.company-meta {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px;margin-bottom:6px}
.company-meta :deep(.ant-form-item) {flex:1 1 140px;min-width:0;margin-bottom:0}
.company-meta :deep(.rule-remove) {margin-bottom:0}
.company-standard-fields {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px}
.company-standard-column-labels {display:none;color:var(--muted);font-size:13px;padding:3px 8px;background:#f6f8fb;border-radius:4px 4px 0 0}
.company-standard-fields :deep(.ant-form-item) {flex:1 1 130px;min-width:0;margin:0}
.company-standard-fields :deep(.ant-form-item.company-scope-field) {flex:3 1 220px}
.company-meta :deep(.ant-form-item-label),.company-standard-fields :deep(.ant-form-item-label) {padding-bottom:2px}
.company-standard-remove {margin:0 0 0 auto}
.company-advanced :deep(.ant-collapse-header) {padding:4px 0}
.company-advanced :deep(.ant-collapse-content-box) {padding:4px 0}
.company-standard-fields :deep(.ant-input-number),.company-standard-fields :deep(.ant-input-number-group-wrapper) {width:100%}
@container (min-width:760px) {
  .company-standard-fields {
    display:grid;align-items:center;
    grid-template-columns:minmax(0,3fr) var(--company-method-column,) minmax(0,1.25fr) var(--company-recommendation-column,) 48px;
  }
  .company-category {padding-block:3px;margin-bottom:0;background:white;border-bottom:1px solid var(--line);border-radius:0}
  .company-category :deep(.ant-form-item-label) {position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
}
</style>

<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleScopeSelect from './RuleScopeSelect.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import RuleEditableTable from './RuleEditableTable.vue'
import { practiceCompanyMatchLabels, practiceCompanyRatePriority,
  practiceCompanyScopes } from './rule-component-form'

const props = defineProps({component:Object,index:Number,companies:Array,organizations:Array,salesCategories:Array,recipients:Array,amounts:Array,
  payeeRoles:Array,people:Array,units:Array,pools:Array,memberChoices:Array})
const emit = defineEmits(['member-change'])
const sales = computed(() => props.component.companySales)
const categories = computed(() => sales.value.categories)
const overrides = computed(() => categories.value.flatMap((category,index)=>
  category.overrides.map((override,position)=>({category,index,override,position}))))
const standardRowsByScope = computed(() => ({
  companies:categories.value.map((standard,index)=>({standard,index})).filter(({standard})=>standard.scope==='companies'),
  category:categories.value.map((standard,index)=>({standard,index})).filter(({standard})=>standard.scope==='category'),
}))
const pageSize = 8
const page = ref(1)
const companyRows = computed(() => [...standardRowsByScope.value.companies,...overrides.value])
const scopes = computed(() => practiceCompanyScopes(sales.value))
const scopeMode = computed(() => sales.value.scopeMode || (sales.value.ownSales ? 'own' : sales.value.general ? 'all' : 'team'))
const organizationCount = computed(() => scopes.value.companies.length + scopes.value.units.length)
const scopeOptions = computed(() => [
  {value:'category',count:scopes.value.categories.length},
  {value:'companies',count:organizationCount.value},
  ...(scopeMode.value==='team' ? [] : [{value:'general'}]),
])
const scope = ref(scopeMode.value!=='team' ? 'general' : scopes.value.categories.length ? 'category' :
  organizationCount.value ? 'companies' : 'category')
watch(scope,()=>{ page.value=1 },{flush:'sync'})
const standardAction = computed(() => scope.value==='category' ? '添加销售类别' : '添加指定组织')
const priority = computed(() => practiceCompanyRatePriority(scopes.value))
const payee = computed(() => props.component.companySales.payee)
watch(()=>payee.value.kind, kind=>{
  if (kind!=='person' && scopeMode.value==='own') setScopeMode('team')
})
const payeeKinds = [
  {value:'sales_company',label:'销售所属公司'},
  {value:'order_person',label:'订单上的人员'},
  {value:'person',label:'指定个人'},
  {value:'company',label:'指定公司'},
  {value:'pool',label:'团队池'},
]
const hasRecommendations = computed(() => props.component.classMember==='new' && categories.value.some(category=>
  category.recommendationRate>0 || category.overrides.some(override=>override.recommendationRate>0)))
const legacyRecommendations = computed(() => props.component.classMember==='new' &&
  (hasRecommendations.value || props.component.companySales.recommendationRecipients?.length))
const companyAssignments = computed(() => {
  const assignments = new Map()
  for (const row of categories.value) {
    const ids = (row.scope==='companies' ? row.companies : []).concat(row.overrides.flatMap(override=>override.companyIds || []))
    for (const id of ids) {
      if (!assignments.has(id)) assignments.set(id,new Set())
      assignments.get(id).add(row)
    }
  }
  return assignments
})
const internalCompanies = computed(() => new Set(sales.value.internalCompanies))
const internalOptions = computed(() => props.companies.filter(option=>!companyAssignments.value.has(option.value)))
const amounts = computed(() => props.amounts)
const tableRows = computed(() => {
  const entries = scope.value==='companies' ? companyRows.value : standardRowsByScope.value.category
  return entries.map(entry=>({...entry,term:entry.override || entry.standard,category:entry.category || entry.standard}))
})
const tableColumns = computed(() => [
  scope.value==='category' ? '适用销售类别' : '适用销售组织',
  ...(amounts.value.length>1 ? ['计提方式','计提标准'] : ['比例（%）']),
  ...(legacyRecommendations.value ? ['推荐比例（%）'] : []),
])
const defaultMethods = ref({})
const activeStandards = computed(() => scope.value==='general' ? [sales.value.general] : scope.value==='companies'
  ? companyRows.value.map(row=>row.standard || row.override)
  : standardRowsByScope.value.category.map(row=>row.standard))
const activeMethod = computed(() => {
  if (!activeStandards.value.length) return defaultMethods.value[scope.value] ?? amounts.value[0]?.value
  const modes = new Set(activeStandards.value.map(row=>row.mode))
  return modes.size===1 ? activeStandards.value[0].mode : undefined
})
const field = name => 'practice-company-'+props.index+'-'+name
const termField = (entry, name) => field(entry.override ? 'override-'+name+'-'+entry.index+'-'+entry.position : name+'-'+entry.index)
const sourceOrganizations = computed(() => new Map(props.companies.map(option=>[option.value,option.scope?.organizationId])))
function unitIDs(ids, legacy) {
  return [...(ids || []),...(legacy || []).map(id=>sourceOrganizations.value.get(id)).filter(Boolean)]
}
function organizationOptions(category, current) {
  const taken = new Set()
  for (const row of categories.value) {
    if (row!==category) for (const id of unitIDs(row.salesUnits,row.companies)) taken.add(id)
    for (const override of row.overrides) {
      if (override!==current) for (const id of unitIDs(override.salesUnits,override.companyIds)) taken.add(id)
    }
  }
  const selected = new Set(unitIDs(current?.salesUnits || category.salesUnits,current?.companyIds || category.companies))
  const parent = current && category.scope==='companies' ? new Set(unitIDs(category.salesUnits,category.companies)) : undefined
  return props.organizations.filter(option=>selected.has(option.value) || !taken.has(option.value) && (!parent || parent.has(option.value)))
}
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())

function selectOrganizations(category) {
  const selected = new Set(unitIDs(category.salesUnits,category.companies))
  for (const override of category.overrides) {
    override.companyIds = (override.companyIds || []).filter(id=>category.companies.includes(id) || selected.has(sourceOrganizations.value.get(id)))
    override.salesUnits = (override.salesUnits || []).filter(id=>selected.has(id))
  }
  category.overrides = category.overrides.filter(row=>row.companyIds.length || row.salesUnits.length)
}
function salesCategoryOptions(category) {
  return props.salesCategories.filter(option=>!categories.value.some(other=>
    other!==category && other.scope==='category' && other.salesCategoryIds.includes(option.value)))
}
const standardMethod = mode => ({mode})
async function addStandard(scope) {
  const index = categories.value.length
  categories.value.push({scope,...standardMethod(activeMethod.value || defaultMethods.value[scope] || amounts.value[0]?.value),companies:[],salesCategoryIds:[],overrides:[]})
  const rowIndex = tableRows.value.findIndex(entry=>entry.index===index && entry.standard)
  page.value = Math.floor(rowIndex/pageSize)+1
  await nextTick()
  document.getElementById(field((scope==='category' ? 'sales-category-' : 'companies-')+index))?.focus()
}
async function addCategoryOverride(category,i) {
  category.overrides.push({companyIds:[],...standardMethod(category.mode)})
  scope.value = 'companies'
  const rowIndex = companyRows.value.findIndex(row=>row.override===category.overrides.at(-1))
  page.value = Math.floor(rowIndex/pageSize)+1
  await nextTick()
  document.getElementById(field('override-'+i+'-'+(category.overrides.length-1)))?.focus()
}
function modeChanged(item, mode) {
  if (item.mode!==mode) Object.assign(item,standardMethod(mode),{value:undefined})
}
function setScopeMode(mode) {
  sales.value.scopeMode = mode
  if (mode!=='team') sales.value.general ??= {mode:amounts.value[0]?.value}
  scope.value = mode==='team' ? (organizationCount.value ? 'companies' : 'category') : 'general'
}
function selectMethod(mode) {
  defaultMethods.value[scope.value] = mode
  for (const row of activeStandards.value) modeChanged(row,mode)
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
  <RuleArrangementFields :name-id="field('label')" v-model:name="component.companySales.label"
    :order-type-id="'rule-member-'+index" :order-types="memberChoices" :order-type="component.classMember"
    @update:order-type="component.classMember=$event; emit('member-change')">
    <a-form-item label="收款对象" :html-for="field('payee')" required>
      <a-select :id="field('payee')" v-model:value="payee.kind" :options="payeeKinds"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='order_person'" label="从订单取收款人" :html-for="field('payee-role')" required>
      <a-select :id="field('payee-role')" v-model:value="payee.role" :options="payeeRoles" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='person'" label="指定收款人" :html-for="field('payee-person')" required>
      <a-select :id="field('payee-person')" v-model:value="payee.personId" :options="people" show-search :filter-option="byName"/>
    </a-form-item>
    <a-form-item v-if="payee.kind==='company'" label="收款公司" :html-for="field('payee-company')" required>
      <a-select :id="field('payee-company')" v-model:value="payee.companyId" :options="recipients" show-search :filter-option="byName"/>
    </a-form-item>
    <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
      :pools="pools"/>
    <template #calculation>
      <a-form-item label="计提方式" :html-for="field('method')">
        <a-select :id="field('method')" :value="activeMethod" :options="amounts"
          :placeholder="activeStandards.length ? '按各标准设置' : '选择计提方式'" @change="selectMethod"/>
      </a-form-item>
    </template>
    <template #scope>
      <RuleOrderScope :id="field('scope')" :model-value="scopeMode" :can-own="payee.kind==='person'"
        @update:model-value="setScopeMode"/>
    </template>
    <template #standards>
      <RuleCommissionStandards v-model="scope" :groups="scopeOptions" :priority="priority">
        <template v-if="scope!=='general'" #tools>
          <a-button :disabled="scope==='category' && !salesCategories.length" size="small" @click="addStandard(scope)">
            {{ standardAction }}
          </a-button>
        </template>
        <RuleStandardValue v-if="scope==='general'" :id="field('general')" v-model="sales.general.value"
          :method="sales.general.mode" :precision="sales.general.mode==='rate' ? 4 : 2"/>
        <section v-else :aria-label="practiceCompanyMatchLabels[scope]">
          <RuleEditableTable v-if="tableRows.length" :key="scope" :label="practiceCompanyMatchLabels[scope]"
            :columns="tableColumns" :rows="tableRows" :page-size="pageSize" v-model:page="page" wide-first-column>
            <template #cells="{row:entry}">
              <td>
                <RuleScopeSelect v-if="scope==='category'" :id="field('sales-category-'+entry.index)" label="适用销售类别"
                  v-model="entry.term.salesCategoryIds" :options="salesCategoryOptions(entry.category)" placeholder="选择销售类别" required/>
                <RuleScopeSelect v-else-if="entry.override" :id="field('override-'+entry.index+'-'+entry.position)" label="特例销售组织"
                  v-model="entry.term.salesUnits" v-model:legacy-values="entry.term.companyIds"
                  :options="organizationOptions(entry.category,entry.override)" :legacy-options="companies" placeholder="选择公司、部门或小组" required/>
                <RuleScopeSelect v-else :id="field('companies-'+entry.index)" label="适用销售组织"
                  v-model="entry.term.salesUnits" v-model:legacy-values="entry.term.companies" :options="organizationOptions(entry.category)"
                  :legacy-options="companies" placeholder="选择公司、部门或小组" required @change="selectOrganizations(entry.category)"/>
                <small v-if="entry.override && entry.category.salesCategoryIds.length" class="order-version">
                  来源类别 · {{ entry.category.salesCategoryIds.map(id=>salesCategories.find(option=>option.value===id)?.label || id).join('、') }}
                </small>
                <a-button v-if="scope==='category' && entry.term.salesCategoryIds.length && organizationOptions(entry.category).length"
                  class="company-category-override" type="link" size="small" @click="addCategoryOverride(entry.category,entry.index)">为此类别添加组织特例</a-button>
              </td>
              <td v-if="amounts.length>1">
                <a-form-item label="计提方式" :html-for="termField(entry,'method')">
                  <a-select :id="termField(entry,'method')" :value="entry.term.mode" :options="amounts" size="small"
                    @change="modeChanged(entry.term,$event)"/>
                </a-form-item>
              </td>
              <td>
                <a-form-item :label="entry.term.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="termField(entry,'value')" required>
                  <a-input-number :id="termField(entry,'value')" v-model:value="entry.term.value" size="small" :min="0"
                    :addon-after="amounts.length>1 ? (entry.term.mode==='rate' ? '%' : '元') : undefined"
                    :max="entry.term.mode==='rate'?100:undefined" :precision="entry.term.mode==='rate'?4:2"/>
                </a-form-item>
              </td>
              <td v-if="legacyRecommendations">
                <a-form-item label="推荐比例（%）" :html-for="termField(entry,'recommendation')">
                  <a-input-number :id="termField(entry,'recommendation')" v-model:value="entry.term.recommendationRate" size="small"
                    :min="0" :max="100" :precision="4" :placeholder="entry.override ? '沿用 '+(entry.category.recommendationRate ?? 0)+'%' : undefined"/>
                </a-form-item>
              </td>
            </template>
            <template #actions="{row:entry}">
              <a-button v-if="entry.override" type="link" danger size="small" @click="entry.category.overrides.splice(entry.position,1)">移除</a-button>
              <a-button v-else-if="categories.length>1 || component.companySales.defaultRate != null"
                type="link" danger size="small" @click="categories.splice(entry.index,1)">移除</a-button>
            </template>
          </RuleEditableTable>
          <p v-else class="finance-rate-empty">未配置</p>
        </section>
        <div v-if="scopes.hasDefault" class="saved-standard">
          <span>{{ practiceCompanyMatchLabels.default }} · {{ sales.defaultRate }}%</span>
          <a-button type="link" danger size="small" @click="sales.defaultRate=undefined">移除</a-button>
        </div>
      </RuleCommissionStandards>
    </template>
  </RuleArrangementFields>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse v-if="component.companySales.internalCompanies?.length" ghost class="company-advanced">
    <a-collapse-panel key="advanced" header="订单归属设置">
      <div class="rule-basics">
        <a-form-item label="已配置的个人提成组织" :html-for="field('internal')">
          <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
            mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" size="small"/>
        </a-form-item>
      </div>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.saved-standard {display:flex;align-items:center;gap:8px;margin-top:6px}
.company-category-override {padding-inline:0}
.company-advanced :deep(.ant-collapse-header) {padding:4px 0}
.company-advanced :deep(.ant-collapse-content-box) {padding:4px 0}
</style>

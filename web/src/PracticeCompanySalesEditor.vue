<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleScopeSelect from './RuleScopeSelect.vue'
import RuleOrderScope from './RuleOrderScope.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import { practiceCompanyMatchLabels, practiceCompanyRatePriority,
  practiceCompanyScopes, practiceRecipientIsPerson } from './rule-component-form'

const props = defineProps({component:Object,index:Number,companies:Array,organizations:Array,salesCategories:Array,recipients:Array,amounts:Array,
  sourceRoles:Array,payeeRoles:Array,people:Array,units:Array,pools:Array,createPool:Function,memberChoices:Array,duties:Array})
const emit = defineEmits(['member-change'])
const sales = computed(() => props.component.companySales)
const categories = computed(() => sales.value.categories)
const overrides = computed(() => categories.value.flatMap((category,index)=>
  category.overrides.map((override,position)=>({category,index,override,position}))))
const standardRowsByScope = computed(() => ({
  companies:categories.value.map((standard,index)=>({standard,index})).filter(({standard})=>standard.scope==='companies'),
  category:categories.value.map((standard,index)=>({standard,index})).filter(({standard})=>standard.scope==='category'),
}))
const companyPageSize = 8
const companyPage = ref(1)
const companyRows = computed(() => [...standardRowsByScope.value.companies,...overrides.value])
const companyPageCount = computed(() => Math.max(1,Math.ceil(companyRows.value.length/companyPageSize)))
const visibleCompanyRows = computed(() => {
  const start = (companyPage.value-1)*companyPageSize
  return companyRows.value.slice(start,start+companyPageSize)
})
const visibleStandards = computed(() => ({
  category:standardRowsByScope.value.category,
  companies:visibleCompanyRows.value.filter(row=>row.standard),
}))
const visibleOverrides = computed(() => visibleCompanyRows.value.filter(row=>row.override))
watch(companyPageCount, count=>{ companyPage.value=Math.min(companyPage.value,count) })
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
  if (scope==='companies') {
    companyPage.value = Math.ceil(standardRowsByScope.value.companies.length/companyPageSize)
  }
  await nextTick()
  document.getElementById(field((scope==='category' ? 'sales-category-' : 'companies-')+index))?.focus()
}
async function addCategoryOverride(category,i) {
  category.overrides.push({companyIds:[],...standardMethod(category.mode)})
  scope.value = 'companies'
  const rowIndex = companyRows.value.findIndex(row=>row.override===category.overrides.at(-1))
  companyPage.value = Math.floor(rowIndex/companyPageSize)+1
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
    :duty-id="practiceRecipientIsPerson(payee.kind) ? field('duty') : undefined" :duties="duties" v-model:duty="component.recipientDuty"
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
  </RuleArrangementFields>
  <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
    :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
  <RuleCommissionStandards v-model="scope" :groups="scopeOptions" :priority="priority">
    <template v-if="scope!=='general'" #tools>
      <div v-if="scope==='companies' && companyPageCount>1" class="company-standard-pages" role="group" aria-label="组织标准分页">
        <a-button size="small" type="text" :disabled="companyPage===1" @click="companyPage--">上一页</a-button>
        <small role="status">{{ companyPage }} / {{ companyPageCount }}</small>
        <a-button size="small" type="text" :disabled="companyPage===companyPageCount" @click="companyPage++">下一页</a-button>
      </div>
      <a-button :disabled="scope==='category' && !salesCategories.length" size="small" @click="addStandard(scope)">
        {{ standardAction }}
      </a-button>
    </template>
    <div v-if="scope==='general'" class="practice-company-criterion saved-standard">
      <a-form-item :label="sales.general.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="field('general')" required>
        <a-input-number :id="field('general')" v-model:value="sales.general.value" :min="0"
          :max="sales.general.mode==='rate' ? 100 : undefined" :precision="sales.general.mode==='rate' ? 4 : 2"/>
      </a-form-item>
    </div>
    <section v-else :key="scope" :aria-label="practiceCompanyMatchLabels[scope]" class="practice-company-criterion">
      <div v-if="standardRowsByScope[scope].length || (scope==='companies' && overrides.length)"
        class="company-standard-table" :class="{'has-method':amounts.length>1,'has-recommendation':legacyRecommendations}">
        <div class="company-standard-fields company-standard-column-labels" aria-hidden="true">
          <span>{{ scope==='category' ? '适用销售类别' : '适用销售组织' }}</span>
          <span v-if="amounts.length>1">计提方式</span>
          <span>{{ amounts.length>1 ? '计提标准' : '比例（%）' }}</span>
          <span v-if="legacyRecommendations">推荐比例（%）</span>
        </div>
        <div v-for="({standard:category,index:i}) in visibleStandards[scope]" :key="i" class="company-category company-standard-fields">
          <RuleScopeSelect v-if="category.scope==='category'" class="company-scope-field" label="销售类别" :id="field('sales-category-'+i)"
            v-model="category.salesCategoryIds" :options="salesCategoryOptions(category)" placeholder="选择销售类别" required/>
          <RuleScopeSelect v-else class="company-scope-field" label="适用销售组织" :id="field('companies-'+i)"
            v-model="category.salesUnits" v-model:legacy-values="category.companies" :options="organizationOptions(category)"
            :legacy-options="companies" placeholder="选择公司、部门或小组" required @change="selectOrganizations(category)"/>
          <a-form-item v-if="amounts.length>1" label="计提方式" :html-for="field('method-'+i)">
            <a-select :id="field('method-'+i)" :value="category.mode" :options="amounts" size="small" @change="modeChanged(category,$event)"/>
          </a-form-item>
          <a-form-item :label="category.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="field('value-'+i)" required>
            <a-input-number :id="field('value-'+i)" v-model:value="category.value" size="small" :min="0"
              :addon-after="amounts.length>1 ? (category.mode==='rate' ? '%' : '元') : undefined"
              :max="category.mode==='rate'?100:undefined" :precision="category.mode==='rate'?4:2"/>
          </a-form-item>
          <a-form-item v-if="legacyRecommendations" label="推荐比例（%）" :html-for="field('recommendation-'+i)">
            <a-input-number :id="field('recommendation-'+i)" v-model:value="category.recommendationRate" size="small" :min="0" :max="100" :precision="4"/>
          </a-form-item>
          <a-button v-if="categories.length>1 || component.companySales.defaultRate != null"
            size="small" type="link" danger class="company-standard-remove" @click="categories.splice(i,1)">移除</a-button>
          <a-button v-if="category.scope==='category' && category.salesCategoryIds.length && organizationOptions(category).length"
            class="company-category-override" type="link" size="small" @click="addCategoryOverride(category,i)">为此类别添加组织特例</a-button>
        </div>
        <template v-if="scope==='companies'">
          <div class="company-category company-standard-fields" v-for="({category,index:i,override,position:j}) in visibleOverrides" :key="i+'-'+j">
            <div class="company-scope-field">
              <RuleScopeSelect label="特例销售组织" :id="field('override-'+i+'-'+j)" v-model="override.salesUnits"
                v-model:legacy-values="override.companyIds" :options="organizationOptions(category,override)"
                :legacy-options="companies" placeholder="选择公司、部门或小组" required/>
              <small v-if="category.salesCategoryIds.length" class="order-version">
                来源类别 · {{ category.salesCategoryIds.map(id=>salesCategories.find(option=>option.value===id)?.label || id).join('、') }}
              </small>
            </div>
            <a-form-item v-if="amounts.length>1" label="计提方式" :html-for="field('override-method-'+i+'-'+j)">
              <a-select :id="field('override-method-'+i+'-'+j)" :value="override.mode" :options="amounts" size="small" @change="modeChanged(override,$event)"/>
            </a-form-item>
            <a-form-item :label="override.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="field('override-value-'+i+'-'+j)" required>
              <a-input-number :id="field('override-value-'+i+'-'+j)" v-model:value="override.value" size="small" :min="0"
                :addon-after="amounts.length>1 ? (override.mode==='rate' ? '%' : '元') : undefined"
                :max="override.mode==='rate'?100:undefined" :precision="override.mode==='rate'?4:2"/>
            </a-form-item>
            <a-form-item v-if="legacyRecommendations" label="推荐比例（%）" :html-for="field('override-recommendation-'+i+'-'+j)">
              <a-input-number :id="field('override-recommendation-'+i+'-'+j)" v-model:value="override.recommendationRate"
                size="small" :min="0" :max="100" :precision="4" :placeholder="'沿用 '+(category.recommendationRate ?? 0)+'%'"/>
            </a-form-item>
            <a-button type="link" danger size="small" class="company-standard-remove" @click="category.overrides.splice(j,1)">移除</a-button>
          </div>
        </template>
      </div>
      <p v-else class="finance-rate-empty">未配置</p>
    </section>
    <div v-if="scopes.hasDefault" class="saved-standard">
      <span>{{ practiceCompanyMatchLabels.default }} · {{ sales.defaultRate }}%</span>
      <a-button type="link" danger size="small" @click="sales.defaultRate=undefined">移除</a-button>
    </div>
  </RuleCommissionStandards>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse ghost class="company-advanced">
    <a-collapse-panel key="advanced" header="订单归属设置">
      <div class="rule-basics">
        <a-form-item label="订单上的销售角色" :html-for="field('role')" required>
          <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles" size="small"/>
        </a-form-item>
        <a-form-item v-if="component.companySales.internalCompanies?.length" label="已配置的个人提成组织" :html-for="field('internal')">
          <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
            mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" size="small"/>
        </a-form-item>
      </div>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.saved-standard {display:flex;align-items:center;gap:8px;margin-top:6px}
.company-standard-pages {display:flex;align-items:center;gap:4px;font-variant-numeric:tabular-nums}
.company-standard-table {container-type:inline-size}
.company-standard-table.has-method {--company-method-column:minmax(0,1.25fr)}
.company-standard-table.has-recommendation {--company-recommendation-column:minmax(0,1fr)}
.company-category {padding:4px 8px 6px;background:#f6f8fb;border-radius:4px;margin-bottom:4px}
.company-standard-fields {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px}
.company-standard-column-labels {display:none;color:var(--muted);font-size:13px;padding:3px 8px;background:#f6f8fb;border-radius:4px 4px 0 0}
.company-standard-fields :deep(.ant-form-item) {flex:1 1 130px;min-width:0;margin:0}
.company-standard-fields :deep(.company-scope-field) {flex:3 1 220px}
.company-standard-fields :deep(.ant-form-item-label) {padding-bottom:2px}
.company-standard-remove {margin:0 0 0 auto}
.company-category-override {justify-self:start;grid-column:1 / -1;padding-inline:0}
.company-advanced :deep(.ant-collapse-header) {padding:4px 0}
.company-advanced :deep(.ant-collapse-content-box) {padding:4px 0}
.company-standard-fields :deep(.ant-input-number),.company-standard-fields :deep(.ant-input-number-group-wrapper) {width:100%}
@container (min-width:760px) {
  .company-standard-fields {
    display:grid;align-items:center;
    grid-template-columns:minmax(0,3fr) var(--company-method-column,) minmax(0,1.25fr) var(--company-recommendation-column,) 48px;
  }
  .company-standard-column-labels {display:grid}
  .company-category {padding-block:3px;margin-bottom:0;background:white;border-bottom:1px solid var(--line);border-radius:0}
  .company-category :deep(.ant-form-item-label) {position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
}
</style>

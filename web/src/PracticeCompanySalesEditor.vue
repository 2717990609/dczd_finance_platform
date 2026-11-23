<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import PracticePoolFields from './PracticePoolFields.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import { practiceCompanyMatchText, practiceCompanyRatePriority, practiceCompanyScopes, practiceRecipientIsPerson } from './rule-component-form'

const props = defineProps({component:Object,index:Number,companies:Array,salesCategories:Array,recipients:Array,amounts:Array,
  sourceRoles:Array,payeeRoles:Array,people:Array,units:Array,pools:Array,createPool:Function,memberChoices:Array,duties:Array})
const emit = defineEmits(['member-change'])
const sales = computed(() => props.component.companySales)
const categories = computed(() => sales.value.categories)
const overrides = computed(() => categories.value.flatMap((category,index)=>
  category.overrides.map((override,position)=>({category,index,override,position}))))
const standardGroups = [
  {scope:'category',label:'按销售类别计提',action:'添加销售类别'},
  {scope:'default',label:'外部公司默认比例'},
  {scope:'companies',label:'按指定公司计提',action:'添加指定公司'},
]
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
const priority = computed(() => practiceCompanyRatePriority(scopes.value))
const calculationText = computed(() => {
  const modes = new Set([...categories.value,...overrides.value.map(({override})=>override)].map(row=>row.mode))
  if (scopes.value.hasDefault) modes.add('rate')
  return [modes.has('rate') && '比例',modes.has('fixed') && '固定金额'].filter(Boolean).join(' / ') || '在计提标准中设置'
})
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
const field = name => 'practice-company-'+props.index+'-'+name
const byName = (text, option) => (option?.label || '').toLowerCase().includes(text.trim().toLowerCase())

function availableCompanies(category) {
  return props.companies.filter(option => {
    const owners = companyAssignments.value.get(option.value)
    return !internalCompanies.value.has(option.value) && (!owners || owners.size===1 && owners.has(category))
  })
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
async function addStandard(scope) {
  const index = categories.value.length
  categories.value.push({scope,mode:amounts.value[0]?.value,companies:[],salesCategoryIds:[],overrides:[]})
  if (scope==='companies') {
    companyPage.value = Math.ceil(standardRowsByScope.value.companies.length/companyPageSize)
  }
  await nextTick()
  document.getElementById(field((scope==='category' ? 'sales-category-' : 'companies-')+index))?.focus()
}
async function addCategoryOverride(category,i) {
  category.overrides.push({companyIds:[],mode:category.mode})
  const rowIndex = companyRows.value.findIndex(row=>row.override===category.overrides.at(-1))
  companyPage.value = Math.floor(rowIndex/companyPageSize)+1
  await nextTick()
  document.getElementById(field('override-'+i+'-'+(category.overrides.length-1)))?.focus()
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
  <RuleArrangementFields :name-id="field('label')" v-model:name="component.companySales.label"
    :calculation-text="calculationText" :scope-text="practiceCompanyMatchText(scopes)"
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
  </RuleArrangementFields>
  <PracticePoolFields v-if="payee.kind==='pool'" :target="payee.pool" :prefix="field('payee')"
    :pools="pools" :people="people" :units="units" :create-pool="createPool"/>
  <div class="practice-company-criteria">
    <small v-if="priority" class="finance-rate-priority">匹配顺序 {{ priority }}</small>
    <section v-for="group in standardGroups" :key="group.scope" :aria-label="group.label"
      class="practice-company-criterion">
      <header>
        <strong>{{ group.label }}</strong>
        <template v-if="group.scope!=='default'">
          <small v-if="group.scope==='category' && scopes.categories.length">{{ scopes.categories.length }} 类</small>
          <small v-else-if="group.scope==='companies' && scopes.companies.length">{{ scopes.companies.length }} 家</small>
          <small v-else>未配置</small>
          <div v-if="group.scope==='companies' && companyPageCount>1" class="company-standard-pages" role="group" aria-label="指定公司标准分页">
            <a-button size="small" type="text" :disabled="companyPage===1" @click="companyPage--">上一页</a-button>
            <small role="status">{{ companyPage }} / {{ companyPageCount }}</small>
            <a-button size="small" type="text" :disabled="companyPage===companyPageCount" @click="companyPage++">下一页</a-button>
          </div>
          <a-button :disabled="group.scope==='category' && !salesCategories.length" size="small"
            @click="addStandard(group.scope)">{{ group.action }}</a-button>
        </template>
        <a-form-item v-else class="company-default-rate" :html-for="field('default-rate')">
          <a-input-number :id="field('default-rate')" v-model:value="component.companySales.defaultRate"
            aria-label="外部公司默认比例（%）" addon-after="%" size="small" :min="0" :max="100" :precision="4" placeholder="未配置"/>
        </a-form-item>
      </header>
      <div v-if="group.scope!=='default' && (standardRowsByScope[group.scope].length || (group.scope==='companies' && overrides.length))"
        class="company-standard-table" :class="{'has-method':amounts.length>1,'has-recommendation':legacyRecommendations}">
        <div class="company-standard-fields company-standard-column-labels" aria-hidden="true">
          <span>{{ group.scope==='category' ? '销售类别' : '指定公司' }}</span>
          <span v-if="amounts.length>1">计提方式</span>
          <span>{{ amounts.length>1 ? '计提标准' : '比例（%）' }}</span>
          <span v-if="legacyRecommendations">推荐比例（%）</span>
        </div>
        <div v-for="({standard:category,index:i}) in visibleStandards[group.scope]" :key="i" class="company-category company-standard-fields">
          <a-form-item v-if="category.scope==='category'" class="company-scope-field" label="销售类别" :html-for="field('sales-category-'+i)" required>
            <a-select :id="field('sales-category-'+i)" v-model:value="category.salesCategoryIds" :options="salesCategoryOptions(category)" size="small"
              mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择销售类别"/>
          </a-form-item>
          <a-form-item v-else class="company-scope-field" label="适用公司" :html-for="field('companies-'+i)" required>
            <a-select :id="field('companies-'+i)" :value="category.companies" :options="availableCompanies(category)" size="small"
              mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" placeholder="选择公司" @change="selectCompanies(category,$event)"/>
          </a-form-item>
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
          <a-button v-if="category.scope==='category' && category.salesCategoryIds.length && overrideOptions(category).length"
            class="company-category-override" type="link" size="small" @click="addCategoryOverride(category,i)">为此类别添加公司特例</a-button>
        </div>
        <template v-if="group.scope==='companies'">
          <div class="company-category company-standard-fields" v-for="({category,index:i,override,position:j}) in visibleOverrides" :key="i+'-'+j">
            <a-form-item class="company-scope-field" label="特例公司" :html-for="field('override-'+i+'-'+j)" required>
              <a-select :id="field('override-'+i+'-'+j)" v-model:value="override.companyIds"
                :options="overrideOptions(category,override)" size="small" mode="multiple" :max-tag-count="3" :filter-option="byName"/>
              <small v-if="category.salesCategoryIds.length" class="order-version">
                来源类别 · {{ category.salesCategoryIds.map(id=>salesCategories.find(option=>option.value===id)?.label || id).join('、') }}
              </small>
            </a-form-item>
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
    </section>
  </div>
  <a-form-item v-if="hasRecommendations" label="可收推荐分润的公司" :html-for="field('referral-recipients')" required>
    <a-select :id="field('referral-recipients')" v-model:value="component.companySales.recommendationRecipients"
      :options="recipients" mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName"/>
  </a-form-item>
  <a-collapse ghost class="company-advanced">
    <a-collapse-panel key="advanced" header="订单归属设置">
      <div class="rule-basics">
        <a-form-item label="订单销售角色" :html-for="field('role')" required>
          <a-select :id="field('role')" v-model:value="component.sourceRole" :options="sourceRoles" size="small"/>
        </a-form-item>
        <a-form-item v-if="component.companySales.internalCompanies" label="按个人销售提成的公司" :html-for="field('internal')">
          <a-select :id="field('internal')" v-model:value="component.companySales.internalCompanies" :options="internalOptions"
            mode="multiple" :max-tag-count="3" allow-clear :filter-option="byName" size="small"/>
        </a-form-item>
      </div>
    </a-collapse-panel>
  </a-collapse>
</template>

<style scoped>
.practice-company-criterion .company-default-rate {margin:0 0 0 auto;width:125px}
.company-standard-pages {display:flex;align-items:center;gap:4px;font-variant-numeric:tabular-nums}
.company-standard-table {container-type:inline-size}
.company-standard-table.has-method {--company-method-column:minmax(0,1.25fr)}
.company-standard-table.has-recommendation {--company-recommendation-column:minmax(0,1fr)}
.company-category {padding:4px 8px 6px;background:#f6f8fb;border-radius:4px;margin-bottom:4px}
.company-standard-fields {display:flex;flex-wrap:wrap;align-items:end;gap:4px 10px}
.company-standard-column-labels {display:none;color:var(--muted);font-size:13px;padding:3px 8px;background:#f6f8fb;border-radius:4px 4px 0 0}
.company-standard-fields :deep(.ant-form-item) {flex:1 1 130px;min-width:0;margin:0}
.company-standard-fields :deep(.ant-form-item.company-scope-field) {flex:3 1 220px}
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

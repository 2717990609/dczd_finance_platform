<script setup>
import { computed } from 'vue'
import RuleOrderScope from './RuleOrderScope.vue'

const props = defineProps({row:Object,index:Number,companies:Array,categories:Array,units:Array,people:Array})
const flexibleScope = computed(() => !props.row.retrainSplits?.length)
const organizationOptions = computed(() => props.units.filter(unit=>unit.kind!=='COMPANY' || props.row.saleScope?.salesUnits?.includes(unit.value)))
const field = name => 'practice-personal-'+props.index+'-'+name
const mode = computed(() => {
  const row = props.row
  if (row.saleScope?.ownSales && row.salesRecipientKind==='person') return 'own'
  return row.restrictSaleScope || row.saleScope?.kind || row.salesCompanies?.length || row.saleScope?.salesUnits?.length || row.saleScope?.salesCategoryIds?.length ? 'team' : 'all'
})
const matchKind = computed(() => {
  const {row} = props
  if (row.saleScope?.kind) return row.saleScope.kind
  const configured = [
    row.salesCompanies?.length && 'company',
    row.saleScope?.salesCategoryIds?.length && 'category',
    row.saleScope?.salesUnits?.length && 'organization',
  ].filter(Boolean)
  return configured.length>1 ? 'legacy' : configured[0] || 'company'
})
const exceptionGroups = [
  {key:'personExceptions',id:'personId',label:'个人特例',optionLabel:'销售人员',options:'people'},
  {key:'companyExceptions',id:'companyId',label:'公司特例',optionLabel:'销售公司',options:'companies'},
]
const exceptionMethods = [{value:'rate',label:'比例'},{value:'fixed',label:'固定金额'}]

function changeScope(next) {
  const row = props.row
  row.restrictSaleScope = next==='team'
  row.saleScope = next==='own' ? {ownSales:true} : next==='team' && flexibleScope.value ? {kind:'company'} : undefined
  if (next!=='team') row.salesCompanies = []
}

function changeMatchKind(kind) {
  props.row.salesCompanies = []
  props.row.saleScope = {kind}
}

function selectCompanies(companies) {
  props.row.salesCompanies = companies
  if (mode.value==='team' && flexibleScope.value) props.row.saleScope = {kind:'company'}
}

function updateFilter(key, value) {
  props.row.saleScope = {...props.row.saleScope,kind:matchKind.value,[key]:value}
}

function addException(key) {
  const scope = props.row.saleScope
  scope.kind = 'category'
  scope[key] ??= []
  scope[key].push({mode:props.row.mode})
}
function setExceptionMethod(exception, method) {
  exception.mode = method
  exception.amount = undefined
}
function exceptionOptions(group, current) {
  const selected = new Set(props.row.saleScope[group.key].filter(row=>row!==current).map(row=>row[group.id]))
  return props[group.options].filter(option=>!selected.has(option.value))
}
</script>

<template>
  <RuleOrderScope :id="field('scope')" :model-value="mode" :can-own="flexibleScope && row.salesRecipientKind==='person'"
    team-label="指定销售范围" all-label="全班" @update:model-value="changeScope"/>
  <div v-if="mode==='team' || mode==='own' && row.salesCompanies?.length" class="rule-scope-filters">
    <a-form-item v-if="mode==='team' && flexibleScope" class="practice-match-kind" label="按什么筛选订单">
      <a-radio-group :value="matchKind" role="radiogroup" aria-label="按什么筛选订单" @update:value="changeMatchKind">
        <a-radio value="company">销售公司</a-radio>
        <a-radio value="category">销售类别</a-radio>
        <a-radio value="organization">销售组织</a-radio>
      </a-radio-group>
    </a-form-item>
    <a-form-item v-if="matchKind==='legacy'" class="practice-match-kind" label="已保存的组合范围">
      <span>保留当前配置。重新选择后，按一种范围筛选订单。</span>
    </a-form-item>
    <a-form-item v-if="mode==='own' || matchKind==='company' || !flexibleScope" label="适用销售公司" :html-for="field('companies')">
      <a-select :id="field('companies')" :value="row.salesCompanies" :options="companies" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择公司" @change="selectCompanies"/>
    </a-form-item>
    <a-form-item v-if="matchKind==='category'" label="适用销售类别" :html-for="field('categories')">
      <a-select :id="field('categories')" :value="row.saleScope?.salesCategoryIds" :options="categories" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择类别" @change="updateFilter('salesCategoryIds',$event)"/>
    </a-form-item>
    <a-form-item v-if="matchKind==='organization'" label="适用销售组织" :html-for="field('units')">
      <a-select :id="field('units')" :value="row.saleScope?.salesUnits" :options="organizationOptions" mode="multiple"
        :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择部门或小组" @change="updateFilter('salesUnits',$event)"/>
    </a-form-item>
    <section v-if="matchKind==='category' && row.saleScope?.salesCategoryIds?.length && ['rate','fixed'].includes(row.mode)"
      class="practice-scope-exceptions" :aria-labelledby="field('exceptions-title')">
      <div class="practice-exception-heading">
        <strong :id="field('exceptions-title')">类别内特例</strong>
        <a-space wrap :size="4">
          <a-button v-for="group in exceptionGroups" :key="group.key" size="small" @click="addException(group.key)">
            添加{{ group.label }}
          </a-button>
        </a-space>
      </div>
      <small v-if="row.saleScope.companyExceptions?.length || row.saleScope.personExceptions?.length" class="finance-rate-priority">
        个人特例 → 公司特例 → 类别默认。填 0 不计提。
      </small>
      <template v-for="group in exceptionGroups" :key="group.key">
        <div v-for="(exception,i) in row.saleScope[group.key]" :key="i" class="practice-exception-row" :aria-label="group.label">
          <a-form-item :label="group.optionLabel" :html-for="field(group.key+'-'+i)" required>
            <a-select :id="field(group.key+'-'+i)" v-model:value="exception[group.id]" :options="exceptionOptions(group,exception)"
              show-search option-filter-prop="label" :placeholder="'选择'+group.optionLabel"/>
          </a-form-item>
          <a-form-item label="计提方式" :html-for="field(group.key+'-method-'+i)">
            <a-select :id="field(group.key+'-method-'+i)" :value="exception.mode" :options="exceptionMethods"
              @change="setExceptionMethod(exception,$event)"/>
          </a-form-item>
          <a-form-item :label="exception.mode==='rate' ? '比例（%）' : '金额（元）'" :html-for="field(group.key+'-amount-'+i)" required>
            <a-input-number :id="field(group.key+'-amount-'+i)" v-model:value="exception.amount" :min="0"
              :max="exception.mode==='rate' ? 100 : 1000000000" :precision="2"/>
          </a-form-item>
          <a-button type="link" danger size="small" @click="row.saleScope[group.key].splice(i,1)">移除</a-button>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.practice-match-kind,.practice-scope-exceptions { grid-column:1 / -1 }
.practice-exception-heading { display:flex; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:4px 10px }
.practice-exception-row { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,152px),1fr)); align-items:end; gap:4px 10px }
.practice-exception-row :deep(.ant-form-item) { margin:4px 0; min-width:0 }
.practice-exception-row :deep(.ant-btn) { justify-self:start; margin-bottom:4px }
</style>

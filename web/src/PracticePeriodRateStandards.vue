<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import RuleScopeSelect from './RuleScopeSelect.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import RuleEditableTable from './RuleEditableTable.vue'
import { commissionRatePriority, practiceStandardRateGroups } from './rule-component-form'

const props = defineProps({prefix:String,uniformId:String,companies:Array,organizations:Array,categories:Array,scopeGroup:String})
const emit = defineEmits(['start-table'])
const rows = defineModel({type:Array})
const rate = defineModel('rate',{type:Number})
const baseGroup = computed(() => rows.value ? 'general' : props.scopeGroup || 'general')
const groups = computed(() => practiceStandardRateGroups(rows.value || []).filter(group=>group.scope!=='external' || group.items.length))
const tabs = computed(() => ['category','company','general','external'].flatMap(scope=>
  groups.value.filter(group=>group.scope===scope).map(group=>({
    value:scope,count:scope===baseGroup.value && !rows.value ? Number(rate.value!=null) : group.items.length,
  }))))
const active = ref(groups.value.find(group=>group.items.length)?.scope || baseGroup.value)
const page = ref(1)
const pageSize = 8
watch(active,()=>{ page.value=1 })
const selected = computed(() => groups.value.find(group=>group.scope===active.value) || groups.value.find(group=>group.scope==='general'))
const uniform = computed(() => selected.value.scope===baseGroup.value && !rows.value)
watch(baseGroup,(next,previous)=>{
  if (!rows.value && active.value===previous) active.value = next
})
const priority = computed(() => commissionRatePriority(tabs.value.filter(tab=>tab.count).map(tab=>tab.value)))
const newStandard = {
  company:{label:'添加指定组织',field:'salesUnits'},
  category:{label:'添加销售类别',field:'salesCategoryIds'},
  external:{},
  general:{label:'添加通用比例'},
}
const scopeColumn = computed(() => selected.value.scope==='company' ? '适用销售组织' :
  selected.value.scope==='category' ? '适用销售类别' : '')
const columns = computed(() => [...(scopeColumn.value ? [scopeColumn.value] : []),'比例（%）'])
async function add(scope) {
  const field = newStandard[scope].field
  const row = field ? {scope,[field]:[]} : {scope}
  if (!rows.value) {
    emit('start-table',row)
  } else {
    rows.value.push(row)
  }
  await nextTick()
  if (active.value!==scope) return
  const items = selected.value.items
  page.value = Math.ceil(items.length/pageSize)
  await nextTick()
  const index = items.at(-1)?.index
  document.getElementById(props.prefix+'-'+(field ? scope : 'rate')+'-'+index)?.focus()
}
function remove(index) {
  rows.value.splice(index,1)
  if (active.value==='external') active.value = 'general'
}
</script>

<template>
  <RuleCommissionStandards v-model="active" :groups="tabs" :priority="priority">
    <template #tools>
      <a-button v-if="newStandard[selected.scope].label && (newStandard[selected.scope].field || !uniform && !selected.items.length)"
        :disabled="selected.scope==='category' && !categories.length" size="small" @click="add(selected.scope)">{{ newStandard[selected.scope].label }}</a-button>
    </template>
    <template v-if="!uniform && selected.scope===baseGroup" #scope><slot name="scope"/></template>
    <div v-if="uniform" :class="{'practice-scoped-flat-rate':baseGroup!=='general'}">
      <slot name="scope"/>
      <a-form-item class="practice-flat-rate" label="比例（%）" :html-for="uniformId" required>
        <a-input-number :id="uniformId" v-model:value="rate" :min="0" :max="100" :precision="4"/>
      </a-form-item>
    </div>
    <section v-else :aria-label="selected.label">
      <RuleEditableTable v-if="selected.items.length" :label="selected.label" :columns="columns" :rows="selected.items"
        :page-size="pageSize" v-model:page="page" :wide-first-column="Boolean(scopeColumn)">
        <template #cells="{row:{row,index:i}}">
          <td v-if="scopeColumn">
            <RuleScopeSelect v-if="selected.scope==='category'" :id="prefix+'-category-'+i" :label="scopeColumn"
              v-model="row.salesCategoryIds" :options="categories" placeholder="选择适用类别" required/>
            <RuleScopeSelect v-else :id="prefix+'-company-'+i" :label="scopeColumn"
              v-model="row.salesUnits" v-model:legacy-values="row.salesCompanies" :options="organizations"
              :legacy-options="companies" placeholder="选择公司、部门或小组" required/>
          </td>
          <td>
            <a-form-item label="比例（%）" :html-for="prefix+'-rate-'+i" required>
              <a-input-number :id="prefix+'-rate-'+i" v-model:value="row.value" size="small" :min="0" :max="100" :precision="4"/>
            </a-form-item>
          </td>
        </template>
        <template #actions="{row:{index:i}}">
          <a-button type="link" danger size="small" @click="remove(i)">移除</a-button>
        </template>
      </RuleEditableTable>
      <p v-else class="finance-rate-empty">未配置</p>
    </section>
  </RuleCommissionStandards>
</template>

<style scoped>
.practice-scoped-flat-rate {display:grid;grid-template-columns:minmax(0,1fr) minmax(85px,128px);gap:0 8px;align-items:start}
.practice-scoped-flat-rate :deep(.ant-form-item) {min-width:0;margin:0}
.practice-flat-rate {width:min(100%,128px);margin-bottom:0}
</style>

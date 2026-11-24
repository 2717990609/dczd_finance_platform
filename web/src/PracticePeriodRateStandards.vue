<script setup>
import { computed, ref, watch } from 'vue'
import RuleScopeSelect from './RuleScopeSelect.vue'
import RuleCommissionStandards from './RuleCommissionStandards.vue'
import { practiceStandardRateGroups } from './rule-component-form'

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
const selected = computed(() => groups.value.find(group=>group.scope===active.value) || groups.value.find(group=>group.scope==='general'))
const uniform = computed(() => selected.value.scope===baseGroup.value && !rows.value)
watch(baseGroup,(next,previous)=>{
  if (!rows.value && active.value===previous) active.value = next
})
const priority = computed(() => {
  const configured = groups.value.filter(group=>group.items.length)
  return configured.length>1 ? configured.map(group=>group.label).join(' → ') : ''
})
const newStandard = {
  company:{label:'添加指定组织',field:'salesUnits'},
  category:{label:'添加销售类别',field:'salesCategoryIds'},
  external:{},
  general:{label:'添加通用比例'},
}
function add(scope) {
  const field = newStandard[scope].field
  const row = field ? {scope,[field]:[]} : {scope}
  if (!rows.value) {
    emit('start-table',row)
    return
  }
  rows.value.push(row)
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
    <section v-else :aria-label="selected.label" class="practice-company-criterion">
      <p v-if="!selected.items.length" class="finance-rate-empty">未配置</p>
      <div v-for="({row,index:i}) in selected.items" :key="i" class="practice-standard-row" :class="{'fallback-rate':!newStandard[selected.scope].field}">
        <RuleScopeSelect v-if="selected.scope==='category'" :id="prefix+'-category-'+i" label="适用销售类别"
          v-model="row.salesCategoryIds" :options="categories" placeholder="选择适用类别" required/>
        <RuleScopeSelect v-else-if="selected.scope==='company'" :id="prefix+'-company-'+i" label="适用销售组织"
          v-model="row.salesUnits" v-model:legacy-values="row.salesCompanies" :options="organizations"
          :legacy-options="companies" placeholder="选择公司、部门或小组" required/>
        <a-form-item label="比例（%）" :html-for="prefix+'-rate-'+i" required>
          <a-input-number :id="prefix+'-rate-'+i" v-model:value="row.value" :min="0" :max="100" :precision="4"/>
        </a-form-item>
        <a-button type="link" danger size="small" @click="remove(i)">移除</a-button>
      </div>
    </section>
  </RuleCommissionStandards>
</template>

<style scoped>
.practice-scoped-flat-rate {display:grid;grid-template-columns:minmax(0,1fr) minmax(85px,128px);gap:0 8px;align-items:start}
.practice-scoped-flat-rate :deep(.ant-form-item) {min-width:0;margin:0}
.practice-flat-rate {width:min(100%,128px);margin-bottom:0}
.practice-standard-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(85px,128px) auto;align-items:end;gap:8px;margin:4px 0}
.practice-standard-row.fallback-rate {grid-template-columns:minmax(85px,128px) max-content}
.practice-standard-row :deep(.ant-form-item) {margin:0;min-width:0}
.practice-standard-row :deep(.ant-input-number) {width:100%}
</style>

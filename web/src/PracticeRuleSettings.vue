<script setup>
import { computed, onMounted, ref } from 'vue'
import { Popconfirm } from 'ant-design-vue'
import PracticePersonCountFields from './PracticePersonCountFields.vue'
import { practiceCountStartForm } from './rule-component-form'

const props = defineProps({
  productId:Number, canEdit:Boolean, api:Function, formatTime:Function, optionLabel:Function, startModes:Array,
})
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const editError = ref('')
const result = ref({rules:[]})
const search = ref('')
const form = ref()
const directory = ref()
const query = computed(() => '?'+new URLSearchParams({productId:props.productId}))
const arrangements = computed(() => result.value.rules.filter(rule=>rule.hasCount).flatMap(rule=>
  rule.countArrangements.map(part=>({rule,part,id:rule.id+':'+part.member,
    label:rule.countArrangements.length>1 ? part.label || props.optionLabel('classMember',part.member) : ''}))))
const arrangementsByID = computed(() => new Map(arrangements.value.map(row=>[row.id,row])))
const personNames = computed(() => new Map((result.value.directory?.people || []).map(person=>[person.id,person.name])))
const personName = id => personNames.value.get(id) || '人员 '+id
const scope = rule => [
  rule.periodName || (rule.fromOrdinal ? '从第 '+rule.fromOrdinal+' 期起' : '产品默认'),
  rule.activityType ? props.optionLabel('practiceActivityType',String(rule.activityType)) : '',
  rule.effectiveFrom ? props.formatTime(rule.effectiveFrom)+' 起' : '',
].filter(Boolean).join(' · ')
const ruleOptions = computed(() => arrangements.value.map(({id,rule,label})=>({
  value:id,label:[rule.label,label,scope(rule),'v'+rule.version].filter(Boolean).join(' · '),
})))
const currentArrangement = computed(() => arrangementsByID.value.get(form.value?.arrangementId))
const personalStartModes = computed(() => props.startModes.filter(option=>
  !option.scope?.countScopes || option.scope.countScopes.includes('person')))
const people = computed(() => (directory.value?.people || []).map(person=>({value:person.id,label:person.name+' · '+person.id})))
const eligiblePeople = computed(() => people.value.filter(person=>person.value===form.value?.existing?.personId ||
  !currentArrangement.value?.part.personCountStarts?.some(start=>start.personId===person.value)))
const rows = computed(() => {
  const term = search.value.trim().toLocaleLowerCase()
  return arrangements.value.flatMap(arrangement => (arrangement.part.personCountStarts || []).map(item=>{
    const key = item.personId
    return {id:arrangement.id+':'+key,rule:arrangement.rule,arrangement,item,name:personName(key),key}
  })).filter(row=>!term || (row.name+' '+row.key).toLocaleLowerCase().includes(term))
    .sort((a,b)=>a.name.localeCompare(b.name,'zh-CN') || b.rule.version-a.rule.version)
})
const columns = computed(() => [
  {title:'人员',key:'name',width:160},
  {title:'累计起点',key:'start',width:190},
  {title:'适用规则',key:'scope',width:235},
  ...(props.canEdit ? [{title:'操作',key:'action',width:80}] : []),
])

async function load() {
  loading.value = true
  error.value = ''
  try {
    result.value = await props.api('/practice-rule-settings'+query.value)
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
onMounted(load)
function newEntry() {
  return {startMode:personalStartModes.value[0]?.value}
}
function selectRule() {
  form.value.entry = newEntry()
}
async function open(row) {
  editError.value = ''
  notice.value = ''
  const existing = row?.item
  const entry = existing ? {personId:existing.personId,...practiceCountStartForm(existing)} : newEntry()
  const editing = form.value = {arrangementId:row?.arrangement.id || (arrangements.value.length===1 ? arrangements.value[0].id : undefined),existing,entry}
  if (directory.value) return
  try {
    directory.value = await props.api('/practice-rule-recipients'+query.value)
  } catch (err) {
    if (form.value===editing) editError.value = err.message
  }
}
function countPayload(entry) {
  if (!entry.personId) throw new Error('请选择人员')
  if (entry.startMode==='ordinal') {
    if (!Number.isInteger(entry.countFromOrdinal) || entry.countFromOrdinal<1) throw new Error('请填写起始期数')
    return {personId:entry.personId,countFromOrdinal:entry.countFromOrdinal}
  }
  const at = Date.parse(entry.countFromAt+'+08:00')
  if (!Number.isFinite(at)) throw new Error('请填写起始开班时间')
  return {personId:entry.personId,countFromAt:new Date(at).toISOString()}
}
async function save(remove = false) {
  if (saving.value) return
  editError.value = ''
  const {entry,existing} = form.value
  saving.value = true
  try {
    const arrangement = currentArrangement.value
    if (!arrangement) throw new Error('请选择适用规则')
    const body = {productId:props.productId,ruleId:arrangement.rule.id,countMember:arrangement.part.member,
      countStart:remove ? undefined : countPayload(entry),existingPersonId:existing?.personId}
    const {version} = await props.api('/practice-rule-settings',{method:'POST',body:JSON.stringify(body)})
    form.value = undefined
    notice.value = '已保存规则 v'+version
    if (!existing) search.value = ''
    await load()
  } catch (err) {
    editError.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="records" aria-label="人员累计起点">
    <div class="records-head">
      <h2>人员累计起点</h2>
      <div class="practice-settings-actions">
        <a-input-search v-model:value="search" aria-label="按姓名搜索" placeholder="搜索姓名或人员 ID" allow-clear/>
        <a-button v-if="canEdit" type="primary" :disabled="loading || !arrangements.length" @click="open()">新增人员起点</a-button>
      </div>
    </div>
    <a-alert v-if="notice" type="success" :message="notice" show-icon/>
    <a-alert v-if="error" type="error" :message="error" show-icon>
      <template #action><a-button size="small" @click="load">重新读取</a-button></template>
    </a-alert>
    <div class="table-container">
      <a-table :columns="columns" :data-source="rows" :loading="loading" row-key="id" size="middle"
        :pagination="{pageSize:15,hideOnSinglePage:true,showSizeChanger:false}" :scroll="{x:630}">
        <template #bodyCell="{column,record}">
          <template v-if="column.key==='name'">
            <strong>{{ record.name }}</strong><small class="order-version">人员 ID {{ record.key }}</small>
          </template>
          <template v-else-if="column.key==='start'">
            {{ record.item.countFromOrdinal ? '第 '+record.item.countFromOrdinal+' 期' : formatTime(record.item.countFromAt) }}
          </template>
          <template v-else-if="column.key==='scope'">
            {{ record.rule.label }}<template v-if="record.arrangement.label"> · {{ record.arrangement.label }}</template> · v{{ record.rule.version }}
            <small class="order-version">{{ scope(record.rule) }}</small>
          </template>
          <a-button v-else-if="column.key==='action'" type="link" @click="open(record)">编辑</a-button>
        </template>
        <template #emptyText>
          {{ loading ? '正在加载' : error ? '读取失败' : search ? '未找到匹配记录' :
            !arrangements.length ? '暂无使用累计指标的规则' : '暂无人员起点' }}
        </template>
      </a-table>
    </div>
  </section>
  <a-modal v-if="form" :open="true" :title="(form.existing ? '编辑' : '新增')+'人员累计起点'"
    :width="520" ok-text="保存并生效" :confirm-loading="saving" :ok-button-props="{disabled:!directory}"
    :mask-closable="!saving" :closable="!saving" :keyboard="!saving" @ok="save()" @cancel="!saving && (form=undefined)">
    <a-alert v-if="editError" class="feedback" :message="editError" type="error" show-icon/>
    <a-form layout="vertical">
      <a-form-item label="适用规则" html-for="practice-settings-rule" required>
        <a-select id="practice-settings-rule" v-model:value="form.arrangementId" :options="ruleOptions" :disabled="!!form.existing"
          show-search option-filter-prop="label" @change="selectRule"/>
      </a-form-item>
      <template v-if="currentArrangement && directory">
        <a-form-item label="人员" html-for="practice-settings-person" required>
          <a-select id="practice-settings-person" v-model:value="form.entry.personId" :options="eligiblePeople"
            :disabled="!!form.existing" show-search option-filter-prop="label"/>
        </a-form-item>
        <PracticePersonCountFields :start="form.entry" prefix="practice-settings" :start-modes="personalStartModes"/>
        <Popconfirm v-if="form.existing" title="恢复该人员的默认累计起点？" ok-text="恢复默认" @confirm="save(true)">
          <a-button type="link" :disabled="saving">恢复默认起点</a-button>
        </Popconfirm>
      </template>
    </a-form>
  </a-modal>
</template>

<style scoped>
.practice-settings-actions { display:flex; flex-wrap:wrap; gap:8px; align-items:center; }
.practice-settings-actions :deep(.ant-input-search) { width:225px; max-width:100%; }
</style>

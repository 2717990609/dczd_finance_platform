<script setup>
import { computed, onBeforeUnmount, ref } from 'vue'
import { TreeSelect } from 'ant-design-vue'

const props = defineProps({api:Function, mode:String, record:Object, tree:Array, kinds:Array, unitId:String})
const emit = defineEmits(['close','saved'])
const directory = ref([])
const personId = ref()
const target = ref(props.mode==='add' ? props.unitId : undefined)
const kind = ref(props.mode==='transfer' ? props.record.kind : undefined)
const position = ref(props.record?.position || '')
const effective = ref('')
const through = ref('')
const saving = ref(false)
const error = ref('')
const searching = ref(false)
const searchError = ref('')
const titles = {add:'登记任职',transfer:'办理调动',end:'结束任职'}
const needsTarget = computed(() => props.mode!=='end')
const path = computed(() => '/organization-assignments'+(props.mode==='add' ? '' : '/'+props.mode))
let query = 0
let timer

function searchPeople(name = '') {
  const request = ++query
  clearTimeout(timer)
  searching.value = true
  searchError.value = ''
  timer = setTimeout(async () => {
    try {
      const rows = await props.api('/organization-people?'+new URLSearchParams({name:name.trim()}))
      if (request===query) {
        const selected = directory.value.find(option => option.value===personId.value)
        directory.value = rows.map(person=>({label:person.name+' · '+person.id,value:person.id}))
        if (selected && !directory.value.some(option=>option.value===selected.value)) directory.value.unshift(selected)
      }
    } catch (err) {
      if (request===query) searchError.value = err.message
    } finally {
      if (request===query) searching.value = false
    }
  }, 200)
}
onBeforeUnmount(() => { clearTimeout(timer); query++ })

const beijingTime = value => value ? (value.length===16 ? value+':00' : value)+'+08:00' : null
async function save() {
  if (saving.value) return
  error.value = ''
  if (!effective.value || (needsTarget.value && (!target.value || !kind.value)) || props.mode==='add' && !personId.value) {
    error.value = '请填写必填项。'
    return
  }
  const at = beijingTime(effective.value)
  const payload = props.mode==='add'
    ? {personId:personId.value,startsAt:at,throughAt:beijingTime(through.value)}
    : {assignmentId:props.record.id,at,expectedThroughAt:props.record.validTo || null}
  if (needsTarget.value) Object.assign(payload,{unitId:target.value,kind:kind.value,position:position.value.trim()})
  saving.value = true
  try {
    await props.api(path.value,{method:'POST',body:JSON.stringify(payload)})
    emit('saved',titles[props.mode]+'已保存。')
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer :title="titles[mode]" :open="true" :width="'min(440px, 100vw)'" :closable="!saving"
    :keyboard="!saving" :mask-closable="!saving" @close="emit('close')">
    <a-form layout="vertical" @submit.prevent="save">
      <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
      <a-form-item v-if="mode==='add'" label="人员" html-for="appointment-person" required :extra="searchError">
        <a-select id="appointment-person" v-model:value="personId" :filter-option="false" :options="directory"
          :loading="searching" placeholder="输入姓名搜索" show-search @search="searchPeople" @dropdown-visible-change="open=>open && searchPeople()"/>
      </a-form-item>
      <p v-else>{{ record.personName }} · {{ record.unitName }}<template v-if="record.position"> · {{ record.position }}</template></p>
      <template v-if="needsTarget">
        <a-form-item :label="mode==='transfer'?'调入组织':'任职组织'" html-for="appointment-unit" required>
          <TreeSelect id="appointment-unit" v-model:value="target" :tree-data="tree"
            tree-node-filter-prop="title" show-search placeholder="选择组织"/>
        </a-form-item>
        <a-form-item label="任职类别" html-for="appointment-kind" required>
          <a-select id="appointment-kind" v-model:value="kind" :options="kinds" placeholder="选择类别"/>
        </a-form-item>
        <a-form-item label="岗位名称" html-for="appointment-position">
          <a-input id="appointment-position" v-model:value="position" :maxlength="100"/>
        </a-form-item>
      </template>
      <a-form-item :label="mode==='end'?'结束时间（北京时间）':'生效时间（北京时间）'" html-for="appointment-effective" required>
        <a-input id="appointment-effective" v-model:value="effective" type="datetime-local" required/>
      </a-form-item>
      <a-form-item v-if="mode==='add'" label="结束时间（北京时间）" html-for="appointment-end-time" extra="长期任职可留空。">
        <a-input id="appointment-end-time" v-model:value="through" type="datetime-local"/>
      </a-form-item>
      <a-space>
        <a-button :disabled="saving" @click="emit('close')">取消</a-button>
        <a-button type="primary" html-type="submit" :loading="saving">保存</a-button>
      </a-space>
    </a-form>
  </a-drawer>
</template>

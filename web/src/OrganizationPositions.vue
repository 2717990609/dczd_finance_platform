<script setup>
import { computed, onMounted, ref } from 'vue'
import { Switch } from 'ant-design-vue'

const props = defineProps({api:Function,canWrite:Boolean,scopes:{type:Array,default:()=>[]}})
const emit = defineEmits(['close','changed'])
const path = '/organization-positions'
const positions = ref([])
const loading = ref(false)
const pending = ref()
const saving = computed(() => Boolean(pending.value))
const error = ref('')
const notice = ref('')
const search = ref('')
const draft = ref()
const rows = computed(() => {
  const term = search.value.trim().toLocaleLowerCase()
  return positions.value.filter(position=>position.name.toLocaleLowerCase().includes(term))
})
const columns = [
  {title:'岗位名称',dataIndex:'name',key:'name',width:'38%'},
  {title:'适用范围',key:'scopes'},
  {title:'状态',key:'status',width:86},
]
const scopeLabel = key => props.scopes.find(scope=>scope.value===key)?.label || key
const scopeDescription = keys => props.scopes.length && props.scopes.every(scope=>keys.includes(scope.value))
  ? '全部范围' : keys.map(scopeLabel).join('、')

async function load() {
  loading.value = true
  error.value = ''
  try {
    positions.value = await props.api(path)
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}
function openCreate() {
  error.value = ''
  notice.value = ''
  draft.value = {
    name:'',scopes:props.scopes.map(scope=>scope.value),active:true,
  }
}
function cancel() {
  draft.value = undefined
  error.value = ''
}
function createPosition() {
  if (draft.value?.name.trim() && draft.value.scopes.length) save(draft.value)
}
async function save(input) {
  if (!props.canWrite || saving.value) return
  pending.value = input.id || 'create'
  error.value = ''
  notice.value = ''
  try {
    const updated = await props.api(path,{method:'POST',body:JSON.stringify(input)})
    if (input.id) {
      positions.value = positions.value.map(position=>position.id===updated.id ? updated : position)
      notice.value = updated.name+(updated.active ? '已启用' : '已停用')
    } else {
      notice.value = updated.name+'已保存'
      draft.value = undefined
      await load()
    }
    emit('changed')
  } catch (err) {
    error.value = err.message
  } finally {
    pending.value = undefined
  }
}
onMounted(load)
</script>

<template>
  <a-drawer title="岗位目录" open width="min(680px, 100vw)" :closable="!saving" :keyboard="!saving"
    :mask-closable="!saving" @close="emit('close')">
    <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
    <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon/>
    <a-form v-if="draft" :model="draft" layout="vertical" size="small" :disabled="saving" @finish="createPosition">
      <div class="position-fields">
        <a-form-item label="岗位名称" html-for="organization-position-name" required>
          <a-input id="organization-position-name" v-model:value="draft.name" :maxlength="80" autofocus/>
        </a-form-item>
        <a-form-item label="适用范围" html-for="organization-position-scopes" required>
          <a-select id="organization-position-scopes" v-model:value="draft.scopes" :options="scopes" mode="multiple"/>
        </a-form-item>
        <a-form-item label="状态" html-for="organization-position-active">
          <Switch id="organization-position-active" aria-label="岗位状态" v-model:checked="draft.active" checked-children="启用" un-checked-children="停用"/>
        </a-form-item>
      </div>
      <div class="position-actions">
        <a-button size="small" :disabled="saving" @click="cancel">取消</a-button>
        <a-button type="primary" size="small" html-type="submit" :loading="saving"
          :disabled="!draft.name.trim() || !draft.scopes.length">保存岗位</a-button>
      </div>
    </a-form>
    <div v-else class="position-toolbar">
      <a-input v-model:value="search" placeholder="搜索岗位名称" aria-label="搜索岗位名称" allow-clear/>
      <a-button v-if="canWrite" type="primary" :disabled="loading || saving || !scopes.length" @click="openCreate">新增岗位</a-button>
    </div>
    <a-table :columns="columns" :data-source="rows" :loading="loading && !saving" row-key="id" size="small"
      :pagination="{pageSize:10,hideOnSinglePage:true,showSizeChanger:false}">
      <template #bodyCell="{column,record}">
        <template v-if="column.key==='scopes'">{{ scopeDescription(record.scopes) }}</template>
        <Switch v-else-if="column.key==='status'" :checked="record.active" :aria-label="record.name+'状态'"
          checked-children="启用" un-checked-children="停用" :loading="pending===record.id"
          :disabled="!canWrite || Boolean(draft) || saving"
          @change="active=>save({id:record.id,revision:record.revision,active})"/>
      </template>
    </a-table>
  </a-drawer>
</template>

<style scoped>
.position-toolbar,.position-actions { display:flex; justify-content:space-between; gap:8px; margin-bottom:12px; }
.position-toolbar .ant-input-affix-wrapper { width:min(100%,240px); }
.position-actions { justify-content:flex-end; }
.position-fields { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,165px),1fr)); gap:0 10px; }
.position-fields :deep(.ant-form-item) { margin-bottom:8px; }
</style>

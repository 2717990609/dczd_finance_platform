<script setup>
import { onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({api:Function})
const emit = defineEmits(['close','changed'])
const base = '/organization-sales-categories'
const items = ref([])
const edit = ref()
const readError = ref('')
const saveError = ref('')
const loading = ref(false)
const saving = ref(false)
let request = 0
const columns = [
  {title:'销售类别',dataIndex:'name',key:'name',width:220},
  {title:'状态',key:'active',width:100},
  {title:'操作',key:'actions',width:92},
]
async function refresh() {
  const seq = ++request
  loading.value = true
  readError.value = ''
  try {
    const rows = await props.api(base)
    if (seq===request) items.value = rows
  } catch (err) {
    if (seq===request) readError.value = err.message
  } finally {
    if (seq===request) loading.value = false
  }
}
function open(row) {
  saveError.value = ''
  edit.value = row ? {...row} : {name:'',active:true}
}
async function save() {
  if (!edit.value || saving.value) return
  const {id, name, active, version} = edit.value
  if (!name.trim()) {
    saveError.value = '请填写销售类别名称。'
    return
  }
  saving.value = true
  saveError.value = ''
  try {
    await props.api(base+(id?'/update':''),{method:'POST',body:JSON.stringify(id
      ? {id,name,active,expectedVersion:version} : {name})})
    edit.value = undefined
    emit('changed')
    await refresh()
  } catch (err) {
    saveError.value = err.message
  } finally {
    saving.value = false
  }
}

onMounted(refresh)
onBeforeUnmount(() => { request++ })
</script>

<template>
  <a-drawer :open="true" title="管理销售类别" width="min(540px, 100vw)"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <a-alert v-if="readError" class="feedback" type="error" :message="readError" show-icon>
      <template #action><a-button size="small" @click="refresh">重试</a-button></template>
    </a-alert>
    <div v-if="edit" class="category-editor-form" aria-label="类别编辑">
      <a-alert v-if="saveError" class="feedback" type="error" :message="saveError" show-icon/>
      <a-form :model="edit" layout="vertical" @finish="save">
        <a-form-item label="类别名称" html-for="category-editor-name" required>
          <a-input id="category-editor-name" v-model:value="edit.name" placeholder="最多 10 个字符" :disabled="saving"/>
        </a-form-item>
        <a-form-item v-if="edit.id" label="状态">
          <a-checkbox v-model:checked="edit.active" :disabled="saving">启用</a-checkbox>
        </a-form-item>
        <a-space>
          <a-button type="primary" html-type="submit" :loading="saving">保存类别</a-button>
          <a-button :disabled="saving" @click="edit=undefined">取消</a-button>
        </a-space>
      </a-form>
    </div>
    <a-button v-else type="primary" class="feedback" @click="open()">新建类别</a-button>
    <a-table :columns="columns" :data-source="items" :loading="loading" row-key="id" :scroll="{x:412}"
      :pagination="{pageSize:15,hideOnSinglePage:true,showSizeChanger:false}">
      <template #bodyCell="{column,record}">
        <a-tag v-if="column.key==='active'" :color="record.active?'green':'default'">{{ record.active ? '启用' : '停用' }}</a-tag>
        <a-button v-else-if="column.key==='actions'" type="link" size="small" :disabled="saving" @click="open(record)">编辑</a-button>
      </template>
    </a-table>
  </a-drawer>
</template>

<style scoped>
.category-editor-form { margin-bottom: 16px; }
</style>

<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'

const props = defineProps({ products:Array, api:Function, canPublish:Function })
const product = defineModel({type:Number})
const directory = ref()
const loading = ref(false)
const error = ref('')
const editor = ref()
const editorError = ref('')
const saving = ref(false)
let sequence = 0
const options = computed(() => props.products.map(row => ({value:row.id,label:row.name})))
const units = computed(() => directory.value?.units.map(row => ({value:row.id,label:row.name})) || [])
const columns = [
  {title:'团队池名称', dataIndex:'name', width:230},
  {title:'归集组织', dataIndex:'unitName', width:200},
  {title:'组织状态', key:'status', width:110},
]
watch(options, choices => {
  if (!choices.some(item=>item.value===product.value)) product.value = choices.length===1 ? choices[0].value : undefined
}, {immediate:true})
async function load() {
  const request = ++sequence
  error.value = ''
  if (!product.value) return
  loading.value = true
  try {
    const result = await props.api('/practice-pools?productId='+product.value)
    if (request===sequence) directory.value = result
  } catch (err) {
    if (request===sequence) error.value = err.message
  } finally {
    if (request===sequence) loading.value = false
  }
}
watch(product, () => {
  directory.value = undefined
  loading.value = false
  load()
}, {immediate:true})
onBeforeUnmount(() => { sequence++ })
defineExpose({refresh:load})

function openEditor() {
  editorError.value = ''
  editor.value = {productId:product.value, name:'', unitId:undefined}
}
async function save() {
  if (!editor.value || saving.value) return
  const {productId, unitId, name} = editor.value
  if (!name.trim() || !unitId) {
    editorError.value = '填写团队池名称并选择归集组织。'
    return
  }
  editorError.value = ''
  saving.value = true
  try {
    await props.api('/practice-pools',{method:'POST',body:JSON.stringify({productId,unitId,name:name.trim()})})
    editor.value = undefined
    await load()
  } catch (err) {
    editorError.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section class="records">
    <div class="records-head pool-directory-head">
      <div class="topbar-actions">
        <label for="pool-config-product">产品</label>
        <a-select id="pool-config-product" v-model:value="product" :options="options" placeholder="选择产品" style="min-width: 180px"/>
      </div>
      <div class="topbar-actions">
        <a-button v-if="product" :loading="loading" @click="load">刷新</a-button>
        <a-button v-if="product && canPublish(product)" type="primary" :disabled="!directory" @click="openEditor">新增团队池</a-button>
      </div>
    </div>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <a-table :columns="columns" :data-source="directory?.items || []" row-key="id" :loading="loading"
      :pagination="{pageSize:15,showSizeChanger:false,hideOnSinglePage:true}" :scroll="{x:550}">
      <template #bodyCell="{column,record}">
        <a-tag v-if="column.key==='status'" :color="record.unitStatus==='ACTIVE' ? 'green' : 'default'">{{ record.unitStatus==='ACTIVE' ? '正常' : '已停用' }}</a-tag>
      </template>
      <template #emptyText>{{ product ? '本产品暂无团队池' : '请选择可配置团队池的产品' }}</template>
    </a-table>
  </section>
  <Modal v-if="editor" :open="true" title="新增团队池" ok-text="保存" cancel-text="取消" :confirm-loading="saving"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @cancel="!saving && (editor=undefined)" @ok="save">
    <a-alert v-if="editorError" class="feedback" type="error" :message="editorError" show-icon/>
    <a-form layout="vertical" @finish="save">
      <a-form-item label="团队池名称" html-for="pool-config-name" required>
        <a-input id="pool-config-name" v-model:value="editor.name" :maxlength="80" :disabled="saving"/>
      </a-form-item>
      <a-form-item label="归集组织" html-for="pool-config-unit" required>
        <a-select id="pool-config-unit" v-model:value="editor.unitId" :options="units" :disabled="saving"
          show-search option-filter-prop="label" placeholder="搜索组织名称"/>
      </a-form-item>
    </a-form>
  </Modal>
</template>

<style scoped>
.pool-directory-head { flex-wrap: wrap; }
</style>

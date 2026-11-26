<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Modal } from 'ant-design-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({ products:Array, api:Function, canPublish:Function })
const product = defineModel({type:Number})
const directory = ref()
const loading = ref(false)
const error = ref('')
const editor = ref()
const editorError = ref('')
const saving = ref(false)
let sequence = 0
const options = computed(() => [{value:0,label:'全部产品'}, ...props.products.map(row => ({value:row.id,label:row.name}))])
const units = computed(() => directory.value?.units.map(row => ({value:row.id,label:row.name})) || [])
const columns = [
  {title:'团队池名称', dataIndex:'name', width:230},
  {title:'所属产品', dataIndex:'productName', width:170},
  {title:'归集组织', dataIndex:'unitName', width:200},
  {title:'团队池负责人', dataIndex:'ownerName', key:'owner', width:150},
  {title:'组织状态', key:'status', width:110},
  {title:'操作', key:'action', width:110},
]
watch(options, choices => {
  if (product.value && !choices.some(item=>item.value===product.value)) product.value = undefined
}, {immediate:true})
async function load() {
  const request = ++sequence
  error.value = ''
  loading.value = true
  try {
    const result = await props.api(product.value ? '/team-pools?productId='+product.value : '/team-pools')
    if (request===sequence) directory.value = result
  } catch (err) {
    if (request===sequence) error.value = err.message
  } finally {
    if (request===sequence) loading.value = false
  }
}
watch(product, () => {
  directory.value = undefined
  load()
}, {immediate:true})
onBeforeUnmount(() => { sequence++ })
defineExpose({refresh:load})

function openEditor(pool) {
  editorError.value = ''
  editor.value = pool
    ? {...pool,poolId:pool.id,originalOwner:pool.ownerName ? {id:pool.ownerPersonId,name:pool.ownerName} : undefined}
    : {productId:product.value,name:'',unitId:undefined,ownerPersonId:undefined}
}
async function save() {
  if (!editor.value || saving.value) return
  const {poolId, productId, unitId, name, ownerPersonId} = editor.value
  if (!name.trim() || !unitId) {
    editorError.value = '填写团队池名称并选择归集组织。'
    return
  }
  if (!ownerPersonId) {
    editorError.value = '请选择团队池负责人。'
    return
  }
  editorError.value = ''
  saving.value = true
  try {
    const body = poolId ? {poolId,productId,ownerPersonId} : {productId,unitId,name:name.trim(),ownerPersonId}
    await props.api(poolId ? '/team-pools/owner' : '/team-pools',{method:'POST',body:JSON.stringify(body)})
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
        <a-select id="pool-config-product" :value="product ?? 0" :options="options" style="min-width: 180px"
          @change="product=$event || undefined"/>
      </div>
      <div class="topbar-actions">
        <a-button :loading="loading" @click="load">刷新</a-button>
        <a-button v-if="product && canPublish(product)" type="primary" :disabled="!directory" @click="openEditor()">新增团队池</a-button>
      </div>
    </div>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <a-table :key="product ?? 'all'" :columns="columns" :data-source="directory?.items || []" row-key="id" :loading="loading"
      :pagination="{pageSize:15,showSizeChanger:false,hideOnSinglePage:true}" :scroll="{x:970}">
      <template #bodyCell="{column,record}">
        <a-tag v-if="column.key==='status'" :color="record.unitStatus==='ACTIVE' ? 'green' : 'default'">{{ record.unitStatus==='ACTIVE' ? '正常' : '已停用' }}</a-tag>
        <span v-else-if="column.key==='owner'">{{ record.ownerName || '待指定' }}</span>
        <a-button v-else-if="column.key==='action' && canPublish(record.productId)" type="link" size="small" @click="openEditor(record)">设置负责人</a-button>
      </template>
      <template #emptyText>{{ product ? '本产品暂无团队池' : '暂无团队池' }}</template>
    </a-table>
  </section>
  <Modal v-if="editor" :open="true" :title="editor.poolId ? '设置团队池负责人' : '新增团队池'" ok-text="保存" cancel-text="取消" :confirm-loading="saving"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @cancel="!saving && (editor=undefined)" @ok="save">
    <a-alert v-if="editorError" class="feedback" type="error" :message="editorError" show-icon/>
    <a-form layout="vertical" @finish="save">
      <a-form-item v-if="!editor.poolId" label="团队池名称" html-for="pool-config-name" required>
        <a-input id="pool-config-name" v-model:value="editor.name" :maxlength="80" :disabled="saving"/>
      </a-form-item>
      <a-form-item v-if="!editor.poolId" label="归集组织" html-for="pool-config-unit" required>
        <a-select id="pool-config-unit" v-model:value="editor.unitId" :options="units" :disabled="saving"
          show-search option-filter-prop="label" placeholder="搜索组织名称"/>
      </a-form-item>
      <a-form-item label="团队池负责人" html-for="pool-config-owner" required>
        <AppointmentPersonSelect id="pool-config-owner" v-model:value="editor.ownerPersonId" :api="api" :disabled="saving"
          :current="editor.originalOwner"/>
      </a-form-item>
    </a-form>
  </Modal>
</template>

<style scoped>
.pool-directory-head { flex-wrap: wrap; }
</style>

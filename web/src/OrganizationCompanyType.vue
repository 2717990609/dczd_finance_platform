<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({api:Function, company:Object, canWrite:Boolean, formatTime:Function, types:Array})
const emit = defineEmits(['close','changed'])
const base = '/organization-sales-company-types'
const details = ref()
const desired = ref()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
let request = 0

const options = computed(() => props.types.filter(type=>type.value==='INTERNAL' || type.value==='EXTERNAL'))
const label = value => props.types.find(type=>type.value===value)?.label || value
const columns = [
  {title:'变更时间',key:'changedAt',dataIndex:'changedAt',width:160},
  {title:'原类型',key:'previousType',dataIndex:'previousType',width:85},
  {title:'新类型',key:'companyType',dataIndex:'companyType',width:85},
  {title:'经办人',key:'changedBy',dataIndex:'changedBy',width:100},
]
async function load() {
  const seq = ++request
  error.value = ''
  loading.value = true
  try {
    const data = await props.api(base+'?'+new URLSearchParams({unitId:props.company.id}))
    if (seq===request) {
      details.value = data
      desired.value = data.companyType==='UNKNOWN' ? undefined : data.companyType
    }
  } catch (err) {
    if (seq===request) error.value = err.message
  } finally {
    if (seq===request) loading.value = false
  }
}

async function save() {
  if (!details.value || !desired.value || desired.value===details.value.companyType || saving.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await props.api(base,{method:'POST',body:JSON.stringify({
      unitId:props.company.id,companyType:desired.value,expectedVersion:details.value.version,
    })})
    notice.value = '公司类型已保存。'
    emit('changed')
    await load()
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => { request++ })
</script>

<template>
  <a-drawer :open="true" :title="company.name+' · 公司类型'" width="min(560px, 100vw)"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <section aria-label="公司类型与变更记录">
      <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon>
        <template #action><a-button size="small" :disabled="saving" @click="load">重新读取</a-button></template>
      </a-alert>
      <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon/>
      <p v-if="details" class="company-type-current">
        <span>当前类型</span><a-tag :color="details.companyType==='UNKNOWN' ? 'orange' : 'blue'">{{ label(details.companyType) }}</a-tag>
        <a-tag :color="details.status==='ACTIVE' ? 'green' : 'default'">{{ details.status==='ACTIVE' ? '启用' : '停用' }}</a-tag>
      </p>
      <a-form v-if="canWrite && details" :model="details" layout="vertical" @finish="save">
        <a-form-item label="调整类型" html-for="company-type-select">
          <a-select id="company-type-select" v-model:value="desired" :options="options" :disabled="saving || loading" placeholder="选择内部或外部"/>
        </a-form-item>
        <p class="draft-report-note">公司类型用于组织管理，计提按已发布的产品规则执行。</p>
        <a-button class="feedback" type="primary" html-type="submit" :loading="saving"
          :disabled="loading || !desired || desired===details.companyType">保存类型</a-button>
      </a-form>
      <h3>类型变更记录</h3>
      <a-table :columns="columns" :data-source="details?.changes || []" :loading="loading" row-key="version"
        :scroll="{x:430}" :pagination="{pageSize:10,hideOnSinglePage:true,showSizeChanger:false}">
        <template #bodyCell="{column,text}">
          <span v-if="column.key==='changedAt'">{{ formatTime(text) }}</span>
          <span v-else-if="column.key==='previousType'||column.key==='companyType'">{{ label(text) }}</span>
          <span v-else-if="column.key==='changedBy'">{{ text || '—' }}</span>
        </template>
        <template #emptyText>暂无类型变更记录</template>
      </a-table>
    </section>
  </a-drawer>
</template>

<style scoped>
.company-type-current { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
</style>

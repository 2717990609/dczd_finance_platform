<script setup>
import { computed, ref, watch } from 'vue'
import { Upload } from 'ant-design-vue'

const props = defineProps({
  api:Function, productId:Number, periodId:Number, blocked:Boolean, people:Array, units:Array, formatTime:Function,
})
const emit = defineEmits(['add'])
const open = ref(false)
const file = ref()
const rate = ref()
const preview = ref()
const busy = ref(false)
const error = ref('')
let request = 0

const names = computed(() => {
  const lookup = options => new Map(options.map(option=>[option.value,option.label]))
  return {people:lookup(props.people),units:lookup(props.units)}
})
function recipient(benefit) {
  return benefit.personId ? names.value.people.get(benefit.personId) || '个人'
    : names.value.units.get(benefit.unitId) || '公司'
}
function selectFile(selected) {
  file.value = selected
  return false
}
watch([file,rate,()=>props.productId,()=>props.periodId], () => {
  request++
  busy.value = false
  preview.value = undefined
  error.value = ''
})
async function readPreview() {
  if (!file.value || rate.value == null || busy.value) return
  const current = ++request
  busy.value = true
  error.value = ''
  preview.value = undefined
  try {
    if (file.value.size > 850*1024) throw new Error('归属文件太大，请使用已核对的关系版本文件。')
    let document
    try {
      document = JSON.parse(await file.value.text())
    } catch {
      throw new Error('无法读取归属文件，请选择已核对的关系版本 JSON。')
    }
    const result = await props.api('/practice-registration-import/preview',{method:'POST',body:JSON.stringify({
      productId:props.productId, ...(props.periodId ? {periodId:props.periodId} : {}),
      rate:Math.round(rate.value*10000), document,
    })})
    if (current===request) preview.value = result
  } catch (err) {
    if (current===request) error.value = err.message
  } finally {
    if (current===request) busy.value = false
  }
}
function add() {
  if (!preview.value?.components?.length || props.blocked) return
  emit('add',preview.value)
}
</script>

<template>
  <a-button size="small" :aria-expanded="open" @click="open=!open">批量导入报名归属</a-button>
  <section v-if="open" class="registration-import" aria-label="报名归属导入">
    <div class="registration-import-controls">
      <Upload accept=".json,application/json" :before-upload="selectFile" :show-upload-list="false">
        <a-button>{{ file ? '更换文件' : '选择关系版本文件' }}</a-button>
      </Upload>
      <span v-if="file" class="registration-file">{{ file.name }}</span>
      <a-form-item label="计提比例（%）" html-for="registration-import-rate">
        <a-input-number id="registration-import-rate" v-model:value="rate" :min="0" :max="100" :precision="4"/>
      </a-form-item>
      <a-button :loading="busy" :disabled="!file || rate==null || blocked" @click="readPreview">预览导入</a-button>
    </div>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
    <div v-if="preview">
      <p>{{ preview.periodName }} · 关系 v{{ preview.version }} · {{ formatTime(preview.startsAt) }}起 · {{ preview.rowCount }} 条</p>
      <ul v-if="preview.pending?.length" aria-label="待核关系">
        <li v-for="row in preview.pending" :key="row.number">第 {{ row.number }} 条待核 · {{ row.message }}</li>
      </ul>
      <ul v-if="preview.issues?.length" aria-label="导入问题">
        <li v-for="(row,index) in preview.issues" :key="index"><template v-if="row.number">第 {{ row.number }} 条 · </template>{{ row.message }}</li>
      </ul>
      <template v-else>
        <ul aria-label="拟添加的收款安排">
          <li v-for="part in preview.components" :key="part.periodBenefit.registrationScope.sellerId">
            {{ part.periodBenefit.label }} · {{ recipient(part.periodBenefit) }} · {{ part.periodBenefit.registrationScope.companies?.length || 0 }} 家下属公司
          </li>
        </ul>
        <a-button type="primary" size="small" :disabled="blocked" @click="add">加入 {{ preview.components.length }} 项收款安排</a-button>
      </template>
    </div>
  </section>
</template>

<style scoped>
.registration-import { flex:1 1 100%; padding:8px 10px; background:var(--accent-soft); border-radius:5px; min-width:0; }
.registration-import-controls { display:flex; flex-wrap:wrap; align-items:end; gap:6px 10px; }
.registration-import-controls :deep(.ant-form-item) { margin:0; }
.registration-import p { margin:8px 0 4px; }
.registration-import ul { max-height:168px; overflow:auto; margin:4px 0 8px; padding-left:20px; }
.registration-file { overflow-wrap:anywhere; }
</style>

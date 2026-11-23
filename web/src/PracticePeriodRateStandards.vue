<script setup>
import { computed } from 'vue'

defineProps({prefix:String,companies:Array,categories:Array})
const rows = defineModel({type:Array,required:true})
const hasFallback = computed(() => rows.value.some(row=>row.scope==='general'))
</script>

<template>
  <div class="practice-standard-rates" aria-label="比例标准">
    <div class="practice-standard-toolbar">
      <small>匹配顺序　指定公司 → 销售类别默认 → 通用比例</small>
      <a-space wrap :size="4">
        <a-button size="small" @click="rows.push({scope:'category',salesCategoryIds:[]})">添加类别默认比例</a-button>
        <a-button size="small" @click="rows.push({scope:'company',salesCompanies:[]})">添加公司特例</a-button>
        <a-button v-if="!hasFallback" size="small" @click="rows.push({scope:'general'})">添加通用比例</a-button>
      </a-space>
    </div>
    <div v-for="(row,i) in rows" :key="i" class="practice-standard-row">
      <a-form-item v-if="row.scope==='category'" label="销售类别" :html-for="prefix+'-category-'+i" required>
        <a-select :id="prefix+'-category-'+i" v-model:value="row.salesCategoryIds" :options="categories" mode="multiple"
          :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择适用类别"/>
      </a-form-item>
      <a-form-item v-else-if="row.scope==='company'" label="指定公司" :html-for="prefix+'-company-'+i" required>
        <a-select :id="prefix+'-company-'+i" v-model:value="row.salesCompanies" :options="companies" mode="multiple"
          :max-tag-count="2" allow-clear option-filter-prop="label" placeholder="选择有例外的公司"/>
      </a-form-item>
      <span v-else>通用比例</span>
      <a-form-item label="比例（%）" :html-for="prefix+'-rate-'+i" required>
        <a-input-number :id="prefix+'-rate-'+i" v-model:value="row.value" :min="0" :max="100" :precision="4"/>
      </a-form-item>
      <a-button type="link" danger size="small" @click="rows.splice(i,1)">移除</a-button>
    </div>
  </div>
</template>

<style scoped>
.practice-standard-rates {margin:8px 0 4px;padding:8px 12px;border:1px solid var(--line);border-radius:6px}
.practice-standard-toolbar {display:flex;flex-wrap:wrap;justify-content:space-between;align-items:center;gap:6px}
.practice-standard-toolbar small {color:var(--muted)}
.practice-standard-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(95px,128px) auto;align-items:end;gap:8px;margin-top:6px}
.practice-standard-row :deep(.ant-form-item) {margin:0}
.practice-standard-row > span {align-self:center}
</style>

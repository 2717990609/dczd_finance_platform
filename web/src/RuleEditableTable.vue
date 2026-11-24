<script setup>
import { computed, watch } from 'vue'

const props = defineProps({
  label: { type: String, required: true },
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
  pageSize:Number,
  wideFirstColumn:Boolean,
  rowKey:Function,
})
const page = defineModel('page',{type:Number,default:1})
const headers = computed(() => props.columns.map(column=>typeof column==='string' ? {key:column,title:column} : column))
const totalWidth = computed(() => headers.value.reduce((total,column)=>total+(column.width || 0),0))
const pageCount = computed(() => props.pageSize ? Math.max(1,Math.ceil(props.rows.length/props.pageSize)) : 1)
const currentPage = computed(() => Math.min(page.value,pageCount.value))
const offset = computed(() => props.pageSize ? (currentPage.value-1)*props.pageSize : 0)
const visibleRows = computed(() => props.pageSize ? props.rows.slice(offset.value,offset.value+props.pageSize) : props.rows)
watch(pageCount,count=>{ if (page.value>count) page.value=count })
</script>

<template>
  <div class="rule-editable-table-wrap" :class="{'rule-standard-table':wideFirstColumn}">
    <table class="rule-editable-table" :aria-label="label">
      <thead>
        <tr>
          <th v-for="column in headers" :key="column.key" scope="col" :class="{'rule-table-actions':column.key==='operation'}"
            :style="column.width ? {width:(column.width/totalWidth*100)+'%'} : undefined">{{ column.title }}</th>
          <th v-if="$slots.actions" scope="col" class="rule-table-actions">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row,index) in visibleRows" :key="rowKey ? rowKey(row) : offset+index">
          <template v-if="$slots.cell">
            <td v-for="column in headers" :key="column.key" :class="{'rule-table-actions':column.key==='operation'}">
              <span v-if="column.key!=='operation'" class="rule-table-mobile-label">{{ column.title }}</span>
              <slot name="cell" :column="column" :record="row"/>
            </td>
          </template>
          <slot v-else name="cells" :row="row" :index="offset+index"/>
          <td v-if="$slots.actions" class="rule-table-actions">
            <slot name="actions" :row="row" :index="offset+index"/>
          </td>
        </tr>
      </tbody>
    </table>
    <div v-if="pageCount>1" class="rule-table-pagination" role="group" aria-label="标准分页">
      <a-button size="small" type="text" :disabled="currentPage===1" @click="page=currentPage-1">上一页</a-button>
      <small role="status">{{ currentPage }} / {{ pageCount }}</small>
      <a-button size="small" type="text" :disabled="currentPage===pageCount" @click="page=currentPage+1">下一页</a-button>
    </div>
  </div>
</template>

<style scoped>
.rule-editable-table-wrap { min-width:0; margin-bottom:6px; container-type:inline-size }
.rule-editable-table { width:100%; table-layout:fixed; border-collapse:collapse; background:#fff }
.rule-editable-table th,.rule-editable-table :deep(td) { padding:3px 6px; text-align:start; border-bottom:1px solid var(--line) }
.rule-editable-table th { background:#f3f6fa; font-weight:600 }
.rule-editable-table :deep(td) {overflow-wrap:anywhere}
.rule-table-mobile-label {display:none}
.rule-editable-table .rule-table-actions { width:72px; text-align:end; white-space:nowrap }
.rule-editable-table .rule-table-actions :deep(.ant-btn) { padding-inline:0 }
.rule-editable-table :deep(.ant-form-item) { margin:0; min-width:0 }
.rule-editable-table :deep(.ant-form-item-label) {
  position:absolute; width:1px; height:1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap;
}
.rule-editable-table :deep(.ant-input-number),.rule-editable-table :deep(.ant-input-number-group-wrapper) { width:100% }
.rule-standard-table { margin:0; border:1px solid var(--line); border-radius:5px; overflow:hidden }
.rule-standard-table th:first-child { width:48% }
.rule-standard-table th:first-child:nth-last-child(3) { width:70% }
.rule-standard-table tr:last-child > :deep(td) { border-bottom:0 }
.rule-table-pagination {
  display:flex; align-items:center; justify-content:flex-end; gap:4px; padding:3px 6px;
  border-top:1px solid var(--line); font-variant-numeric:tabular-nums;
}

@container (max-width:699px) {
  .rule-editable-table,.rule-editable-table tbody { display:block }
  .rule-editable-table thead { display:none }
  .rule-editable-table tbody tr { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); border-bottom:1px solid var(--line) }
  .rule-editable-table :deep(td) { display:block; border-bottom:0; min-width:0 }
  .rule-editable-table td.rule-table-actions { width:auto; grid-column:2; align-self:end }
  .rule-table-mobile-label {display:block; margin-bottom:2px}
  .rule-standard-table :deep(td:first-child:not(.rule-table-actions)) { grid-column:1 / -1 }
  .rule-standard-table tbody tr:last-child { border-bottom:0 }
  .rule-editable-table :deep(.ant-form-item-label) {
    position:static; width:auto; height:auto; overflow:visible; clip-path:none; white-space:normal; text-align:start;
  }
}
</style>

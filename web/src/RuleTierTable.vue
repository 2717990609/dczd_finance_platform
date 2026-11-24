<script setup>
defineProps({
  label: { type: String, required: true },
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
})
</script>

<template>
  <div class="rule-tier-table-wrap">
    <table class="rule-tier-table" :aria-label="label">
      <thead>
        <tr>
          <th v-for="column in columns" :key="column" scope="col">{{ column }}</th>
          <th v-if="$slots.actions" scope="col" class="tier-actions">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row,index) in rows" :key="index">
          <slot name="cells" :row="row" :index="index"/>
          <td v-if="$slots.actions" class="tier-actions">
            <slot name="actions" :index="index"/>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.rule-tier-table-wrap { min-width:0; margin-bottom:6px; container-type:inline-size }
.rule-tier-table { width:100%; table-layout:fixed; border-collapse:collapse; background:#fff }
.rule-tier-table th,.rule-tier-table :deep(td) { padding:3px 6px; text-align:start; border-bottom:1px solid var(--line) }
.rule-tier-table th { background:#f3f6fa; font-weight:600 }
.rule-tier-table .tier-actions { width:72px; text-align:end; white-space:nowrap }
.rule-tier-table .tier-actions :deep(.ant-btn) { padding-inline:0 }
.rule-tier-table :deep(.ant-form-item) { margin:0; min-width:0 }
.rule-tier-table :deep(.ant-form-item-label) {
  position:absolute; width:1px; height:1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap;
}
.rule-tier-table :deep(.ant-input-number),.rule-tier-table :deep(.ant-input-number-group-wrapper) { width:100% }

@container (max-width:699px) {
  .rule-tier-table,.rule-tier-table tbody { display:block }
  .rule-tier-table thead { display:none }
  .rule-tier-table tbody tr { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); border-bottom:1px solid var(--line) }
  .rule-tier-table :deep(td) { display:block; border-bottom:0 }
  .rule-tier-table td.tier-actions { width:auto; grid-column:2; align-self:end }
  .rule-tier-table :deep(.ant-form-item-label) {
    position:static; width:auto; height:auto; overflow:visible; clip-path:none; white-space:normal; text-align:start;
  }
}
</style>

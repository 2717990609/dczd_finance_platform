<script setup>
const props = defineProps({ config:Object, sections:Array, amounts:Array, includeRetrain:Boolean, prefix:{type:String,default:''} })
function addRow(section) {
  const mode = props.amounts[0]?.value
  props.config[section].push(props.includeRetrain ? {mode,retrain:{mode}} : {mode})
}
function setMode(row, mode) {
  if (row.mode===mode) return
  row.mode = mode
  row.value = undefined
}
</script>
<template>
    <fieldset v-for="section in sections" :key="section.key" class="tier-ranges">
      <legend>{{ section.title }}</legend>
      <p class="tier-note">上下限均含当前值。上限留空时，延续至下一档的起点之前。</p>
      <table class="tier-range-table" :aria-label="section.title">
        <thead>
          <tr>
            <th scope="col">下限（{{ section.unit }}）</th>
            <th scope="col">上限（{{ section.unit }}）</th>
            <th scope="col">{{ includeRetrain ? '新报计提方式' : '计提方式' }}</th>
            <th scope="col">{{ includeRetrain ? '新报计提标准' : '计提标准' }}</th>
            <th v-if="includeRetrain" scope="col">复训计提方式</th>
            <th v-if="includeRetrain" scope="col">复训计提标准</th>
            <th scope="col" class="tier-actions">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(row, index) in config[section.key]" :key="index">
            <td>
              <a-form-item :html-for="prefix+'tier-from-'+section.key+index" :label="'下限（'+section.unit+'）'" required>
                <a-input-number :id="prefix+'tier-from-'+section.key+index" v-model:value="row.from" :min="0" :precision="section.decimals"/>
              </a-form-item>
            </td>
            <td>
              <a-form-item :html-for="prefix+'tier-through-'+section.key+index" :label="'上限（'+section.unit+'）'">
                <a-input-number :id="prefix+'tier-through-'+section.key+index" v-model:value="row.through" :min="0" :precision="section.decimals"/>
              </a-form-item>
            </td>
            <td>
              <a-form-item :html-for="prefix+'tier-mode-'+section.key+index" :label="includeRetrain ? '新报计提方式' : '计提方式'">
                <a-select :id="prefix+'tier-mode-'+section.key+index" :value="row.mode" :options="amounts" @change="setMode(row,$event)"/>
              </a-form-item>
            </td>
            <td>
              <a-form-item :html-for="prefix+'tier-value-'+section.key+index"
                :label="(includeRetrain ? '新报' : '')+(row.mode==='rate' ? '比例（%）' : '每份金额（元）')" required>
                <a-input-number :id="prefix+'tier-value-'+section.key+index" v-model:value="row.value" :addon-after="row.mode==='rate' ? '%' : '元'"
                  :min="0" :max="row.mode==='rate' ? 100 : undefined" :precision="2"/>
              </a-form-item>
            </td>
            <template v-if="includeRetrain">
              <td>
                <a-form-item :html-for="prefix+'tier-retrain-mode-'+section.key+index" label="复训计提方式">
                  <a-select :id="prefix+'tier-retrain-mode-'+section.key+index" :value="row.retrain.mode" :options="amounts"
                    @change="row.retrain={mode:$event}"/>
                </a-form-item>
              </td>
              <td>
                <a-form-item :html-for="prefix+'tier-retrain-value-'+section.key+index"
                  :label="row.retrain.mode==='rate' ? '复训比例（%）' : '复训每份金额（元）'" required>
                  <a-input-number :id="prefix+'tier-retrain-value-'+section.key+index" v-model:value="row.retrain.value" :addon-after="row.retrain.mode==='rate' ? '%' : '元'"
                    :min="0" :max="row.retrain.mode==='rate' ? 100 : undefined" :precision="2"/>
                </a-form-item>
              </td>
            </template>
            <td class="tier-actions">
              <a-button v-if="config[section.key].length>1" type="link" danger size="small"
                @click="config[section.key].splice(index,1)">移除档位</a-button>
            </td>
          </tr>
        </tbody>
      </table>
      <a-button type="dashed" size="small" @click="addRow(section.key)">添加{{ section.title }}</a-button>
    </fieldset>
</template>

<style scoped>
.tier-ranges { container-type:inline-size }
.tier-range-table { width:100%; table-layout:fixed; border-collapse:collapse; margin-bottom:6px; background:white }
.tier-range-table th,.tier-range-table td { padding:3px 6px; text-align:left; border-bottom:1px solid var(--line) }
.tier-range-table th { background:#f3f6fa; font-weight:600; white-space:nowrap }
.tier-range-table .tier-actions { width:72px; text-align:right; white-space:nowrap }
.tier-range-table .tier-actions :deep(.ant-btn) { padding-inline:0 }
.tier-range-table :deep(.ant-form-item) { margin:0; min-width:0 }
.tier-range-table :deep(.ant-form-item-label) {
  position:absolute; width:1px; height:1px; overflow:hidden; clip-path:inset(50%); white-space:nowrap;
}
.tier-range-table :deep(.ant-input-number),.tier-range-table :deep(.ant-input-number-group-wrapper) { width:100% }

@container (max-width:699px) {
  .tier-range-table,.tier-range-table tbody { display:block }
  .tier-range-table thead { display:none }
  .tier-range-table tbody tr { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); border-bottom:1px solid var(--line) }
  .tier-range-table td { display:block; border-bottom:0 }
  .tier-range-table :deep(.ant-form-item-label) {
    position:static; width:auto; height:auto; overflow:visible; clip-path:none; white-space:normal;
  }
}
</style>

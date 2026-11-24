<script setup>
import RuleEditableTable from './RuleEditableTable.vue'

const props = defineProps({ config:Object, sections:Array, amounts:Array, includeRetrain:Boolean, prefix:{type:String,default:''} })
function columns(unit) {
  const bounds = ['下限（'+unit+'）','上限（'+unit+'）']
  return props.includeRetrain
    ? [...bounds,'新报计提方式','新报计提标准','复训计提方式','复训计提标准']
    : [...bounds,'计提方式','计提标准']
}
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
      <RuleEditableTable :label="section.title" :columns="columns(section.unit)" :rows="config[section.key]">
        <template #cells="{row,index}">
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
        </template>
        <template #actions="{index}">
          <a-button v-if="config[section.key].length>1" type="link" danger size="small"
            @click="config[section.key].splice(index,1)">移除档位</a-button>
        </template>
      </RuleEditableTable>
      <a-button type="dashed" size="small" @click="addRow(section.key)">添加{{ section.title }}</a-button>
    </fieldset>
</template>

<style scoped>
.tier-ranges { min-width:0; padding:0; margin:0 0 6px; border:0 }
.tier-ranges:last-child { margin-bottom:0 }
.tier-ranges legend { font-weight:600; margin-bottom:2px }
</style>

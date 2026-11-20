<script setup>
const props = defineProps({ config:Object, sections:Array, amounts:Array, includeRetrain:Boolean, prefix:{type:String,default:''} })
function addRow(section) {
  const mode = props.amounts[0]?.value
  props.config[section].push(props.includeRetrain ? {mode,retrain:{mode}} : {mode})
}
</script>
<template>
    <fieldset v-for="section in sections" :key="section.key" class="tier-ranges">
      <legend>{{ section.title }}</legend>
      <p class="tier-note">上下限均含当前值。上限留空时，延续至下一档的起点之前。</p>
      <div v-for="(row, index) in config[section.key]" :key="index" class="tier-range">
        <div class="tier-values" :class="{'tier-values-with-retrain':includeRetrain}">
          <a-form-item :html-for="prefix+'tier-from-'+section.key+index" :label="'下限（'+section.unit+'）'" required>
            <a-input-number :id="prefix+'tier-from-'+section.key+index" v-model:value="row.from" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-through-'+section.key+index" :label="'上限（'+section.unit+'）'">
            <a-input-number :id="prefix+'tier-through-'+section.key+index" v-model:value="row.through" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-mode-'+section.key+index" :label="includeRetrain ? '新报计提方式' : '计提方式'">
            <a-select :id="prefix+'tier-mode-'+section.key+index" v-model:value="row.mode" :options="amounts"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-value-'+section.key+index" :label="includeRetrain ? '新报计提值' : '计提值'" required>
            <a-input-number :id="prefix+'tier-value-'+section.key+index" v-model:value="row.value" :min="0" :precision="2"/>
          </a-form-item>
          <template v-if="includeRetrain">
            <a-form-item :html-for="prefix+'tier-retrain-mode-'+section.key+index" label="复训计提方式">
              <a-select :id="prefix+'tier-retrain-mode-'+section.key+index" :value="row.retrain.mode" :options="amounts"
                @change="row.retrain={mode:$event}"/>
            </a-form-item>
            <a-form-item :html-for="prefix+'tier-retrain-value-'+section.key+index"
              :label="row.retrain.mode==='rate' ? '复训比例（%）' : '复训每份金额（元）'" required>
              <a-input-number :id="prefix+'tier-retrain-value-'+section.key+index" v-model:value="row.retrain.value"
                :min="0" :max="row.retrain.mode==='rate' ? 100 : undefined" :precision="2"/>
            </a-form-item>
          </template>
        </div>
        <a-button v-if="config[section.key].length>1" type="link" danger @click="config[section.key].splice(index,1)">移除档位</a-button>
      </div>
      <a-button type="dashed" block @click="addRow(section.key)">添加{{ section.title }}</a-button>
    </fieldset>
</template>
<style scoped>
.tier-values-with-retrain { grid-template-columns: repeat(auto-fit,minmax(min(100%,130px),1fr)); }
</style>

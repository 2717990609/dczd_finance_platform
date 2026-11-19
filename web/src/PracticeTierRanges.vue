<script setup>
defineProps({ config:Object, sections:Array, amounts:Array, prefix:{type:String,default:''} })
</script>
<template>
    <fieldset v-for="section in sections" :key="section.key" class="tier-ranges">
      <legend>{{ section.title }}</legend>
      <p class="tier-note">上下限均含当前值。上限留空时，延续至下一档的起点之前。</p>
      <div v-for="(row, index) in config[section.key]" :key="index" class="tier-range">
        <div class="tier-values">
          <a-form-item :html-for="prefix+'tier-from-'+section.key+index" :label="'下限（'+section.unit+'）'" required>
            <a-input-number :id="prefix+'tier-from-'+section.key+index" v-model:value="row.from" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-through-'+section.key+index" :label="'上限（'+section.unit+'）'">
            <a-input-number :id="prefix+'tier-through-'+section.key+index" v-model:value="row.through" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-mode-'+section.key+index" label="计提方式">
            <a-select :id="prefix+'tier-mode-'+section.key+index" v-model:value="row.mode" :options="amounts"/>
          </a-form-item>
          <a-form-item :html-for="prefix+'tier-value-'+section.key+index" label="计提值" required>
            <a-input-number :id="prefix+'tier-value-'+section.key+index" v-model:value="row.value" :min="0" :precision="2"/>
          </a-form-item>
        </div>
        <a-button v-if="config[section.key].length>1" type="link" danger @click="config[section.key].splice(index,1)">移除档位</a-button>
      </div>
      <a-button type="dashed" block @click="config[section.key].push({mode:amounts[0]?.value})">添加{{ section.title }}</a-button>
    </fieldset>
</template>

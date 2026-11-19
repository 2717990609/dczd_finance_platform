<script setup>
import { computed } from 'vue'

const props = defineProps({
  config: { type: Object, required: true },
  metrics: { type: Array, required: true },
  startModes: { type: Array, required: true },
  amounts: { type: Array, required: true },
  people: { type: Array, required: true },
})
const sections = computed(() => {
  const result = []
  if (props.config.metric !== 'performance') result.push({ key: 'count', title: '累计人数档', unit: '人', decimals: 0 })
  if (props.config.metric !== 'count') result.push({ key: 'performance', title: '本期实付档', unit: '元', decimals: 2 })
  return result
})
function eligiblePeople(row) {
  return props.people.filter(person =>
    !props.config.personCountStarts.some(other => other !== row && other.personId === person.value))
}
</script>

<template>
  <div class="tier-editor">
    <a-form-item html-for="tier-metric" label="阶梯指标">
      <a-select id="tier-metric" v-model:value="config.metric" :options="metrics"/>
    </a-form-item>
    <template v-if="config.metric !== 'performance'">
      <a-form-item html-for="tier-start-mode" label="默认累计起点">
        <a-select id="tier-start-mode" v-model:value="config.startMode" :options="startModes"/>
      </a-form-item>
      <a-form-item v-if="config.startMode==='ordinal'" html-for="tier-start-ordinal" label="起始期数" required>
        <a-input-number id="tier-start-ordinal" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
      </a-form-item>
      <a-form-item v-else html-for="tier-start-time" label="起始开班时间" extra="使用北京时间。" required>
        <a-input id="tier-start-time" v-model:value="config.countFromAt" type="datetime-local"/>
      </a-form-item>
      <details class="tier-person-starts">
        <summary>按人员设置累计起点</summary>
        <p class="tier-note">指定起点到达后，该人员按自己的起点累计。</p>
        <div v-for="(row,index) in config.personCountStarts" :key="index" class="tier-range">
          <a-form-item :html-for="'tier-person-'+index" label="人员" required>
            <a-select :id="'tier-person-'+index" v-model:value="row.personId" :options="eligiblePeople(row)" show-search option-filter-prop="label"/>
          </a-form-item>
          <a-form-item :html-for="'tier-person-start-mode-'+index" label="个人累计起点">
            <a-select :id="'tier-person-start-mode-'+index" v-model:value="row.startMode" :options="startModes"/>
          </a-form-item>
          <a-form-item v-if="row.startMode==='ordinal'" :html-for="'tier-person-start-ordinal-'+index" label="个人起始期数" required>
            <a-input-number :id="'tier-person-start-ordinal-'+index" v-model:value="row.countFromOrdinal" :min="1" :precision="0"/>
          </a-form-item>
          <a-form-item v-else :html-for="'tier-person-start-time-'+index" label="个人起始开班时间" extra="使用北京时间。" required>
            <a-input :id="'tier-person-start-time-'+index" v-model:value="row.countFromAt" type="datetime-local"/>
          </a-form-item>
          <a-button type="link" danger @click="config.personCountStarts.splice(index,1)">移除个人起点</a-button>
        </div>
        <a-button type="dashed" block @click="config.personCountStarts.push({startMode:startModes[0]?.value})">添加个人起点</a-button>
      </details>
    </template>
    <fieldset v-for="section in sections" :key="section.key" class="tier-ranges">
      <legend>{{ section.title }}</legend>
      <p class="tier-note">上下限均含当前值。上限留空时，延续至下一档的起点之前。</p>
      <div v-for="(row, index) in config[section.key]" :key="index" class="tier-range">
        <div class="tier-values">
          <a-form-item :html-for="'tier-from-'+section.key+index" :label="'下限（'+section.unit+'）'" required>
            <a-input-number :id="'tier-from-'+section.key+index" v-model:value="row.from" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="'tier-through-'+section.key+index" :label="'上限（'+section.unit+'）'">
            <a-input-number :id="'tier-through-'+section.key+index" v-model:value="row.through" :min="0" :precision="section.decimals"/>
          </a-form-item>
          <a-form-item :html-for="'tier-mode-'+section.key+index" label="计提方式">
            <a-select :id="'tier-mode-'+section.key+index" v-model:value="row.mode" :options="amounts"/>
          </a-form-item>
          <a-form-item :html-for="'tier-value-'+section.key+index" label="计提值" required>
            <a-input-number :id="'tier-value-'+section.key+index" v-model:value="row.value" :min="0" :precision="2"/>
          </a-form-item>
        </div>
        <a-button v-if="config[section.key].length>1" type="link" danger @click="config[section.key].splice(index,1)">移除档位</a-button>
      </div>
      <a-button type="dashed" block @click="config[section.key].push({mode:amounts[0]?.value})">添加{{ section.title }}</a-button>
    </fieldset>
  </div>
</template>

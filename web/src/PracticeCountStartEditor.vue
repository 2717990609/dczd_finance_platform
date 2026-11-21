<script setup>
import { computed } from 'vue'
import PracticePersonCountFields from './PracticePersonCountFields.vue'

const props = defineProps({
  config: { type: Object, required: true },
  startModes: { type: Array, required: true },
  people: { type: Array, required: true },
  openings: { type: Array, required: true },
})

const personalStartModes = computed(() => props.startModes.filter(option =>
  !option.scope?.countScopes || option.scope.countScopes.includes('person')))
function eligiblePeople(row) {
  return props.people.filter(person =>
    !props.config.personCountStarts.some(other => other !== row && other.personId === person.value))
}
</script>

<template>
  <section class="rule-block practice-count-starts" aria-label="累计起点">
    <h3 class="form-subhead">累计起点</h3>
    <div class="tier-values practice-count-default">
      <a-form-item html-for="tier-start-mode" label="默认累计起点">
        <a-select id="tier-start-mode" v-model:value="config.startMode" :options="startModes"/>
      </a-form-item>
      <a-form-item v-if="config.startMode==='ordinal'" html-for="tier-start-ordinal" label="起始期数" required>
        <a-input-number id="tier-start-ordinal" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
      </a-form-item>
      <a-form-item v-else-if="config.startMode==='opening'" html-for="tier-opening" label="冻结期初"
        :extra="openings.length ? '从冻结截止时刻之后开始追加新报。' : '当前产品尚未登记累计期初。'" required>
        <a-select id="tier-opening" v-model:value="config.openingId" :options="openings" />
      </a-form-item>
      <a-form-item v-else html-for="tier-start-time" label="起始开班时间" extra="使用北京时间。" required>
        <a-input id="tier-start-time" v-model:value="config.countFromAt" type="datetime-local" step="any"/>
      </a-form-item>
    </div>
    <div class="rule-blocks-heading">
      <h4 class="rule-item-section">人员特例</h4>
      <a-button size="small" @click="config.personCountStarts.push({startMode:personalStartModes[0]?.value})">添加个人起点</a-button>
    </div>
    <div v-for="(row,index) in config.personCountStarts" :key="index" class="tier-values practice-count-row">
      <a-form-item :html-for="'tier-person-'+index" label="人员" required>
        <a-select :id="'tier-person-'+index" v-model:value="row.personId" :options="eligiblePeople(row)" show-search option-filter-prop="label"/>
      </a-form-item>
      <PracticePersonCountFields :start="row" :prefix="'tier-person-count-'+index" :start-modes="personalStartModes"/>
      <a-button type="link" danger aria-label="移除个人起点" @click="config.personCountStarts.splice(index,1)">移除</a-button>
    </div>
  </section>
</template>

<script setup>
import { useId } from 'vue'
import { RadioGroup } from 'ant-design-vue'

defineProps({compact:Boolean})

const mode = defineModel('mode', {type:String, required:true})
const date = defineModel('date', {type:String, required:true})
const dateId = useId()
const modes = [
  {label:'当前',value:'current'},
  {label:'指定日期',value:'date'},
  {label:'全部记录',value:'history'},
]
</script>

<template>
  <div class="appointment-date-filter">
    <div>
      <div v-if="!compact" class="appointment-filter-label">任职记录</div>
      <RadioGroup v-model:value="mode" role="radiogroup" aria-label="任职记录"
        :options="modes" option-type="button" button-style="solid"/>
    </div>
    <div v-if="mode==='date'">
      <label :for="dateId">任职日期</label>
      <input :id="dateId" v-model="date" type="date" class="ant-input organization-date"
        :aria-describedby="dateId+'-timezone'"/>
      <small :id="dateId+'-timezone'" class="appointment-timezone">北京时间零点</small>
    </div>
  </div>
</template>

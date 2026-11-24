<script setup>
defineProps({
  config: { type: Object, required: true },
  startModes: { type: Array, required: true },
  openings: { type: Array, required: true },
  prefix: {type:String,default:'tier'},
})
</script>

<template>
  <a-form-item :html-for="prefix+'-start-mode'" label="默认累计起点" extra="用于累计阶梯选档，个人起点优先。">
    <a-select :id="prefix+'-start-mode'" v-model:value="config.startMode" :options="startModes"/>
  </a-form-item>
  <a-form-item v-if="config.startMode==='ordinal'" :html-for="prefix+'-start-ordinal'" label="起始期数" required>
    <a-input-number :id="prefix+'-start-ordinal'" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
  </a-form-item>
  <a-form-item v-else-if="config.startMode==='opening'" :html-for="prefix+'-opening'" label="冻结期初"
    :extra="openings.length ? '沿用已登记的累计人数，再加截止后符合条件的新报。' : '当前产品尚未登记累计期初。'" required>
    <a-select :id="prefix+'-opening'" v-model:value="config.openingId" :options="openings" />
  </a-form-item>
  <a-form-item v-else-if="config.startMode==='time'" :html-for="prefix+'-start-time'" label="起始开班时间（北京时间）" required>
    <a-input :id="prefix+'-start-time'" v-model:value="config.countFromAt" type="datetime-local" step="any"/>
  </a-form-item>
</template>

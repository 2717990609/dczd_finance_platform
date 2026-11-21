<script setup>
defineProps({ start:Object, prefix:String, startModes:Array })
</script>

<template>
  <a-form-item :html-for="prefix+'-start-mode'" label="起点类型" required>
    <a-select :id="prefix+'-start-mode'" v-model:value="start.startMode" :options="startModes"/>
  </a-form-item>
  <a-form-item v-if="start.startMode==='ordinal'" :html-for="prefix+'-start-ordinal'" label="起始期数" required>
    <a-input-number :id="prefix+'-start-ordinal'" v-model:value="start.countFromOrdinal" :min="1" :precision="0"/>
  </a-form-item>
  <a-form-item v-else :html-for="prefix+'-start-time'" label="起始开班时间（北京时间）" required>
    <a-input :id="prefix+'-start-time'" v-model:value="start.countFromAt" type="datetime-local" step="any"/>
  </a-form-item>
</template>

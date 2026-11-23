<script setup>
defineProps({
  config: { type: Object, required: true },
  startModes: { type: Array, required: true },
  openings: { type: Array, required: true },
  prefix: {type:String,default:'tier'},
  label:String,
})
</script>

<template>
  <section class="rule-block practice-count-starts" aria-label="累计起点">
    <h3 class="form-subhead">累计起点<template v-if="label"> · {{ label }}</template></h3>
    <div class="tier-values practice-count-default">
      <a-form-item :html-for="prefix+'-start-mode'" label="默认累计起点">
        <a-select :id="prefix+'-start-mode'" v-model:value="config.startMode" :options="startModes"/>
      </a-form-item>
      <a-form-item v-if="config.startMode==='ordinal'" :html-for="prefix+'-start-ordinal'" label="起始期数" required>
        <a-input-number :id="prefix+'-start-ordinal'" v-model:value="config.countFromOrdinal" :min="1" :precision="0"/>
      </a-form-item>
      <a-form-item v-else-if="config.startMode==='opening'" :html-for="prefix+'-opening'" label="冻结期初"
        :extra="openings.length ? '从冻结截止时刻之后开始追加新报。' : '当前产品尚未登记累计期初。'" required>
        <a-select :id="prefix+'-opening'" v-model:value="config.openingId" :options="openings" />
      </a-form-item>
      <a-form-item v-else-if="config.startMode==='time'" :html-for="prefix+'-start-time'" label="起始开班时间" required>
        <a-input :id="prefix+'-start-time'" v-model:value="config.countFromAt" type="datetime-local" step="any"/>
      </a-form-item>
    </div>
  </section>
</template>

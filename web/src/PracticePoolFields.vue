<script setup>
defineProps({target:Object,prefix:String,pools:Array})
</script>

<template>
  <a-form-item label="团队池" :html-for="prefix+'-pool'" required>
    <a-select :id="prefix+'-pool'" :value="target.poolId" :options="pools" show-search option-filter-prop="label"
      placeholder="选择已登记的团队池" @update:value="id=>Object.assign(target,{poolId:id,ownerId:undefined})"/>
  </a-form-item>
</template>

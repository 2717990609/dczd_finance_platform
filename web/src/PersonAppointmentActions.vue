<script setup>
defineProps({label:String})
defineEmits(['select'])
</script>

<template>
 <a-dropdown :trigger="['click']">
  <a-button size="small" :aria-label="'管理'+label+'任职'">管理</a-button>
  <template #overlay>
   <a-menu @click="({key})=>$emit('select',key)">
    <a-menu-item key="transfer">调动任职</a-menu-item>
    <a-menu-item key="end">结束任职</a-menu-item>
   </a-menu>
  </template>
 </a-dropdown>
</template>

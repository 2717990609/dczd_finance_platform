<script setup>
defineProps({label:String})
defineEmits(['select'])
</script>

<template>
 <a-space wrap :size="4">
  <a-button type="link" size="small" :aria-label="'调动'+label+'任职'" @click="$emit('select','transfer')">调动</a-button>
  <a-button type="text" size="small" :aria-label="'结束'+label+'任职'" @click="$emit('select','end')">结束</a-button>
 </a-space>
</template>

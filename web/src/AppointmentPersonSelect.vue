<script setup>
import { onBeforeUnmount, ref } from 'vue'

defineOptions({inheritAttrs:false})
const props = defineProps({api:Function})
const value = defineModel('value', {type:Number})
const directory = ref([])
const searching = ref(false)
const error = ref('')
let query = 0
let timer

function search(name = '') {
  const request = ++query
  clearTimeout(timer)
  searching.value = true
  error.value = ''
  timer = setTimeout(async () => {
    try {
      const rows = await props.api('/organization-people?'+new URLSearchParams({name:name.trim()}))
      if (request !== query) return
      const selected = directory.value.find(option => option.value===value.value)
      directory.value = rows.map(person=>({label:person.name+' · '+person.id,value:person.id}))
      if (selected && !directory.value.some(option=>option.value===selected.value)) directory.value.unshift(selected)
    } catch (err) {
      if (request===query) error.value = err.message
    } finally {
      if (request===query) searching.value = false
    }
  }, 200)
}
onBeforeUnmount(() => { clearTimeout(timer); query++ })
</script>

<template>
  <a-select v-bind="$attrs" v-model:value="value" :filter-option="false" :options="directory" :loading="searching"
    placeholder="输入姓名搜索" show-search @search="search" @dropdown-visible-change="open=>open && search()">
    <template #notFoundContent>{{ error || (searching ? '正在查询' : '没有匹配人员') }}</template>
  </a-select>
</template>

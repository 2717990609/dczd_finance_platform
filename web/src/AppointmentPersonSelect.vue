<script setup>
import { computed, onBeforeUnmount, ref } from 'vue'

defineOptions({inheritAttrs:false})
const props = defineProps({api:Function, endpoint:{type:String,default:'/organization-people'}, filters:Object, current:Object,
  placeholder:{type:String,default:'姓名、人员 ID 或来源账号'}})
const value = defineModel('value', {type:Number})
const directory = ref([])
const searching = ref(false)
const error = ref('')
let term = ''
let query = 0
let timer
const toOption = person=>({label:person.name+' · '+person.id,value:person.id,name:person.name})
const options = computed(() => props.current?.id && !directory.value.some(option=>option.value===props.current.id)
  ? [toOption(props.current),...directory.value] : directory.value)

function search(name = '') {
  term = name.trim()
  const request = ++query
  clearTimeout(timer)
  searching.value = true
  error.value = ''
  timer = setTimeout(async () => {
    try {
      const rows = await props.api(props.endpoint+'?'+new URLSearchParams({...props.filters,name:term}))
      if (request !== query) return
      const selected = directory.value.find(option => option.value===value.value)
      directory.value = rows.map(toOption)
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
  <a-select v-bind="$attrs" v-model:value="value" :filter-option="false" :options="options" :loading="searching"
    :placeholder="placeholder" show-search @search="search" @dropdown-visible-change="open=>open && search(term)">
    <template #notFoundContent>{{ error || (searching ? '正在查询' : '没有匹配人员') }}</template>
  </a-select>
</template>

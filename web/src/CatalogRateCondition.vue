<script setup>
defineProps({options:Object, suffix:{type:String,default:''}})
const value = defineModel({type:Object,required:true})
</script>

<template>
  <div class="field-grid">
    <div>
      <label :for="'catalog-add-company'+suffix">销售公司</label>
      <a-select :id="'catalog-add-company'+suffix" v-model:value="value.company"
        :options="options.companies" show-search option-filter-prop="label"/>
    </div>
    <div>
      <label :for="'catalog-add-relationship'+suffix">销售关系</label>
      <a-select :id="'catalog-add-relationship'+suffix" v-model:value="value.relationship" :options="options.relationships"/>
    </div>
    <div>
      <label :for="'catalog-add-category'+suffix">销售类别</label>
      <a-select :id="'catalog-add-category'+suffix" v-model:value="value.category" :options="options.categories"
        placeholder="不限" allow-clear show-search option-filter-prop="label"/>
    </div>
    <div>
      <label :for="'catalog-add-rate'+suffix">新增比例（%）</label>
      <a-input-number :id="'catalog-add-rate'+suffix" v-model:value="value.rate" :min="0" :max="100" :precision="2" string-mode/>
    </div>
  </div>
</template>

<script setup>
import RuleEditableTable from './RuleEditableTable.vue'

defineProps({tiers:Array,disabled:Boolean,formatMoney:Function})
const emit = defineEmits(['remove'])
const tierColumns = ['净额起点（元，含）','净额止点（元，不含）','计提比例（%）']
</script>

<template>
  <p class="rule-formula">按本组净额选档，净额乘该档比例。</p>
  <RuleEditableTable label="整期净额阶梯" :columns="tierColumns" :rows="tiers">
    <template #cells="{row,index}">
      <td>
        <a-form-item :label="tierColumns[0]" :html-for="'catalog-tier-from-'+index">
          <a-input-number :id="'catalog-tier-from-'+index" v-model:value="row.from" :aria-label="'第 '+(index+1)+' 档净额起点'"
            :min="0" :precision="2" :disabled="disabled || index===0"/>
        </a-form-item>
      </td>
      <td>
        <a-form-item :label="tierColumns[1]">
          {{ tiers[index+1]?.from == null ? (index===tiers.length-1 ? '不限' : '待填写') : formatMoney(Math.round(tiers[index+1].from*100)) }}
        </a-form-item>
      </td>
      <td>
        <a-form-item :label="tierColumns[2]" :html-for="'catalog-tier-rate-'+index">
          <a-input-number :id="'catalog-tier-rate-'+index" v-model:value="row.rate" :aria-label="'第 '+(index+1)+' 档计提比例'"
            :min="0" :max="100" :precision="2" :disabled="disabled"/>
        </a-form-item>
      </td>
    </template>
    <template v-if="!disabled" #actions="{index}">
      <a-button danger type="link" size="small" :disabled="tiers.length===1"
        @click="emit('remove',index)">删除</a-button>
    </template>
  </RuleEditableTable>
  <a-button v-if="!disabled" type="dashed" size="small" @click="tiers.push({from:undefined,rate:0})">新增档位</a-button>
</template>

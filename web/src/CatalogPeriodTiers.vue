<script setup>
import RuleEditableTable from './RuleEditableTable.vue'

defineProps({tiers:Array,disabled:Boolean,formatMoney:Function,basisLabel:{type:String,default:'本组净额'},ratePrecision:{type:Number,default:2},
  idPrefix:{type:String,default:'catalog-tier'},hideFormula:Boolean})
const emit = defineEmits(['remove'])
const tierColumns = ['净额起点（元，含）','净额止点（元，不含）','计提比例（%）']
</script>

<template>
  <p v-if="!hideFormula" class="rule-formula">按{{ basisLabel }}选档，适用订单净额乘该档比例。</p>
  <RuleEditableTable label="整期净额阶梯" :columns="tierColumns" :rows="tiers">
    <template #cells="{row,index}">
      <td>
        <a-form-item :label="tierColumns[0]" :html-for="idPrefix+'-from-'+index">
          <a-input-number :id="idPrefix+'-from-'+index" v-model:value="row.from" :aria-label="'第 '+(index+1)+' 档净额起点'"
            :min="0" :precision="2" :disabled="disabled || index===0"/>
        </a-form-item>
      </td>
      <td>
        <a-form-item :label="tierColumns[1]">
          {{ tiers[index+1]?.from == null ? (index===tiers.length-1 ? '不限' : '待填写') : formatMoney(Math.round(tiers[index+1].from*100)) }}
        </a-form-item>
      </td>
      <td>
        <a-form-item :label="tierColumns[2]" :html-for="idPrefix+'-rate-'+index">
          <a-input-number :id="idPrefix+'-rate-'+index" v-model:value="row.rate" :aria-label="'第 '+(index+1)+' 档计提比例'"
            :min="0" :max="100" :precision="ratePrecision" :disabled="disabled"/>
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

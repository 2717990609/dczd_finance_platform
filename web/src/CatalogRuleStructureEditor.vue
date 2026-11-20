<script setup>
import { computed, ref, watch } from 'vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import CatalogRateCondition from './CatalogRateCondition.vue'

const props = defineProps({options:Object, component:Object, api:Function, peopleFilters:Object, parseRate:Function})
const draft = defineModel({type:Object,required:true})
const editing = defineModel('editing', {type:Boolean,default:false})
const form = ref()
const error = ref('')
const targets = [{value:'person',label:'个人'},{value:'company',label:'公司'},{value:'pool',label:'团队池'}]
const formats = [{value:'fixed',label:'固定比例'},{value:'table',label:'按费率条件'}]
const organizations = computed(() => props.options.organizations.filter(option=>form.value?.target!=='company' || option.kind==='COMPANY'))
const roles = computed(() => props.options.roles.filter(option=>form.value?.target==='pool' || option.personal))
const blankCondition = () => ({company:'ANY',
  relationship:props.options.relationships.find(option=>option.value==='ANY')?.value || props.options.relationships[0]?.value})
const labelFor = (choices,value) => choices.find(option=>option.value===value)?.label || value
const percent = basisPoints => (basisPoints/100)+'%'
function conditionLabel(condition) {
  return [labelFor(props.options.companies,condition.company),labelFor(props.options.relationships,condition.relationship),
    condition.category ? labelFor(props.options.categories,condition.category) : '不限类别',percent(condition.rateBps)].join(' · ')
}
function itemLabel(item) {
  const recipient = item.target==='person' ? item.personName : labelFor(props.options.organizations,item.organization)
  return [item.label,recipient,item.rows?.length ? item.rows.length+' 条费率条件' : percent(item.rateBps)].join(' · ')
}
function begin(kind) {
  error.value = ''
  form.value = kind==='item'
    ? {kind,target:'person',format:'fixed',rows:[blankCondition()]}
    : {kind,itemKey:props.component.itemKey,itemLabel:props.component.label,row:blankCondition()}
}
function changeTarget() {
  form.value.role = undefined
  form.value.personId = undefined
  form.value.personName = ''
  form.value.organization = undefined
}
function cancel() {
  form.value = undefined
  error.value = ''
}
function readCondition(row) {
  const rateBps = props.parseRate(row.rate)
  if (!row.company || !row.relationship || rateBps===undefined) throw new Error('请选择费率条件，并填写有效比例。')
  return {company:row.company,relationship:row.relationship,category:row.category,rateBps}
}
function stage() {
  error.value = ''
  const staged = form.value
  try {
    if (staged.kind==='row') {
      draft.value.rateRows.push({itemKey:staged.itemKey,itemLabel:staged.itemLabel,...readCondition(staged.row)})
    } else {
      const label = staged.label?.trim()
      if (!label || (staged.target==='person' && (!staged.personId || !staged.role))
        || (staged.target!=='person' && !staged.organization) || (staged.target==='pool' && !staged.role)) {
        throw new Error('请填写分项名称，并选好收款对象及适用任职。')
      }
      const item = {label,target:staged.target,role:staged.role,personId:staged.personId,
        personName:staged.personName,organization:staged.organization}
      if (staged.format==='fixed') {
        item.rateBps = props.parseRate(staged.rate)
        if (item.rateBps===undefined) throw new Error('请填写有效比例。')
      } else item.rows = staged.rows.map(readCondition)
      draft.value.items.push(item)
    }
    cancel()
  } catch (err) {
    error.value = err.message
  }
}
watch(form, value => { editing.value = !!value })
</script>

<template>
  <section class="catalog-rule-structure" aria-label="分润分项和费率条件">
    <div v-if="!form" class="topbar-actions">
      <a-button @click="begin('item')">添加分润分项</a-button>
      <a-button v-if="component?.rows?.length" @click="begin('row')">添加费率条件</a-button>
    </div>
    <div v-if="form" class="catalog-add-form">
      <a-alert v-if="error" :message="error" type="error" show-icon class="feedback"/>
      <template v-if="form.kind==='row'">
        <p class="workbench-scope-note">{{ form.itemLabel }}</p>
        <CatalogRateCondition v-model="form.row" :options="options"/>
      </template>
      <template v-else>
        <a-form-item label="分项名称" html-for="catalog-add-label" required>
          <a-input id="catalog-add-label" v-model:value="form.label" :maxlength="80"/>
        </a-form-item>
        <a-form-item label="收款对象" html-for="catalog-add-target">
          <a-select id="catalog-add-target" v-model:value="form.target" :options="targets" @change="changeTarget"/>
        </a-form-item>
        <a-form-item v-if="form.target!=='company'" :label="form.target==='pool'?'分配负责人任职':'计提任职'"
          html-for="catalog-add-role" required>
          <a-select id="catalog-add-role" v-model:value="form.role" :options="roles" show-search option-filter-prop="label"/>
        </a-form-item>
        <a-form-item v-if="form.target==='person'" label="指定收款人" html-for="catalog-add-person" required>
          <AppointmentPersonSelect id="catalog-add-person" v-model:value="form.personId" :api="api" endpoint="/catalog-rule-people"
            :filters="peopleFilters" @change="(_,option)=>form.personName=option.name"/>
        </a-form-item>
        <a-form-item v-else :label="form.target==='pool'?'归集组织':'收款公司'" html-for="catalog-add-organization" required>
          <a-select id="catalog-add-organization" v-model:value="form.organization" :options="organizations"
            show-search option-filter-prop="label" placeholder="选择已完成映射的组织"/>
        </a-form-item>
        <a-form-item label="计提方式" html-for="catalog-add-format">
          <a-select id="catalog-add-format" v-model:value="form.format" :options="formats"/>
        </a-form-item>
        <a-form-item v-if="form.format==='fixed'" label="新增比例（%）" html-for="catalog-add-fixed" required>
          <a-input-number id="catalog-add-fixed" aria-label="新增比例（%）" v-model:value="form.rate"
            :min="0" :max="100" :precision="2" string-mode/>
        </a-form-item>
        <template v-else>
          <div v-for="(_,index) in form.rows" :key="index" class="catalog-add-condition">
            <CatalogRateCondition v-model="form.rows[index]" :options="options" :suffix="'-'+index"/>
            <a-button v-if="form.rows.length>1" type="link" @click="form.rows.splice(index,1)">移除此条件</a-button>
          </div>
          <a-button type="dashed" @click="form.rows.push(blankCondition())">再添一个条件</a-button>
        </template>
      </template>
      <div class="form-actions">
        <a-button @click="cancel">取消添加</a-button>
        <a-button type="primary" @click="stage">{{ form.kind==='item' ? '暂存分项' : '暂存条件' }}</a-button>
      </div>
    </div>
    <ul v-if="draft.items.length || draft.rateRows.length" class="catalog-staged-entries" aria-label="待发布新增配置">
      <li v-for="(item,index) in draft.items" :key="'item-'+index">
        <span>{{ itemLabel(item) }}</span><a-button type="link" size="small" @click="draft.items.splice(index,1)">移除</a-button>
      </li>
      <li v-for="(row,index) in draft.rateRows" :key="'row-'+index">
        <span>{{ row.itemLabel }} · {{ conditionLabel(row) }}</span><a-button type="link" size="small" @click="draft.rateRows.splice(index,1)">移除</a-button>
      </li>
    </ul>
  </section>
</template>

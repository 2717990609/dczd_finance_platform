<script setup>
import { computed, reactive } from 'vue'
import CatalogPeriodTiers from './CatalogPeriodTiers.vue'
import PracticePersonCountFields from './PracticePersonCountFields.vue'
import { practiceSalesSchedule } from './practice-plugin-sales'

const props = defineProps({form:Object,companies:Array,openings:Array,people:Array,prefix:String})
const selections = [
  {value:'performance',label:'本期个人新报净实收'},
  {value:'count',label:'累计新报优先，未达档按本期业绩'},
]
const origins = [
  {value:'ordinal',label:'从指定期次起累计'},
  {value:'time',label:'从指定开班时点起累计'},
  {value:'opening',label:'使用已确认的期初人数'},
]
const formatMoney = minor=>new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'}).format(minor/100)
const personStartModes = origins.filter(option=>option.value!=='opening')
const peopleOptions = computed(() => (props.people || []).map(person=>({...person,label:person.label+' · '+person.value})))
const dateFormat = new Intl.DateTimeFormat('sv-SE',{dateStyle:'short',timeStyle:'short',timeZone:'Asia/Shanghai'})
const open = reactive(new Set([props.form.schedules[0]]))
const id = part=>props.prefix+'-'+part
function addSchedule() {
  const row = practiceSalesSchedule()
  props.form.schedules.push(row)
  open.add(props.form.schedules.at(-1))
}
function toggle(row) {
  if (!open.delete(row)) open.add(row)
}
function scopeLabel(row, index) {
  const names = row.companyIds.map(id=>props.companies.find(company=>company.value===id)?.label || id)
  if (names.length) return names.join('、')
  return index===0 ? '通用档位' : '选择销售公司'
}
</script>

<template>
  <div class="practice-sales-editor">
    <div class="specialty-fields">
      <a-form-item label="选档依据" :html-for="id('metric')">
        <a-select :id="id('metric')" v-model:value="form.metric" :options="selections"/>
      </a-form-item>
      <template v-if="form.metric==='count'">
        <a-form-item label="人数门槛" :html-for="id('threshold')" required>
          <a-input-number :id="id('threshold')" v-model:value="form.threshold" :min="1" :precision="0" addon-after="人"/>
        </a-form-item>
        <a-form-item label="累计来源" :html-for="id('origin')">
          <a-select :id="id('origin')" v-model:value="form.openingMode" :options="origins"/>
        </a-form-item>
        <a-form-item v-if="form.openingMode==='opening'" label="已确认期初" :html-for="id('opening')" required>
          <a-select :id="id('opening')" v-model:value="form.openingId" :options="openings" show-search option-filter-prop="label"/>
        </a-form-item>
        <a-form-item v-else-if="form.openingMode==='time'" label="累计起始时间（北京时间）" :html-for="id('start-time')" required>
          <a-input :id="id('start-time')" type="datetime-local"
            :value="form.fromAt ? dateFormat.format(new Date(form.fromAt)).replace(' ','T') : undefined"
            @update:value="form.fromAt=$event ? $event+':00+08:00' : undefined"/>
        </a-form-item>
        <a-form-item v-else label="累计起始期数" :html-for="id('start-period')" required>
          <a-input-number :id="id('start-period')" v-model:value="form.fromOrdinal" :min="1" :precision="0"/>
        </a-form-item>
      </template>
    </div>
    <p class="specialty-note">{{ form.metric==='count'
      ? '每笔报名先看前序累计人数。未达人数档的报名按个人汇总净实收选业绩档。'
      : '同一销售本期的新报净实收合计选一档，再逐报名计提。' }}</p>
    <details v-if="form.metric==='count'" class="sales-schedule">
      <summary>人员单独起算</summary>
      <div class="schedule-content">
        <div v-for="(start,index) in form.personCountStarts" :key="index" class="specialty-fields">
          <a-form-item label="销售人员" :html-for="id('person-'+index)" required>
            <a-select :id="id('person-'+index)" v-model:value="start.personId" :options="peopleOptions"
              show-search option-filter-prop="label"/>
          </a-form-item>
          <PracticePersonCountFields :start="start" :prefix="id('person-start-'+index)" :start-modes="personStartModes"/>
          <a-button type="link" danger @click="form.personCountStarts.splice(index,1)">移除此起点</a-button>
        </div>
        <a-button size="small" @click="form.personCountStarts.push({startMode:'ordinal'})">添加人员起点</a-button>
      </div>
    </details>
    <section v-for="(row,index) in form.schedules" :key="row" class="sales-schedule">
      <button class="schedule-toggle" type="button" :aria-expanded="open.has(row)" @click="toggle(row)">
        <strong>{{ scopeLabel(row,index) }}</strong>
        <span>{{ row.performance.length }} 档业绩<span v-if="form.metric==='count' && row.countRate!=null"> · 人数档 {{ row.countRate }}%</span></span>
      </button>
      <div v-if="open.has(row)" class="schedule-content">
        <div class="specialty-fields">
          <a-form-item :label="index===0 ? '销售公司（留空为通用）' : '销售公司'" :html-for="id('company-'+index)" :required="index!==0">
            <a-select :id="id('company-'+index)" v-model:value="row.companyIds" :options="companies" mode="multiple"
              show-search option-filter-prop="label" placeholder="选择销售公司"/>
          </a-form-item>
          <a-form-item v-if="form.metric==='count'" label="人数档比例（%）" :html-for="id('count-rate-'+index)" required>
            <a-input-number :id="id('count-rate-'+index)" v-model:value="row.countRate" :min="0" :max="100" :precision="2"/>
          </a-form-item>
        </div>
        <CatalogPeriodTiers :tiers="row.performance" :format-money="formatMoney" :id-prefix="id('performance-'+index)"
          hide-formula @remove="i=>{row.performance.splice(i,1); row.performance[0].from=0}"/>
        <div v-if="form.schedules.length>1" class="schedule-actions">
          <a-button danger type="link" size="small" @click="open.delete(row); form.schedules.splice(index,1)">移除这组档位</a-button>
        </div>
      </div>
    </section>
    <a-button type="dashed" @click="addSchedule">添加公司档位</a-button>
  </div>
</template>

<style scoped>
.sales-schedule > summary {padding:8px 12px;cursor:pointer;color:var(--accent)}
.practice-sales-editor { display:grid; gap:12px; }
.specialty-fields { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,210px),1fr)); gap:0 12px; }
.specialty-fields :deep(.ant-input-number), .specialty-fields :deep(.ant-input-number-group-wrapper) { width:100%; }
.specialty-note { color:var(--muted); margin:0; line-height:1.6; }
.sales-schedule { border:1px solid var(--line); border-radius:8px; min-width:0; }
.schedule-toggle { display:flex; justify-content:space-between; gap:8px; flex-wrap:wrap; width:100%; min-height:40px;
  padding:8px 12px; text-align:left; background:#f7f9fc; color:var(--ink); border:0; border-radius:7px; cursor:pointer; overflow-wrap:anywhere; }
.schedule-toggle[aria-expanded=true] { border-radius:7px 7px 0 0; }
.schedule-toggle span { color:var(--muted); }
.schedule-content { border-top:1px solid var(--line); padding:12px; }
.schedule-actions { text-align:right; }
.practice-sales-editor > .ant-btn { justify-self:start; }
</style>

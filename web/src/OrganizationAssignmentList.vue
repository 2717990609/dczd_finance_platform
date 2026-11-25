<script setup>
import PersonAppointmentActions from './PersonAppointmentActions.vue'

defineProps({rows:Object,loading:Boolean,emptyMessage:String,canWrite:Boolean,page:Number,
 personLink:Function,optionLabel:Function,formatTime:Function})
defineEmits(['page','edit'])
</script>

<template>
 <a-spin :spinning="loading">
  <div v-if="rows.items.length" class="assignment-head" aria-hidden="true">
   <span>人员</span><span>组织</span><span>任职</span><span>任职时间</span><span></span>
  </div>
  <ul v-if="rows.items.length" class="assignment-list" aria-label="任职记录">
   <li v-for="row in rows.items" :key="row.id" class="assignment-row">
    <div class="assignment-person">
     <a class="person-name-link" :href="personLink(row.personId)">{{row.nameNeedsReview ? '姓名待核对' : row.personName}}</a>
     <small>人员 ID {{row.personId}}<template v-if="row.nameNeedsReview"> · {{row.personName}}</template></small>
    </div>
    <div class="assignment-unit" :title="row.unitName">
     {{row.unitName}}<small>{{row.salesCompany ? '销售公司' : (optionLabel('organizationKind',row.unitKind) || row.unitKind)}}</small>
    </div>
    <div class="assignment-role">
     {{optionLabel('appointmentKind',row.kind) || row.kind}}
     <small v-if="row.position">{{row.position}}</small>
     <small v-if="row.appointmentType && row.appointmentType!==row.kind && (!row.salesCompany || row.appointmentType==='CONCURRENT')">
      {{optionLabel('appointmentKind',row.appointmentType)}}
     </small>
    </div>
    <div class="assignment-period">
     <span class="numeric">{{formatTime(row.validFrom)}} 起</span>
     <small v-if="row.validTo" class="numeric">{{formatTime(row.validTo)}} 止</small>
    </div>
    <div v-if="canWrite" class="assignment-actions">
     <PersonAppointmentActions :label="row.personName+'在'+row.unitName" @select="mode=>$emit('edit',mode,row)"/>
    </div>
   </li>
  </ul>
  <a-empty v-else-if="!loading" class="assignment-empty" :description="emptyMessage"/>
  <div v-if="rows.total>25" class="assignment-footer">
   <a-pagination :current="page" :total="rows.total" :page-size="25" :show-size-changer="false" @change="$emit('page',$event)"/>
  </div>
 </a-spin>
</template>

<style scoped>
.assignment-list {list-style:none;margin:0;padding:0 16px}
.assignment-head,.assignment-row {display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1.35fr) minmax(0,1fr) minmax(0,1.25fr) 94px;align-items:center;gap:10px}
.assignment-head {padding:9px 16px;background:var(--surface,#f5f7fb);border-block:1px solid var(--line);color:var(--muted);font-size:13px;font-weight:650}
.assignment-row {padding:10px 0;min-height:55px;border-bottom:1px solid var(--line);overflow-wrap:anywhere}
.assignment-row:last-child {border-bottom:0}
.assignment-row small {display:block;color:var(--muted);font-size:12px;line-height:1.5}
.assignment-actions {justify-self:end}
.assignment-footer {display:flex;justify-content:flex-end;padding:10px 16px 14px}
.assignment-empty {margin-block:30px}
@media(max-width:1000px){
 .assignment-head,.assignment-row {grid-template-columns:minmax(0,.95fr) minmax(0,1.25fr) minmax(0,.9fr) minmax(0,1.15fr) 84px;gap:6px}
}
@media(max-width:820px){
 .assignment-head {display:none}
 .assignment-list {padding-inline:12px}
 .assignment-row {grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:6px 10px}
 .assignment-person {grid-column:1/3}
 .assignment-period {grid-column:1;align-self:end}
 .assignment-actions {grid-column:2;align-self:end}
 .assignment-actions :deep(.ant-btn) {min-height:32px}
 .assignment-row:first-child {border-top:1px solid var(--line)}
}
</style>

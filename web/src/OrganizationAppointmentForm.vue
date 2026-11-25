<script setup>
import { computed, ref } from 'vue'
import { TreeSelect } from 'ant-design-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'

const props = defineProps({api:Function, mode:String, record:Object, tree:Array, kinds:Array, unitId:String, fixedPerson:Object})
const emit = defineEmits(['close','saved'])
const personId = ref(props.fixedPerson?.id)
const target = ref(props.mode==='add' ? props.unitId : undefined)
const kind = ref(props.mode==='transfer' ? props.record.kind : undefined)
const position = ref(props.record?.position || '')
const effective = ref('')
const through = ref('')
const saving = ref(false)
const error = ref('')
const titles = {add:'登记任职',transfer:'办理调动',end:'结束任职'}
const submitLabels = {add:'保存任职',transfer:'确认调动',end:'确认结束'}
const needsTarget = computed(() => props.mode!=='end')
const ready = computed(() => !!effective.value && (!needsTarget.value || !!target.value && !!kind.value) &&
  (props.mode!=='add' || !!personId.value && (!through.value || through.value>effective.value)))
const path = computed(() => '/organization-assignments'+(props.mode==='add' ? '' : '/'+props.mode))
const beijingTime = value => value ? (value.length===16 ? value+':00' : value)+'+08:00' : null
async function save() {
  if (saving.value) return
  error.value = ''
  if (!ready.value) {
    error.value = '请填写必填项。'
    return
  }
  const at = beijingTime(effective.value)
  const payload = props.mode==='add'
    ? {personId:personId.value,startsAt:at,throughAt:beijingTime(through.value)}
    : {assignmentId:props.record.id,at,expectedThroughAt:props.record.validTo || null}
  if (needsTarget.value) Object.assign(payload,{unitId:target.value,kind:kind.value,position:position.value.trim()})
  saving.value = true
  try {
    await props.api(path.value,{method:'POST',body:JSON.stringify(payload)})
    emit('saved',titles[props.mode]+'已保存。')
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer :title="titles[mode]" :open="true" :width="'min(440px, 100vw)'" :closable="!saving"
    :keyboard="!saving" :mask-closable="!saving" @close="emit('close')">
    <a-form layout="vertical" @submit.prevent="save">
      <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
      <div v-if="mode==='add' && fixedPerson" class="appointment-subject"><span>人员</span><strong>{{fixedPerson.name}}</strong></div>
      <a-form-item v-else-if="mode==='add'" label="人员" html-for="appointment-person" required>
        <AppointmentPersonSelect id="appointment-person" v-model:value="personId" :api="api"/>
      </a-form-item>
      <div v-else class="appointment-subject">
        <strong>{{record.personName}}</strong><span>{{record.unitName}}<template v-if="record.position"> · {{record.position}}</template></span>
      </div>
      <template v-if="needsTarget">
        <a-form-item :label="mode==='transfer'?'调入组织':'任职组织'" html-for="appointment-unit" required>
          <TreeSelect id="appointment-unit" v-model:value="target" :tree-data="tree"
            tree-node-filter-prop="title" show-search placeholder="选择组织"/>
        </a-form-item>
        <a-form-item label="任职类别" html-for="appointment-kind" required>
          <a-select id="appointment-kind" v-model:value="kind" :options="kinds" placeholder="选择类别"/>
        </a-form-item>
        <a-form-item label="岗位名称" html-for="appointment-position">
          <a-input id="appointment-position" v-model:value="position" :maxlength="100"/>
        </a-form-item>
      </template>
      <a-form-item :label="mode==='end'?'结束时间（北京时间）':'生效时间（北京时间）'" html-for="appointment-effective" required>
        <a-input id="appointment-effective" v-model:value="effective" type="datetime-local" required/>
      </a-form-item>
      <a-form-item v-if="mode==='add'" label="结束时间（北京时间）" html-for="appointment-end-time" extra="长期任职可留空。">
        <a-input id="appointment-end-time" v-model:value="through" type="datetime-local"/>
        <small v-if="effective && through && through<=effective" class="appointment-invalid">结束时间须晚于生效时间。</small>
      </a-form-item>
      <div class="appointment-form-footer">
        <a-button :disabled="saving" @click="emit('close')">取消</a-button>
        <a-button type="primary" :danger="mode==='end'" html-type="submit" :disabled="!ready" :loading="saving">{{submitLabels[mode]}}</a-button>
      </div>
    </a-form>
  </a-drawer>
</template>

<style scoped>
.appointment-subject {display:grid;gap:3px;margin-bottom:16px;padding:10px 12px;border-radius:6px;background:var(--accent-soft)}
.appointment-subject span {color:var(--muted)}
.appointment-form-footer {display:flex;justify-content:flex-end;gap:8px;padding-top:14px;border-top:1px solid var(--line)}
.appointment-invalid {color:#b42318}
</style>

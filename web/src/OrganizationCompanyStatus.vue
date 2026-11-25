<script setup>
import { computed, ref } from 'vue'

const props = defineProps({api:Function,company:Object})
const emit = defineEmits(['close','saved'])
const saving = ref(false)
const error = ref('')
const activating = computed(() => props.company.status==='INACTIVE')
const status = computed(() => activating.value ? 'ACTIVE' : 'INACTIVE')

async function save() {
  if (saving.value) return
  saving.value = true
  error.value = ''
  const {id, status:expectedStatus} = props.company
  const nextStatus = status.value
  try {
    const {profile,wireId} = await props.api('/organization-dispatch?'+new URLSearchParams({companyId:id}))
    if (!profile || !wireId) throw new Error('请先核对该公司的财务主体编号。')
    if (profile.status!==expectedStatus) throw new Error('公司状态已变化，请刷新后重试。')
    await props.api('/organization-dispatch/profile',{method:'POST',body:JSON.stringify({
      id,name:profile.name,parentId:profile.parentId,status:nextStatus,expectedVersion:profile.version,
    })})
    emit('saved',id,nextStatus)
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-modal open :title="activating ? '启用公司' : '停用公司'" :ok-text="activating ? '确认启用' : '确认停用'"
    cancel-text="取消" :ok-button-props="{danger:!activating}" :confirm-loading="saving"
    :closable="!saving" :keyboard="!saving" :mask-closable="false" @ok="save" @cancel="!saving && emit('close')">
    <p><strong>{{company.name}}</strong></p>
    <p v-if="activating">启用后可重新登记公司成员。</p>
    <p v-else>停用前请交接有效的人员归属、下级公司和销售身份。</p>
    <a-alert v-if="error" type="error" :message="error" show-icon/>
  </a-modal>
</template>

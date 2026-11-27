<script setup>
import {computed, onMounted, ref} from 'vue'
import {isCalendarOnlyRule, isClassOnlyRule} from './common-rule-form'

const props = defineProps({period:Object,api:Function})
const emit = defineEmits(['close','saved'])
const rules = ref([])
const ruleId = ref()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const compatible = computed(()=>rules.value.filter(rule=>props.period.periodMode==='class' ? !isCalendarOnlyRule(rule) : !isClassOnlyRule(rule)))
const choices = computed(()=>compatible.value.map(rule=>({value:rule.id,label:rule.label})))
const chosen = computed(()=>compatible.value.find(rule=>rule.id===ruleId.value))

onMounted(async ()=>{
  try {
    rules.value = await props.api('/rule-plugins')
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
})

async function attach() {
  if (!chosen.value || saving.value) return
  error.value = ''
  saving.value = true
  try {
    await props.api('/period-rule-plugin',{method:'POST',body:JSON.stringify({periodId:props.period.id,ruleVersionId:ruleId.value})})
    emit('saved')
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-modal open title="关联分润规则" ok-text="关联" cancel-text="取消" :confirm-loading="saving"
    :ok-button-props="{disabled:!chosen}" :mask-closable="!saving" :closable="!saving" @ok="attach" @cancel="!saving && emit('close')">
    <p>{{ period.name }} · {{ period.startsOn }} 至 {{ period.endsOn }}</p>
    <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
    <a-form layout="vertical">
      <a-form-item label="分润规则" html-for="period-rule-plugin" required extra="关联后锁定本期规则。">
        <a-select id="period-rule-plugin" v-model:value="ruleId" show-search option-filter-prop="label"
          :options="choices" :loading="loading" :disabled="loading || saving" placeholder="选择规则"/>
      </a-form-item>
      <div v-if="chosen" class="period-rule-items">
        <strong>计提项</strong>
        <ul><li v-for="item in chosen.arrangements" :key="item.key">{{ item.label }}</li></ul>
      </div>
    </a-form>
    <a v-if="!loading && !compatible.length && !error" href="#rules" @click="emit('close')">去配置分润规则</a>
  </a-modal>
</template>

<style scoped>
.period-rule-items { padding:12px 16px; border:1px solid var(--line); border-radius:8px; background:#f7f9fc; }
.period-rule-items ul { margin:6px 0 0; padding-inline-start:20px; }
</style>

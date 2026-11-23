<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { moneyAsMinor } from './rule-component-form'

const props = defineProps({ api:Function, month:String, unitId:String, units:Array, formatMoney:Function, formatTime:Function })
const report = ref()
const accountId = ref()
const page = ref(1)
const loading = ref(false)
const error = ref('')
const entryForm = ref()
const formError = ref('')
const saving = ref(false)
const notice = ref('')
let sequence = 0

const account = computed(() => report.value?.items.find(item=>item.unitId===accountId.value))
const pageSize = 25
const columns = [
  {title:'归集组织',dataIndex:'scopeName',key:'organization',width:260},
  {title:'期初余额',dataIndex:'openingMinor',align:'right',width:120},
  {title:'本月结算留存',dataIndex:'incomeMinor',align:'right',width:140},
  {title:'支出净额',dataIndex:'expenseMinor',align:'right',width:120},
  {title:'本月更正',dataIndex:'correctionMinor',align:'right',width:120},
  {title:'可用余额',dataIndex:'balanceMinor',key:'balance',align:'right',width:120},
  {title:'操作',key:'action',width:100},
]
const entryColumns = [
  {title:'归属月',dataIndex:'month',width:100},
  {title:'类型',dataIndex:'kind',key:'kind',width:115},
  {title:'明细',key:'details',width:240},
  {title:'登记时间',dataIndex:'confirmedAt',key:'confirmed',width:145},
  {title:'变动金额',dataIndex:'amountMinor',key:'amount',align:'right',width:120},
  {title:'操作',key:'action',width:75},
]
const entryKinds = [{value:'expense',label:'登记支出'},{value:'opening',label:'历史期初'},{value:'correction',label:'余额更正'}]
const kindLabels = {income:'结算留存',expense:'支出',opening:'历史期初',correction:'更正',reversal:'冲销'}
const unitChoices = computed(() => {
  const units = props.units || []
  const names = new Map(units.map(unit=>[unit.id,unit.name]))
  return units.filter(unit=>unit.status==='ACTIVE' || unit.id===entryForm.value?.unitId).map(unit=>({
    value:unit.id,label:[names.get(unit.parentId),unit.name].filter(Boolean).join(' / '),
  }))
})

async function load() {
  const request = ++sequence
  error.value = ''
  if (!props.month) {
    report.value = undefined
    loading.value = false
    return
  }
  loading.value = true
  try {
    const query = new URLSearchParams({month:props.month})
    if (props.unitId) query.set('unitId',props.unitId)
    if (accountId.value) query.set('accountId',accountId.value)
    if (page.value>1) query.set('page',page.value)
    const result = await props.api('/department-retention?'+query)
    if (request===sequence) report.value = result
  } catch (err) {
    if (request===sequence) error.value = err.message
  } finally {
    if (request===sequence) loading.value = false
  }
}

function openAccount(id) {
  page.value = 1
  accountId.value = id
}
function startEntry(original) {
  formError.value = ''
  notice.value = ''
  entryForm.value = {
    unitId:accountId.value || props.unitId, month:props.month,
    kind:original ? 'reversal' : 'expense', originalId:original?.manualId,
    requestId:crypto.randomUUID(),
  }
}
async function saveEntry() {
  const form = entryForm.value
  if (!form || saving.value) return
  const negative = String(form.amount || '').startsWith('-')
  const minor = form.kind==='reversal' ? 0 : moneyAsMinor(String(form.amount ?? '').replace(/^-/,''))
  if (!form.month || !form.unitId || minor==null || form.kind==='expense' && (negative || minor===0 || !form.reimbursementNo?.trim()) ||
    form.kind==='correction' && (!form.note?.trim() || minor===0)) {
    formError.value='请核对归集组织、结算月及金额，支出填写报销单号，更正填写说明。'
    return
  }
  const {unitId,month,kind,note,reimbursementNo,originalId,requestId} = form
  saving.value = true
  formError.value = ''
  try {
    await props.api('/department-retention',{method:'POST',body:JSON.stringify({
      unitId,month,kind,note,requestId,amountMinor:negative ? -minor : minor,
      ...(kind==='expense' ? {reimbursementNo} : {}),...(kind==='reversal' ? {originalId} : {}),
    })})
    entryForm.value = undefined
    notice.value = '留存变动已登记'
    await load()
  } catch (err) {
    formError.value=err.message
  } finally {
    saving.value=false
  }
}
watch(() => [props.month, props.unitId], () => {
  accountId.value = undefined
  page.value = 1
}, {flush:'sync'})
watch(() => [props.month, props.unitId, accountId.value, page.value], load, {immediate:true})
onBeforeUnmount(() => { sequence++ })
defineExpose({refresh:load})
</script>

<template>
  <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
  <a-alert v-if="notice" class="feedback" type="success" :message="notice" show-icon/>
  <section class="records" aria-label="部门留存余额">
    <div class="records-head">
      <h2>部门余额</h2>
      <a-button v-if="report?.canRecord" type="primary" :disabled="!month" @click="startEntry()">登记收支</a-button>
    </div>
    <a-table :columns="columns" :data-source="report?.items || []" row-key="unitId"
      :loading="loading && !accountId" :pagination="false" :scroll="{x:925}">
      <template #bodyCell="{column,record,text}">
        <span v-if="column.key==='organization'">{{ text }}</span>
        <a-button v-else-if="column.key==='action'" size="small" type="link" @click="openAccount(record.unitId)">查看明细</a-button>
        <strong v-else-if="column.key==='balance'" class="numeric">{{ formatMoney(text) }}</strong>
        <span v-else class="numeric">{{ formatMoney(text) }}</span>
      </template>
      <template #emptyText>{{ month ? '截至本月暂无留存记录' : '请选择结算月' }}</template>
    </a-table>
  </section>
  <a-drawer :open="Boolean(account)" :title="account ? account.scopeName+' · 留存明细' : '留存明细'"
    :width="'min(830px, 100vw)'" @close="openAccount(undefined)">
    <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
    <a-table :columns="entryColumns" :data-source="report?.entries || []" row-key="id"
      :loading="loading" :scroll="{x:795}"
      :pagination="{current:page,total:report?.totalEntries || 0,pageSize,showSizeChanger:false}"
      @change="pagination=>page=pagination.current">
      <template #bodyCell="{column,record,text}">
        <span v-if="column.key==='kind'">{{ kindLabels[text] }}</span>
        <template v-else-if="column.key==='details'">
          <template v-if="record.periodId">
            <a class="order-link" :href="'#settlement?periodId='+record.periodId+'&view=pools&from=retention'">{{ record.periodName }}</a>
            <small class="order-version">{{ record.productName }}</small>
          </template>
          <span v-else>{{ [record.reimbursementNo,record.note].filter(Boolean).join(' · ') || '—' }}</span>
        </template>
        <span v-else-if="column.key==='confirmed'">{{ formatTime(text) }}</span>
        <span v-else-if="column.key==='amount'" class="numeric">{{ formatMoney(text) }}</span>
        <a-button v-else-if="column.key==='action' && report?.canRecord && record.reversible" type="link" size="small" @click="startEntry(record)">冲销</a-button>
      </template>
    </a-table>
  </a-drawer>
  <a-modal :open="Boolean(entryForm)" :title="entryForm?.kind==='reversal' ? '冲销留存记录' : '登记留存收支'"
    :confirm-loading="saving" :mask-closable="!saving" ok-text="保存" @ok="saveEntry" @cancel="!saving && (entryForm=undefined)">
    <a-alert v-if="formError" class="feedback" type="error" :message="formError" show-icon/>
    <a-form v-if="entryForm" layout="vertical" :disabled="saving">
      <a-form-item label="归集组织" html-for="retention-unit" required>
        <a-select id="retention-unit" v-model:value="entryForm.unitId" :options="unitChoices" :disabled="entryForm.kind==='reversal'" show-search option-filter-prop="label"/>
      </a-form-item>
      <a-form-item v-if="entryForm.kind!=='reversal'" label="处理方式" html-for="retention-kind" required>
        <a-select id="retention-kind" v-model:value="entryForm.kind" :options="entryKinds"/>
      </a-form-item>
      <a-form-item :label="entryForm.kind==='opening' ? '期初覆盖至' : '归属月'" html-for="retention-month"
        :extra="entryForm.kind==='opening' ? '截至该月的结算留存计入期初，台账已登记的收支另计。' : undefined" required>
        <a-input id="retention-month" v-model:value="entryForm.month" type="month"/>
      </a-form-item>
      <a-form-item v-if="entryForm.kind!=='reversal'" label="金额（元）" html-for="retention-amount" required>
        <a-input-number id="retention-amount" v-model:value="entryForm.amount" string-mode :precision="2" :min="entryForm.kind==='expense' ? 0 : undefined"/>
      </a-form-item>
      <a-form-item v-if="entryForm.kind==='expense'" label="报销单号" html-for="retention-reimbursement" required>
        <a-input id="retention-reimbursement" v-model:value="entryForm.reimbursementNo" :maxlength="80"/>
      </a-form-item>
      <a-form-item label="说明" html-for="retention-note" :required="entryForm.kind==='correction'">
        <a-input id="retention-note" v-model:value="entryForm.note" :maxlength="200"/>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

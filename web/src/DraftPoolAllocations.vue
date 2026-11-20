<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({ draft:Object, api:Function, formatMoney:Function })
const report = ref()
const selected = ref()
const rows = ref([])
const retained = ref()
const error = ref('')
const notice = ref('')
const loading = ref(false)
const saving = ref(false)
let sequence = 0

const key = row => [row.unitId,row.scenario,row.itemKey].join('/')
const options = computed(() => (report.value?.items || []).map(pool => ({value:key(pool),label:pool.name})))
const pool = computed(() => report.value?.items.find(item => key(item)===selected.value))
const memberOptions = computed(() => (pool.value?.members || []).map(member => ({
  value:member.personId,
  label:member.availableMinor>0 ? member.name : member.name+' · 资格待核',
  disabled:member.availableMinor===0,
})))
const editable = computed(() => !props.draft.settlement && !report.value?.closed && !saving.value && !loading.value)
const moneyInput = amount => (amount/100).toFixed(2)
const toMinor = value => {
  if (value==='' || value==null) return 0
  const text = String(value)
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return NaN
  const [whole, fraction=''] = text.split('.')
  return Number(whole)*100+Number(fraction.padEnd(2,'0'))
}
const amountTotal = computed(() => rows.value.reduce((sum,row) => sum+toMinor(row.amount),toMinor(retained.value)))

async function load() {
  const request = ++sequence
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams({periodId:props.draft.periodId,version:props.draft.version})
    const result = await props.api('/draft-pools?'+params)
    if (request!==sequence) return
    report.value = result
    if (!options.value.some(option => option.value===selected.value)) selected.value=options.value[0]?.value
  } catch (err) {
    if (request===sequence) {
      report.value=undefined
      error.value=err.message
    }
  } finally {
    if (request===sequence) loading.value=false
  }
}
watch(pool, item => {
  const shares = item?.shares || []
  rows.value = shares.filter(row=>row.personId>0).map(row=>({personId:row.personId,amount:moneyInput(row.amountMinor)}))
  retained.value = shares.some(row=>row.personId===0) ? moneyInput(shares.find(row=>row.personId===0).amountMinor) : undefined
})
watch(() => [props.draft.periodId,props.draft.version,props.draft.settlement?.confirmedAt], () => { notice.value=''; load() }, {immediate:true})
onBeforeUnmount(() => { sequence++ })

async function save() {
  const current = pool.value
  if (!current || !editable.value) return
  const shares = rows.value.map(row => ({personId:row.personId,amountMinor:toMinor(row.amount)}))
  const retainedMinor=toMinor(retained.value)
  if (!Number.isSafeInteger(retainedMinor) || shares.some(share=>share.personId==null || !Number.isSafeInteger(share.amountMinor) || share.amountMinor<=0)) {
    error.value='请选择人员并填写大于零的金额。'
    return
  }
  const request = sequence
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await props.api('/draft-pool-allocations',{method:'POST',body:JSON.stringify({
      periodId:props.draft.periodId,version:props.draft.version,revision:current.revision,basis:report.value.basis,
      unitId:current.unitId,itemKey:current.itemKey,scenario:current.scenario,shares,retainedMinor,
    })})
    if (request===sequence) {
      notice.value = '分配草稿已保存'
      await load()
    }
  } catch (err) {
    if (request===sequence) error.value=err.message
  } finally {
    saving.value=false
  }
}
</script>

<template>
  <section class="records draft-pools">
    <div class="records-head">
      <h2>团队池分配</h2>
      <a-button :loading="loading" :disabled="saving" @click="load">刷新余额</a-button>
    </div>
    <div class="pool-form">
      <a-alert v-if="error" type="error" :message="error" show-icon/>
      <a-alert v-if="notice" type="success" :message="notice" show-icon/>
      <a-empty v-if="!loading && !error && !pool" description="本期没有团队池余额或流水"/>
      <template v-if="pool">
        <div class="pool-control">
          <label for="draft-pool-select">团队池</label>
          <a-select id="draft-pool-select" v-model:value="selected" :options="options" :disabled="saving"/>
        </div>
        <dl class="pool-balances">
          <div><dt>期初余额</dt><dd>{{ formatMoney(pool.openingMinor) }}</dd></div>
          <div><dt>本期归集</dt><dd>{{ formatMoney(pool.incomeMinor) }}</dd></div>
          <div><dt>本期退款</dt><dd>{{ formatMoney(pool.refundMinor) }}</dd></div>
          <div v-if="pool.correctionMinor"><dt>本期更正</dt><dd>{{ formatMoney(pool.correctionMinor) }}</dd></div>
          <div v-if="report.closed"><dt>确认分配</dt><dd>{{ formatMoney(pool.confirmedMinor) }}</dd></div>
          <div><dt>{{ report.closed ? '期末余额' : '池余额' }}</dt><dd>{{ formatMoney(pool.balanceMinor) }}</dd></div>
        </dl>
        <a-alert v-if="pool.revision && !pool.planCurrent" type="warning" show-icon
          message="试算已变化，请核对已填金额后重新保存。"/>
        <p v-if="pool.balanceMinor<0" class="draft-report-note">后续归集优先弥补负余额。</p>
        <div v-for="(row,index) in rows" :key="index" class="pool-share">
          <label class="sr-only" :for="'pool-member-'+index">分配人员 {{ index+1 }}</label>
          <a-select :id="'pool-member-'+index" v-model:value="row.personId" :options="memberOptions"
            show-search option-filter-prop="label" placeholder="选择成员" :disabled="!editable"/>
          <label class="sr-only" :for="'pool-amount-'+index">分配金额 {{ index+1 }}</label>
          <a-input-number :id="'pool-amount-'+index" v-model:value="row.amount" string-mode :min="0" :precision="2"
            placeholder="金额（元）" :disabled="!editable"/>
          <a-button v-if="!report.closed" type="text" :disabled="!editable" @click="rows.splice(index,1)">移除</a-button>
        </div>
        <a-button v-if="!report.closed && memberOptions.some(item=>!item.disabled)" :disabled="!editable" @click="rows.push({})">添加成员</a-button>
        <div class="pool-control">
          <label for="pool-retention">留存金额</label>
          <a-input-number id="pool-retention" v-model:value="retained" string-mode :min="0" :precision="2"
            placeholder="金额（元）" :disabled="!editable"/>
        </div>
        <div v-if="!report.closed" class="form-actions">
          <span>拟分配 {{ formatMoney(amountTotal) }}</span>
          <a-button type="primary" :loading="saving" :disabled="!editable" @click="save">保存分配草稿</a-button>
        </div>
      </template>
    </div>
  </section>
</template>

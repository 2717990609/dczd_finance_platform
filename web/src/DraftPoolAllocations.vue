<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { teamPoolKey } from './team-pool-key'

const props = defineProps({ draft:Object, api:Function, formatMoney:Function, requestedPool:String })
const report = ref()
const selected = ref()
const rows = ref([])
const retained = ref()
const error = ref('')
const notice = ref('')
const loading = ref(false)
const saving = ref(false)
let sequence = 0

const options = computed(() => (report.value?.items || []).map(pool => ({
  value:teamPoolKey(pool),label:[pool.name,pool.unitName].filter(Boolean).join(' · '),
})))
const pool = computed(() => report.value?.items.find(item => teamPoolKey(item)===selected.value))
const membersById = computed(() => new Map((pool.value?.members || []).map(member=>[member.personId,member])))
const confirmedRows = computed(() => (pool.value?.shares || []).map(share => ({
  ...share,name:share.personId ? membersById.value.get(share.personId)?.name || '人员 '+share.personId : '组织留存',
})))
const confirmedColumns = [
  {title:'分配对象',dataIndex:'name',width:200},
  {title:'确认金额',dataIndex:'amountMinor',key:'amount',align:'right',width:140},
]
const missingRequestedPool = computed(() => Boolean(report.value && props.requestedPool &&
  !options.value.some(option=>option.value===props.requestedPool)))
function choosePool(preferred = selected.value || props.requestedPool) {
  selected.value = options.value.some(option=>option.value===preferred) ? preferred : options.value[0]?.value
}
const memberOptions = computed(() => (pool.value?.members || []).map(member => ({
  value:member.personId,
  label:[member.name,member.personId,report.value.closed ? '' : member.availableMinor>0
    ? '可分配 '+props.formatMoney(member.availableMinor) : '当前不可分'].filter(Boolean).join(' · '),
  disabled:!report.value.closed && member.availableMinor===0,
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
const memberTotal = computed(() => rows.value.reduce((sum,row) => sum+toMinor(row.amount),0))
const remaining = computed(() => Math.max(0,pool.value?.balanceMinor || 0)-memberTotal.value-toMinor(retained.value))
const memberOverLimit = row => row.personId && toMinor(row.amount)>(membersById.value.get(row.personId)?.availableMinor || 0)
const overMemberLimit = computed(() => rows.value.some(memberOverLimit))

async function load() {
  const request = ++sequence
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams({periodId:props.draft.periodId,version:props.draft.version})
    const result = await props.api('/draft-pools?'+params)
    if (request!==sequence) return
    report.value = result
    choosePool()
  } catch (err) {
    if (request===sequence) {
      report.value=undefined
      error.value=err.message
    }
  } finally {
    if (request===sequence) loading.value=false
  }
}
watch(() => props.requestedPool, value => choosePool(value))
watch(pool, item => {
  const shares = item?.shares || []
  rows.value = shares.filter(row=>row.personId>0).map(row=>({personId:row.personId,amount:moneyInput(row.amountMinor)}))
  retained.value = shares.some(row=>row.personId===0) ? moneyInput(shares.find(row=>row.personId===0).amountMinor) : undefined
})
watch(() => [props.draft.periodId,props.draft.version,props.draft.settlement?.confirmedAt], () => { notice.value=''; load() }, {immediate:true})
onBeforeUnmount(() => { sequence++ })

async function save() {
  const current = pool.value
  if (!current || !editable.value || remaining.value<0 || overMemberLimit.value) return
  const shares = rows.value.map(row => ({personId:row.personId,amountMinor:toMinor(row.amount)}))
  const retainedMinor=toMinor(retained.value)
  if (!Number.isSafeInteger(retainedMinor) || shares.some(share=>share.personId==null || !Number.isSafeInteger(share.amountMinor) || share.amountMinor<=0)) {
    error.value='请选择人员并填写大于零的金额。'
    return
  }
  if (new Set(shares.map(share=>share.personId)).size!==shares.length) {
    error.value='同一成员只填写一行。'
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
      <a-alert v-if="missingRequestedPool" type="warning" message="本期未找到指定团队池，请核对池名称。" show-icon/>
      <a-empty v-if="!loading && !error && !pool" description="本期没有团队池余额或流水"/>
      <template v-if="pool">
        <div class="pool-control">
          <label for="draft-pool-select">团队池</label>
          <a-select id="draft-pool-select" v-model:value="selected" :options="options" :disabled="saving"/>
        </div>
        <dl class="pool-balances">
          <div><dt>期初余额</dt><dd>{{ formatMoney(pool.openingMinor) }}</dd></div>
          <div><dt>本期归集</dt><dd>{{ formatMoney(pool.incomeMinor) }}</dd></div>
          <div><dt>{{ draft.practiceRefundMonth ? '退款调整' : '本期退款' }}</dt><dd>{{ formatMoney(pool.refundMinor) }}</dd></div>
          <div v-if="pool.correctionMinor"><dt>本期更正</dt><dd>{{ formatMoney(pool.correctionMinor) }}</dd></div>
          <div v-if="report.closed"><dt>确认分配</dt><dd>{{ formatMoney(pool.confirmedMinor) }}</dd></div>
          <div><dt>{{ report.closed ? '期末余额' : '池余额' }}</dt><dd>{{ formatMoney(pool.balanceMinor) }}</dd></div>
        </dl>
        <div v-if="pool.owners?.length" class="pool-owners">
          <span>本期归集负责人</span>
          <span v-for="owner in pool.owners" :key="owner.personId">
            {{ owner.name || ('人员 '+owner.personId) }}<template v-if="pool.owners.length>1"> · {{ formatMoney(owner.incomeMinor) }}</template>
          </span>
        </div>
        <a-alert v-if="!report.closed && pool.revision && !pool.planCurrent" type="warning" show-icon
          message="试算已变化，请核对已填金额后重新保存。"/>
        <p v-if="pool.balanceMinor<0" class="draft-report-note">后续归集优先弥补负余额。</p>
        <section v-if="report.closed" aria-label="已确认分配">
          <a-table :data-source="confirmedRows" :columns="confirmedColumns" row-key="personId" size="small"
            :pagination="{pageSize:15,showSizeChanger:false,hideOnSinglePage:true}">
            <template #bodyCell="{column,record}">
              <span v-if="column.key==='amount'" class="numeric">{{ formatMoney(record.amountMinor) }}</span>
            </template>
            <template #emptyText>本期无确认分配</template>
          </a-table>
        </section>
        <template v-else>
          <div v-for="(row,index) in rows" :key="index" class="pool-share">
            <label class="sr-only" :for="'pool-member-'+index">分配人员 {{ index+1 }}</label>
            <a-select :id="'pool-member-'+index" v-model:value="row.personId" :options="memberOptions"
              show-search option-filter-prop="label" placeholder="选择成员" :disabled="!editable"/>
            <label class="sr-only" :for="'pool-amount-'+index">分配金额 {{ index+1 }}</label>
            <a-input-number :id="'pool-amount-'+index" v-model:value="row.amount" string-mode :min="0" :precision="2"
              placeholder="金额（元）" :status="memberOverLimit(row) ? 'error' : undefined" :disabled="!editable"/>
            <a-button type="text" :disabled="!editable" @click="rows.splice(index,1)">移除</a-button>
          </div>
          <a-button v-if="memberOptions.some(item=>!item.disabled)" :disabled="!editable" @click="rows.push({})">添加成员</a-button>
          <div class="pool-control">
            <label for="pool-retention">留存金额</label>
            <a-input-number id="pool-retention" v-model:value="retained" string-mode :min="0" :precision="2"
              placeholder="金额（元）" :disabled="!editable"/>
          </div>
          <div class="form-actions">
            <span>拟分配 {{ formatMoney(memberTotal) }}</span>
            <span>拟留存 {{ formatMoney(toMinor(retained)) }}</span>
            <strong v-if="!Number.isNaN(remaining)" :class="{'pool-over-limit':remaining<0}" aria-live="polite">
              {{ remaining<0 ? '超出池余额' : '待分配' }} {{ formatMoney(Math.abs(remaining)) }}
            </strong>
            <span v-if="overMemberLimit" role="alert" class="pool-over-limit">有成员超过可分配金额</span>
            <a-button type="primary" :loading="saving" :disabled="!editable || remaining<0 || overMemberLimit" @click="save">保存分配草稿</a-button>
          </div>
        </template>
      </template>
    </div>
  </section>
</template>

<script setup>
import { onBeforeUnmount, ref, watch } from 'vue'

const props = defineProps({ api:Function, productId:Number, unitId:String, directoryScope:String, formatMoney:Function,
  formatTime:Function, periodLink:Function })
const accounts = ref([])
const account = ref()
const period = ref()
const report = ref()
const page = ref(1)
const loading = ref(false)
const drawerLoading = ref(false)
const error = ref('')
const drawerError = ref('')
const pageSize = 25
let accountRequest = 0
let drawerRequest = 0

const amount = (title, dataIndex, width=135) => ({title,dataIndex,key:dataIndex,width,align:'right'})
const accountColumns = [
  {title:'团队池',key:'account',width:215},
  {title:'团队池负责人',dataIndex:'ownerName',key:'owner',width:135},
  amount('当前余额','balanceMinor'),amount('历史总收入','totalIncomeMinor'),amount('历史总支出','totalSpentMinor'),
]
const periodColumns = [
  {title:'业务期次',dataIndex:'periodName',key:'period',width:200},
  {title:'归属月',dataIndex:'month',width:110},
  amount('本期收入','totalIncomeMinor'),amount('本期支出','totalSpentMinor'),amount('本期净变动','netMinor'),
  {title:'操作',key:'periodAction',width:90},
]
const entryColumns = [
  {title:'类型',dataIndex:'kind',key:'kind',width:130},
  {title:'业务明细',key:'reference',width:215},
  {title:'入账时间',dataIndex:'confirmedAt',key:'time',width:160},
  amount('变动金额','amountMinor'),
]
const kindLabels = {income:'规则归集',refund:'退款扣回',correction:'计提更正',distribution:'分配到人',retention:'转入部门留存金'}

function ledgerQuery() {
  const query = new URLSearchParams()
  if (props.productId) query.set('productId',props.productId)
  if (props.unitId) query.set('unitId',props.unitId)
  if (props.directoryScope) query.set('scope',props.directoryScope)
  return query
}
async function loadAccounts() {
  const request = ++accountRequest
  loading.value = true
  error.value = ''
  try {
    const query = ledgerQuery()
    const result = await props.api('/team-pool-ledger'+(query.size ? '?'+query : ''))
    if (request===accountRequest) accounts.value = result.items
  } catch (err) {
    if (request===accountRequest) error.value = err.message
  } finally {
    if (request===accountRequest) loading.value = false
  }
}
async function loadDrawer() {
  const request = ++drawerRequest
  report.value = undefined
  drawerError.value = ''
  if (!account.value) {
    drawerLoading.value = false
    return
  }
  drawerLoading.value = true
  const query = ledgerQuery()
  query.set('key',account.value.key)
  query.set('page',page.value)
  if (period.value) query.set('periodId',period.value.periodId)
  try {
    const result = await props.api('/team-pool-ledger?'+query)
    if (request===drawerRequest) report.value = result
  } catch (err) {
    if (request===drawerRequest) drawerError.value = err.message
  } finally {
    if (request===drawerRequest) drawerLoading.value = false
  }
}
function showAccount(row) {
  account.value = row
  showPeriod()
}
function showPeriod(row) {
  period.value = row
  page.value = 1
}
watch(() => [props.productId,props.unitId,props.directoryScope], () => {
  accounts.value = []
  showAccount()
  loadAccounts()
}, {immediate:true})
watch([account,period,page],loadDrawer)
onBeforeUnmount(() => { accountRequest++; drawerRequest++ })
</script>

<template>
  <section class="records" aria-label="团队池台账">
    <div class="records-head">
      <h2>团队池台账</h2>
      <a-button :loading="loading" @click="loadAccounts">刷新台账</a-button>
    </div>
    <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
    <a-table :columns="accountColumns" :data-source="accounts" :loading="loading" row-key="key"
      :pagination="{pageSize:15,hideOnSinglePage:true,showSizeChanger:false}" :scroll="{x:755}">
      <template #bodyCell="{column,record,text}">
        <template v-if="column.key==='account'">
          <a-button type="link" size="small" class="pool-ledger-link" @click="showAccount(record)">{{ record.name }}</a-button>
          <small class="order-version">{{ [record.productName,record.unitName,record.id ? '' : '历史池'].filter(Boolean).join(' · ') }}</small>
        </template>
        <span v-else-if="column.key==='owner'">{{ text || (record.id ? '待指定' : '—') }}</span>
        <span v-else-if="column.align==='right'" class="numeric">{{ formatMoney(text) }}</span>
      </template>
      <template #emptyText>暂无团队池</template>
    </a-table>
  </section>
  <a-drawer :open="Boolean(account)" :title="account ? account.name+' · '+(period?.periodName || '期次流水') : '团队池流水'"
    width="min(860px, 100vw)" @close="showAccount()">
    <a-alert v-if="drawerError" class="feedback" type="error" :message="drawerError" show-icon/>
    <div v-if="period" class="pool-ledger-toolbar topbar-actions">
      <a-button @click="showPeriod()">返回期次流水</a-button>
      <a-button :href="periodLink(period.periodId)">查看本期分润</a-button>
    </div>
    <a-table v-if="period" :columns="entryColumns" :data-source="report?.entries || []" :loading="drawerLoading"
      row-key="id" :scroll="{x:640}" :pagination="{current:page,pageSize,total:report?.total || 0,showSizeChanger:false}"
      @change="pagination=>page=pagination.current">
      <template #bodyCell="{column,record,text}">
        <span v-if="column.key==='kind'">{{ kindLabels[text] }}</span>
        <span v-else-if="column.key==='reference'">{{ record.personName || record.orderId || (record.kind==='retention' ? account.unitName : record.itemKey) }}</span>
        <span v-else-if="column.key==='time'">{{ formatTime(text) }}</span>
        <span v-else-if="column.align==='right'" class="numeric">{{ formatMoney(text) }}</span>
      </template>
    </a-table>
    <a-table v-else :columns="periodColumns" :data-source="report?.periods || []" :loading="drawerLoading" row-key="periodId"
      :scroll="{x:805}" :pagination="{current:page,pageSize,total:report?.total || 0,showSizeChanger:false}"
      @change="pagination=>page=pagination.current">
      <template #bodyCell="{column,record,text}">
        <a-button v-if="column.key==='period'" type="link" size="small" class="pool-ledger-link" @click="showPeriod(record)">{{ text }}</a-button>
        <span v-else-if="column.align==='right'" class="numeric">{{ formatMoney(text) }}</span>
        <a-button v-else-if="column.key==='periodAction'" type="link" size="small" @click="showPeriod(record)">查看明细</a-button>
      </template>
      <template #emptyText>暂无已入账的期次流水</template>
    </a-table>
  </a-drawer>
</template>

<style scoped>
.pool-ledger-link { padding-inline: 0; height: auto; text-align: left; white-space: normal; }
.pool-ledger-toolbar { margin-bottom: 12px; }
</style>

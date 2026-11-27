<script setup>
import { computed, onMounted, ref } from 'vue'
import { DownOutlined, PlusOutlined } from '@ant-design/icons-vue'
import AppointmentPersonSelect from './AppointmentPersonSelect.vue'
import CatalogTeamPoolFields from './CatalogTeamPoolFields.vue'
import RuleArrangementFields from './RuleArrangementFields.vue'
import RuleEditableTable from './RuleEditableTable.vue'
import RuleSalesScopeFields from './RuleSalesScopeFields.vue'
import RuleStandardValue from './RuleStandardValue.vue'
import CatalogPeriodTiers from './CatalogPeriodTiers.vue'
import PracticePluginSalesEditor from './PracticePluginSalesEditor.vue'
import PracticeCompanySalesEditor from './PracticeCompanySalesEditor.vue'
import PracticeRetrainSplitEditor from './PracticeRetrainSplitEditor.vue'
import { isClassOnlyRule, rulePluginPayload, rulePluginRows } from './common-rule-form'
import { practiceSalesForm } from './practice-plugin-sales'
import { catalogFlatMethods, practiceRetrainSplitForm } from './rule-component-form'

const props = defineProps({baseRule:Object,api:Function,people:Array,practiceCompanies:Array,practiceCategories:Array,
  practiceOpenings:Array,practiceContext:Boolean})
const emit = defineEmits(['close','saved'])
const name = ref(props.baseRule?.label || '')
const options = ref()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const rows = ref(rulePluginRows(props.baseRule?.arrangements || [],props.practiceCompanies))
const active = ref()
const recipients = [{value:'person',label:'个人'},{value:'company',label:'公司'},{value:'pool',label:'团队池'},
  {value:'order_seller',label:'订单上的销售'}]
const methods = [{value:'rate',label:'比例'},{value:'fixed',label:'固定金额'},{value:'tiers',label:'阶梯'}]
const signupTypes = [{value:'',label:'全部订单'},{value:'new',label:'仅新报'},{value:'retrain',label:'仅复训（实战班）'}]
const separateSignup = {value:'new_retrain',label:'新报、复训分别计算（实战班）'}
const practiceCompanySignups = signupTypes.filter(option=>option.value)
const fixedBases = [{value:'payment',label:'每笔付款'},{value:'signup',label:'每名有效报名（实战班）'}]
const tierScopes = [{value:'',label:'本项净实收'},{value:'class_net',label:'全班净实收（实战班）'}]
const rateScopes = [{value:'company',label:'指定公司'},{value:'category',label:'销售类别'}]
const rateColumns = ['条件','适用范围','比例（%）']
const grains = [{value:'payment',label:'逐单计算'},{value:'period',label:'整期汇总后计算'}]
const signupGrains = [{value:'payment',label:'逐报名计算'},grains[1]]
const appointmentScopes = [{value:'PROJECT',label:'所属项目'}, {value:'PRODUCT',label:'当前产品'}, {value:'ORGANIZATION_UNIT',label:'指定组织'}]
const recipientSources = [{value:'person',label:'指定个人'},{value:'appointment',label:'成交时的在任人员'}]
const appointmentLevels = [{value:'COMPANY',label:'成交公司'},{value:'DEPARTMENT',label:'成交部门'},{value:'TEAM',label:'成交小组'}]

function setPersonSource(row, value) {
  row.orderAppointment = value==='appointment' ? {level:'COMPANY'} : undefined
  row.personId = row.personName = row.appointment = undefined
}
function appointmentRoles(scope) {
  const roles = options.value?.roles || []
  return !scope || scope==='ORGANIZATION_UNIT'
    ? [{value:'ORGANIZATION_MEMBER',label:'组织在岗成员'},...roles.filter(role=>role.organization)]
    : roles.filter(role=>role.personal)
}
const companies = computed(() => (options.value?.organizations || []).filter(unit=>unit.kind==='COMPANY'))
const salesPeopleOptions = computed(() => (props.people || []).map(person=>({
  ...person,label:person.label+' · '+person.value,
})))
const pools = computed(() => (options.value?.teamPools || []).map(pool=>({...pool,unitName:pool.productName+' · '+pool.unitName})))
const practicePools = computed(() => pools.value.map(pool=>({value:pool.id,label:pool.name+' · '+pool.unitName})))
const titleFor = (values,value) => values.find(option=>option.value===value)?.label
const field = (row,part) => 'common-arrangement-'+row.key+'-'+part
const hasPracticeSales = computed(()=>rows.value.some(row=>row.practiceSales))
const isPractice = computed(()=>props.practiceContext || props.baseRule && isClassOnlyRule(props.baseRule) ||
  rows.value.some(row=>row.practiceSales || row.practiceCompany || row.practiceRetrain ||
    row.signupType==='retrain' || row.signupType==='new_retrain' || row.tierScope==='class_net'))
const signupOptions = computed(()=>isPractice.value ? [...signupTypes,separateSignup] : signupTypes)
const moneyFormat = new Intl.NumberFormat('zh-CN',{style:'currency',currency:'CNY'})
function scopeLabel(row) {
  const signupType = row.practiceCompany?.classMember || row.signupType
  const signup = (signupType ? titleFor(signupOptions.value,signupType) : '全部订单')+(row.appointment && row.target==='person' ? ' · 仅任职期' : '')
  if (row.internalSplit) return signup+' · '+(titleFor(companies.value,row.internalSplit.companyId) || '选择成交公司')
  const salesOrganizations = row.practiceSales
    ? row.practiceSales.schedules.flatMap(schedule=>schedule.companyIds.map(id=>titleFor(props.practiceCompanies,id)))
    : row.units.map(id=>titleFor(options.value?.organizations || [],id))
  const categories = row.categories.map(id=>titleFor(options.value?.categories || [],id))
  const people = (row.salesPeople || []).map(id=>titleFor(props.people,id))
  const scopes = [...new Set([...salesOrganizations,...categories,...people].filter(Boolean))]
  if (!scopes.length) return signup
  return signup+' · '+scopes.slice(0,2).join('、')+(scopes.length>2 ? ' 等 '+scopes.length+' 项' : '')
}
function recipientLabel(row) {
  if (row.internalSplit) return '按成交时岗位拆分'
  if (row.practiceCompany) return '按外部销售归属'
  if (row.practiceRetrain) return '成交人、服务岗与团队池'
  if (row.target==='person' && row.orderAppointment) {
    return titleFor(options.value?.roles || [],row.orderAppointment.role) || '成交时的在任人员'
  }
  if (row.target==='person') return row.personName || titleFor(props.people,row.personId) || '个人'
  if (row.target==='company') return titleFor(companies.value,row.unitId) || '公司'
  if (row.target==='pool') return pools.value.find(pool=>pool.id===row.poolId)?.name || '团队池'
  return titleFor(recipients,row.target)
}
function calculationLabel(row) {
  if (row.internalSplit) {
    const {sellerRate,headSellerRate} = row.internalSplit
    return sellerRate==null ? '设置销售及岗位比例' : '成交 '+sellerRate+'%'+(headSellerRate==null ? '' : ' / '+headSellerRate+'%')
  }
  if (row.practiceSales) {
    return row.practiceSales.metric==='count' ? '累计新报优先，未达档按本期业绩' : '本期个人业绩选档'
  }
  if (row.practiceCompany) return '公司与类别费率'
  if (row.practiceRetrain) return row.practiceRetrain.amount==null ? '按公司拆分' : moneyFormat.format(row.practiceRetrain.amount)+' / 报名'
  if (row.method==='rate' && row.rate!=null) return (row.rateOverrides?.length ? '默认 ' : '')+row.rate+'%'
  if (row.method==='fixed' && row.amount!=null) return moneyFormat.format(row.amount)+(row.grain==='period' ? ' / 期' : row.fixedBasis==='signup' ? ' / 报名' : ' / 付款')
  if (row.method==='tiers') return (row.tierScope==='class_net' && row.grain==='period' ? '全班净实收' : '净实收')+'阶梯'
  return titleFor(methods,row.method)
}

function addRow(kind) {
  const row = {key:crypto.randomUUID(),name:'',target:'order_seller',method:'rate',grain:'payment',
    units:[],categories:[],salesPeople:[],tiers:[{from:0},{from:undefined}],signupType:'',fixedBasis:'payment',tierScope:'',rateOverrides:[]}
  if (kind==='practice') {
    Object.assign(row,{name:'实战班销售提成',method:'tiers',signupType:'new',practiceSales:practiceSalesForm()})
  } else if (kind==='practice_company') {
    row.practiceCompany = {classMember:'new',companySales:{label:'外部公司提成',payee:{kind:'sales_company',pool:{}},
      categories:[],internalCompanies:[]}}
  }
  if (kind && kind!=='common') {
    if (rows.value.length===1 && !rows.value[0].name && rows.value[0].target==='order_seller' &&
        rows.value[0].method==='rate' && rows.value[0].grain==='payment' && rows.value[0].rate==null &&
        !rows.value[0].practiceCompany && !rows.value[0].signupType && !rows.value[0].units.length &&
        !rows.value[0].categories.length && !rows.value[0].salesPeople.length && !rows.value[0].rateOverrides.length) {
      rows.value = []
    }
  }
  rows.value.push(row)
  active.value = row.key
}
function addRetrainCompany(row) {
  row.practiceRetrain.retrainSplits.push(practiceRetrainSplitForm({personalMinor:0,pool:{}}))
}
function removeRow(row) {
  rows.value = rows.value.filter(entry=>entry.key!==row.key)
  active.value = rows.value.at(-1)?.key
}
function removeTier(row, index) {
  row.tiers.splice(index,1)
  row.tiers[0].from = 0
}
onMounted(async ()=>{
  if (!rows.value.length) addRow()
  loading.value = true
  try {
    options.value = await props.api('/rule-plugins/options')
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
})
async function save() {
  if (!options.value || saving.value) return
  error.value = ''
  saving.value = true
  try {
    const payload = rulePluginPayload(name.value,rows.value,props.baseRule?.id)
    await props.api('/rule-plugins',{method:'POST',body:JSON.stringify(payload)})
    emit('saved')
  } catch (err) {
    error.value = err.message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <a-drawer open :title="baseRule ? '编辑分润规则' : '配置分润规则'" width="min(960px, 100vw)" root-class-name="finance-rule-editor common-rule-drawer"
    :closable="!saving" :mask-closable="!saving" :keyboard="!saving" @close="emit('close')">
    <a-form layout="vertical" :disabled="saving" aria-label="分润规则" @finish="save">
      <a-form-item class="rule-name" label="规则名称" html-for="common-rule-name" required>
        <a-input id="common-rule-name" v-model:value="name" placeholder="例如：大春 AI 499 分润" :maxlength="80"/>
      </a-form-item>
      <a-spin v-if="loading" tip="正在读取规则选项"/>
      <template v-if="options">
        <div class="rule-blocks-heading"><h3 class="form-subhead">计提项</h3>
          <a-dropdown :trigger="['click']">
            <a-button><PlusOutlined aria-hidden="true"/> 添加计提项 <DownOutlined aria-hidden="true"/></a-button>
            <template #overlay>
              <a-menu @click="({key})=>addRow(key)">
                <a-menu-item key="common">通用计提项</a-menu-item>
                <a-menu-item key="practice" :disabled="hasPracticeSales">实战班销售阶梯</a-menu-item>
                <a-menu-item key="practice_company">实战班外部公司提成</a-menu-item>
              </a-menu>
            </template>
          </a-dropdown>
        </div>
        <div class="rule-blocks">
        <section v-for="row in rows" :key="row.key" class="rule-block">
          <button type="button" class="rule-block-header" :aria-expanded="active===row.key" :aria-controls="field(row,'fields')"
            @click="active=active===row.key ? undefined : row.key">
            <DownOutlined aria-hidden="true"/>
            <span class="item-heading">
              <strong>{{ (row.practiceCompany ? row.practiceCompany.companySales.label : row.name) || '新计提项' }}</strong>
              <span class="item-context">{{ recipientLabel(row) }} · {{ scopeLabel(row) }}</span>
            </span>
            <span class="item-calc">
              <span class="item-grain">{{ row.internalSplit ? '逐单 + 整期' : row.practiceSales || row.practiceCompany || row.practiceRetrain || (row.grain==='payment' && row.method==='fixed' && row.fixedBasis==='signup') ? '逐报名' : row.grain==='payment' ? '逐单' : '整期' }}</span>
              <span>{{ calculationLabel(row) }}</span>
            </span>
          </button>
          <div v-if="active===row.key" :id="field(row,'fields')" class="rule-block-body">
            <template v-if="row.internalSplit">
              <a-alert type="warning" show-icon message="旧的合并计提项。请移除后，分别配置销售和岗位提成。"/>
            </template>
            <template v-else-if="row.practiceSales">
              <a-form-item label="计提项名称" :html-for="field(row,'name')" required>
                <a-input :id="field(row,'name')" v-model:value="row.name" :maxlength="80"/>
              </a-form-item>
              <PracticePluginSalesEditor :form="row.practiceSales" :companies="practiceCompanies" :people="people"
                :openings="practiceOpenings" :prefix="field(row,'practice')"/>
            </template>
            <PracticeCompanySalesEditor v-else-if="row.practiceCompany" :component="row.practiceCompany" :index="row.key"
              :companies="practiceCompanies" :organizations="options.organizations" :sales-categories="practiceCategories"
              :recipients="companies" :people="people" :units="options.organizations" :pools="practicePools"
              :amounts="catalogFlatMethods" :payee-roles="[{value:'SELLER',label:'成交销售'}]"
              :member-choices="practiceCompanySignups" manage-recommendations/>
            <template v-else-if="row.practiceRetrain">
              <RuleArrangementFields :name-id="field(row,'name')" name-label="计提项名称" v-model:name="row.name" :disabled="saving">
                <template #calculation>
                  <RuleStandardValue :id="field(row,'retrain-total')" v-model="row.practiceRetrain.amount"
                    method="fixed" label="每份复训总提成（元）"/>
                </template>
              </RuleArrangementFields>
              <PracticeRetrainSplitEditor :component="row.practiceRetrain" :index="row.key" :companies="practiceCompanies"
                :pools="practicePools" :people="people" :units="options.organizations" :format-money="minor=>moneyFormat.format(minor/100)"
                @add-company="addRetrainCompany(row)" allow-add/>
            </template>
            <RuleArrangementFields v-else :name-id="field(row,'name')" name-label="计提项名称" v-model:name="row.name" :disabled="saving">
              <a-form-item label="收款对象" :html-for="field(row,'target')" required>
                <a-select :id="field(row,'target')" v-model:value="row.target" :options="recipients"/>
              </a-form-item>
              <a-form-item v-if="row.target==='person'" label="收款人来源" :html-for="field(row,'person-source')" required>
                <a-select :id="field(row,'person-source')" :value="row.orderAppointment ? 'appointment' : 'person'" :options="recipientSources"
                  @update:value="setPersonSource(row,$event)"/>
              </a-form-item>
              <a-form-item v-if="row.target==='person' && !row.orderAppointment" label="收款人" :html-for="field(row,'person')" required>
                <AppointmentPersonSelect :id="field(row,'person')" v-model:value="row.personId" :api="api"
                  :current="row.personId ? {id:row.personId,name:recipientLabel(row)} : undefined"
                  @change="(_,option)=>row.personName=option?.name"/>
              </a-form-item>
              <a-form-item v-else-if="row.target==='company'" label="收款公司" :html-for="field(row,'company')" required>
                <a-select :id="field(row,'company')" v-model:value="row.unitId" :options="companies" show-search option-filter-prop="label"/>
              </a-form-item>
              <CatalogTeamPoolFields v-else-if="row.target==='pool'" :prefix="field(row,'target')" v-model="row.poolId" :pools="pools"/>
              <div v-if="row.target==='person' && row.orderAppointment" class="personal-appointment">
                <div class="rule-basics appointment-fields">
                  <a-form-item label="成交组织层级" :html-for="field(row,'order-appointment-level')" required>
                    <a-select :id="field(row,'order-appointment-level')" v-model:value="row.orderAppointment.level" :options="appointmentLevels"/>
                  </a-form-item>
                  <a-form-item label="收款岗位" :html-for="field(row,'order-appointment-role')" required>
                    <a-select :id="field(row,'order-appointment-role')" v-model:value="row.orderAppointment.role"
                      :options="options.roles?.filter(role=>role.organization)" show-search option-filter-prop="label"/>
                  </a-form-item>
                  <a-form-item v-if="row.orderAppointment.level==='TEAM'" label="适用销售小组" :html-for="field(row,'order-appointment-groups')">
                    <a-select :id="field(row,'order-appointment-groups')" v-model:value="row.orderAppointment.groupIds"
                      :options="options.organizations?.filter(unit=>unit.kind==='TEAM' || unit.kind==='COMPANY')"
                      mode="multiple" show-search option-filter-prop="label"/>
                  </a-form-item>
                </div>
                <a-checkbox v-if="row.orderAppointment.level==='TEAM'" v-model:checked="row.orderAppointment.skipSellerHead">
                  {{ row.orderAppointment.role==='GROUP_LEAD' ? '团队负责人自行成交时，组长份额归本人' : '团队负责人自行成交时，本项不计' }}
                </a-checkbox>
              </div>
              <div v-if="row.target==='person' && !row.orderAppointment" class="personal-appointment">
                <a-checkbox :checked="!!row.appointment"
                  @update:checked="row.appointment=$event ? {scope:'ORGANIZATION_UNIT'} : undefined">仅计任职期间的订单</a-checkbox>
                <div v-if="row.appointment" class="rule-basics appointment-fields">
                  <a-form-item label="任职范围" :html-for="field(row,'appointment-scope')" required>
                    <a-select :id="field(row,'appointment-scope')" :value="row.appointment.scope || 'ORGANIZATION_UNIT'" :options="appointmentScopes"
                      @update:value="row.appointment.scope=$event; row.appointment.unitId=undefined; row.appointment.role=undefined"/>
                  </a-form-item>
                  <a-form-item v-if="!row.appointment.scope || row.appointment.scope==='ORGANIZATION_UNIT'" label="任职组织" :html-for="field(row,'appointment-unit')" required>
                    <a-select :id="field(row,'appointment-unit')" v-model:value="row.appointment.unitId" :options="options.organizations"
                      show-search option-filter-prop="label"/>
                  </a-form-item>
                  <a-form-item label="任职岗位" :html-for="field(row,'appointment-role')" required>
                    <a-select :id="field(row,'appointment-role')" v-model:value="row.appointment.role"
                      :options="appointmentRoles(row.appointment.scope)"
                      show-search option-filter-prop="label"/>
                  </a-form-item>
                </div>
              </div>
              <template #calculation>
                <a-form-item label="计提方式" :html-for="field(row,'method')" required>
                  <a-select :id="field(row,'method')" v-model:value="row.method" :options="methods"/>
                </a-form-item>
                <a-form-item label="计算粒度" :html-for="field(row,'grain')" required>
                  <a-select :id="field(row,'grain')" v-model:value="row.grain"
                    :options="row.method==='fixed' && row.fixedBasis==='signup' ? signupGrains : grains"/>
                </a-form-item>
                <a-form-item v-if="row.method==='fixed' && row.grain==='payment'" label="固定金额单位" :html-for="field(row,'fixed-basis')">
                  <a-select :id="field(row,'fixed-basis')" v-model:value="row.fixedBasis" :options="fixedBases"/>
                </a-form-item>
                <RuleStandardValue v-if="row.method==='rate'" :id="field(row,'rate')" v-model="row.rate" :precision="4"
                  :label="row.rateOverrides?.length && row.target==='order_seller' && row.grain==='payment' ? '默认比例（%）' : undefined"/>
                <RuleStandardValue v-else-if="row.method==='fixed'" :id="field(row,'fixed')" v-model="row.amount" method="fixed"
                  :label="row.grain==='period' ? '每期金额（元）' : '固定金额（元）'"/>
              </template>
              <template #standards>
                <div v-if="row.method==='tiers'" class="tier-settings">
                  <a-form-item v-if="row.grain==='period'" label="选档依据" :html-for="field(row,'tier-scope')">
                    <a-select :id="field(row,'tier-scope')" v-model:value="row.tierScope" :options="tierScopes"/>
                  </a-form-item>
                  <CatalogPeriodTiers :tiers="row.tiers" :rate-precision="4" :id-prefix="field(row,'tier')"
                    :basis-label="row.grain==='period' && row.tierScope==='class_net' ? '全班净实收' : row.grain==='payment' ? '单笔净实收' : '本项净实收'" :format-money="minor=>moneyFormat.format(minor/100)"
                    @remove="i=>removeTier(row,i)"/>
                </div>
              </template>
              <template #advanced>
                <details class="rule-conditions">
                  <summary>{{ row.target==='order_seller' && row.method==='rate' && row.grain==='payment' ? '适用范围与条件比例' : '适用范围' }}</summary>
                  <div v-if="row.target==='order_seller' && row.method==='rate' && row.grain==='payment'" class="rule-rate-overrides">
                    <a-button size="small" @click="row.rateOverrides.push({scope:'category'})">添加条件费率</a-button>
                    <template v-if="row.rateOverrides.length">
                      <p class="rule-formula">指定公司优先于销售类别，其余订单用默认比例。</p>
                      <RuleEditableTable label="条件费率" :columns="rateColumns" :rows="row.rateOverrides">
                        <template #cells="{row:rateRow,index}">
                          <td><label class="sr-only" :for="field(row,'rate-type-'+index)">第 {{ index+1 }} 条条件类型</label>
                            <a-select :id="field(row,'rate-type-'+index)" :value="rateRow.scope" :options="rateScopes"
                            @change="rateRow.scope=$event; rateRow.id=undefined"/></td>
                          <td><label class="sr-only" :for="field(row,'rate-scope-'+index)">第 {{ index+1 }} 条适用范围</label>
                            <a-select :id="field(row,'rate-scope-'+index)" v-model:value="rateRow.id"
                            show-search option-filter-prop="label" :options="rateRow.scope==='company' ? companies : options.categories"/></td>
                          <td><a-input-number :aria-label="'第 '+(index+1)+' 条条件比例'" v-model:value="rateRow.rate"
                            :min="0" :max="100" :precision="4"/></td>
                        </template>
                        <template #actions="{index}">
                          <a-button type="link" danger size="small" @click="row.rateOverrides.splice(index,1)">删除</a-button>
                        </template>
                      </RuleEditableTable>
                    </template>
                  </div>
                  <div class="rule-basics">
                    <RuleSalesScopeFields :organization-id="field(row,'units')" :category-id="field(row,'categories')"
                      :person-id="isPractice ? field(row,'sales-people') : undefined"
                      v-model:organization-ids="row.units" v-model:category-ids="row.categories"
                      v-model:person-ids="row.salesPeople" :people="salesPeopleOptions"
                      :organizations="options.organizations" :categories="options.categories"/>
                    <a-form-item label="适用订单" :html-for="field(row,'signup-type')">
                      <a-select :id="field(row,'signup-type')" v-model:value="row.signupType" :options="signupOptions"/>
                    </a-form-item>
                  </div>
                </details>
              </template>
            </RuleArrangementFields>
            <div v-if="rows.length>1" class="item-actions"><a-button type="link" danger @click="removeRow(row)">移除此计提项</a-button></div>
          </div>
        </section>
        </div>
      </template>
    </a-form>
    <template #footer>
      <a-alert v-if="error" class="feedback" type="error" :message="error" show-icon/>
      <div class="form-actions">
        <a-button :disabled="saving" @click="emit('close')">取消</a-button>
        <a-button type="primary" :loading="saving" :disabled="!options" @click="save">保存规则</a-button>
      </div>
    </template>
  </a-drawer>
</template>

<style scoped>
.rule-name { max-width:480px; }
.rule-blocks-heading { margin-top:20px; margin-bottom:10px; }
.rule-blocks { gap:10px; }
.rule-block { border-radius:8px; }
.rule-block-header { min-height:58px; padding:10px 14px; column-gap:12px; }
.item-heading { display:grid; gap:3px; min-width:0; flex:1; }
.item-context { color:var(--muted); font-size:12px; overflow-wrap:anywhere; }
.item-calc { display:flex; align-items:center; gap:8px; color:var(--ink); text-align:left; }
.item-grain { color:var(--accent); background:#fff; border:1px solid var(--line); border-radius:6px; padding:2px 7px; white-space:nowrap; }
.rule-block-body { padding:16px; }
.rule-block-body :deep(.ant-form-item) { margin-bottom:12px; }
.rule-conditions { margin-top:8px; padding:10px 12px; background:#f7f9fc; border:1px solid var(--line); border-radius:8px; }
.rule-conditions summary { cursor: pointer; color: var(--accent); width: fit-content; }
.rule-conditions .rule-basics { margin-top: 8px; }
.tier-settings, .rule-rate-overrides { margin-top: 6px; }
.rule-rate-overrides .ant-select { width: 100%; }
.item-actions { display:flex; justify-content:flex-end; margin-top:8px; }
.personal-appointment { grid-column:1/-1; margin:0 0 12px; padding:10px 12px; border:1px solid var(--line); border-radius:8px; }
.appointment-fields { margin-top:8px; }
.appointment-fields :deep(.ant-form-item) { margin-bottom:0; }
@media(max-width:600px) {
  .item-heading { flex-basis:calc(100% - 32px); }
  .item-calc { padding-left:25px; flex-wrap:wrap; }
  .rule-block-body { padding:12px; }
}
</style>

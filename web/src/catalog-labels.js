export const salesStatus = {
  SALES_SOURCE_MISSING:'需重拉订单销售数据',
  SALES_SOURCE_CONFLICT:'成交人或锁粉归属冲突',
  SALES_PERSON_MISSING:'成交人待核',
  SALES_PERSON_CONFLICT:'成交人标识冲突',
  SALES_COMPANY_MISSING:'成交时的销售公司待核',
  SALES_COMPANY_CONFLICT:'成交时的销售公司有冲突',
  SALES_APPOINTMENT_MISSING:'成交时的销售任职待核',
  SALES_APPOINTMENT_INACTIVE:'付款时销售任职未生效 · 不计提',
  SALES_CATEGORY_MISSING:'成交时的销售类别待核',
  SALES_CATEGORY_CONFLICT:'成交时的销售类别有冲突',
  SALES_RELATIONSHIP_MISSING:'直销或间销待核',
  SALES_RATE_MISSING:'缺少适用费率',
  SALES_RATE_CONFLICT:'适用费率冲突',
  SALES_COMPANY_REVIEW:'销售公司更正待核',
  SALES_TEAM_PENDING:'成交时的团队任职待核',
  SALES_GROUP_PENDING:'成交时的销售小组待核',
}
export const salesRoles = {SALES:'成交人',DCAI_GROUP_LEAD:'小组组长',DCAI_SERVICE:'服务岗',DCAI_TEAM_HEAD:'团队负责人'}
export const benefitStatus = {
  INACTIVE:'付款时未任职',
  BENEFIT_CONTEXT_MISSING:'核算所需的组织任职待补',
  BENEFIT_PERSON_MISSING:'受益人待核',
  BENEFIT_ORG_MISSING:'受益组织待核',
  BENEFIT_ORG_CONFLICT:'受益组织有冲突',
  BENEFIT_POOL_OWNER_MISSING:'付款时的分配负责人待核',
  BENEFIT_POOL_OWNER_CONFLICT:'付款时的分配负责人有冲突',
  BENEFIT_RULE_UNSUPPORTED:'分润组件待启用',
}

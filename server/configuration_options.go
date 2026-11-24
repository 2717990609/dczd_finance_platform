package main

const configurationOptionsSQL = `SELECT coalesce(jsonb_object_agg(group_key,items),'{}') FROM (
 SELECT o.group_key,jsonb_agg(jsonb_build_object('value',o.option_key,'label',coalesce(CASE WHEN o.label=o.option_key THEN company.name END,o.label),
  'scope',o.applies_to || CASE WHEN company.id IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('organizationId',company.id) END)
  || CASE WHEN o.enabled THEN '{}'::jsonb ELSE '{"disabled":true}'::jsonb END
  ORDER BY o.display_order,o.option_key) AS items
 FROM (
  SELECT group_key,option_key,label,display_order,applies_to,enabled FROM configuration_options
  WHERE enabled OR group_key='catalogAppointmentRole'
  UNION ALL
  SELECT 'practiceSalesCategory',id::text,name,0,'{}'::jsonb,true FROM organization_sales_categories WHERE active
  UNION ALL
  SELECT 'practiceSalesCompany',s.source_company_id,min(u.name),100,'{}'::jsonb,true
   FROM organization_company_sources s JOIN organization_units u ON u.id=s.company_id AND u.kind='COMPANY' AND u.status='ACTIVE'
   WHERE s.source='DCZD' AND s.valid_from<=CURRENT_TIMESTAMP AND (s.valid_to IS NULL OR CURRENT_TIMESTAMP<s.valid_to)
    AND NOT EXISTS(SELECT 1 FROM configuration_options o WHERE o.group_key='practiceSalesCompany' AND o.option_key=s.source_company_id)
   GROUP BY s.source_company_id HAVING count(DISTINCT s.company_id)=1
 ) o LEFT JOIN LATERAL (
  SELECT min(u.name) AS name,min(u.id::text) AS id FROM organization_company_sources c JOIN organization_units u ON u.id=c.company_id
  WHERE o.group_key='practiceSalesCompany' AND c.source='DCZD' AND u.status='ACTIVE'
   AND c.source_company_id=o.option_key AND c.valid_from<=CURRENT_TIMESTAMP
   AND (c.valid_to IS NULL OR CURRENT_TIMESTAMP<c.valid_to)
  HAVING count(DISTINCT c.company_id)=1
 ) company ON true
 GROUP BY o.group_key
) groups`

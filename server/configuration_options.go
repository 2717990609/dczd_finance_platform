package main

const configurationOptionsSQL = `SELECT coalesce(jsonb_object_agg(group_key,items),'{}') FROM (
 SELECT o.group_key,jsonb_agg(jsonb_build_object('value',o.option_key,'label',coalesce(company.name,o.label),'scope',o.applies_to)
  ORDER BY o.display_order,o.option_key) AS items
 FROM configuration_options o LEFT JOIN LATERAL (
  SELECT min(u.name) AS name FROM organization_company_sources c JOIN organization_units u ON u.id=c.company_id
  WHERE o.group_key='practiceSalesCompany' AND o.label=o.option_key AND c.source='DCZD'
   AND c.source_company_id=o.option_key AND c.valid_from<=CURRENT_TIMESTAMP
   AND (c.valid_to IS NULL OR CURRENT_TIMESTAMP<c.valid_to)
  HAVING count(DISTINCT c.company_id)=1
 ) company ON true
 WHERE o.enabled GROUP BY o.group_key
) groups`

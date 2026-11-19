package main

const configurationOptionsSQL = `SELECT coalesce(jsonb_object_agg(group_key,items),'{}') FROM (
 SELECT group_key, jsonb_agg(jsonb_build_object('value',option_key,'label',label,'scope',applies_to)
  ORDER BY display_order,option_key) AS items
 FROM configuration_options WHERE enabled GROUP BY group_key
) groups`

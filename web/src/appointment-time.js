export function beijingTime(value) {
  return value ? (value.length===16 ? value+':00' : value)+'+08:00' : null
}

export function appointmentDateError({mode,record,effective,through}) {
  if (!effective) return ''
  if (mode==='add') return through && through<=effective ? '计划结束时间须晚于生效时间。' : ''
  const at = Date.parse(beijingTime(effective))
  if (at<=Date.parse(record.validFrom)) return '办理时间须晚于原生效时间。'
  if (record.validTo && at>=Date.parse(record.validTo)) return '办理时间须早于原结束时间。'
  return ''
}

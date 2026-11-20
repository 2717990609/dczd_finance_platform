export function organizationTree(units, label, activeOnly = false) {
  const nodes = new Map(units.map(unit => [unit.id, {
    value:unit.id,
    title:unit.name + ' · ' + label(unit.kind) + (unit.status==='INACTIVE' ? '（已停用）' : ''),
    selectable:!activeOnly || unit.status==='ACTIVE',
    children:[],
  }]))
  const roots = []
  for (const unit of units) {
    const node = nodes.get(unit.id)
    const parent = nodes.get(unit.parentId)
    if (parent) parent.children.push(node)
    else roots.push(node)
  }
  return roots
}

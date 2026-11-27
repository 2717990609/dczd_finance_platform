export const organizationScopeOptions = [
  { value:'', label:'全部', allLabel:'全部组织' },
  { value:'INTERNAL', label:'内部', allLabel:'全部内部组织' },
  { value:'EXTERNAL', label:'外部', allLabel:'全部外部组织' },
]

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

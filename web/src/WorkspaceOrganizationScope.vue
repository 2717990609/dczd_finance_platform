<script setup>
import { computed, ref, watch } from 'vue'
import { RadioGroup, Tree, TreeSelect } from 'ant-design-vue'
import { organizationScopeOptions } from './organization-tree'

const props = defineProps({ nodes: Array, selected: String, loading: Boolean, fillHeight: Boolean, hideSearchLabel: Boolean,
  allLabel: { type:String, default:'全部组织' },
  directoryScope: String,
})
const emit = defineEmits(['select','select-scope'])
const scopeLabel = computed(() => organizationScopeOptions.find(option => option.value===props.directoryScope)?.allLabel || props.allLabel)
const treeViewport = ref()
const treeHeight = ref(264)
const search = ref('')
const expanded = ref()
const searchExpanded = ref()
const fieldNames = { key:'value' }
const term = computed(() => search.value.trim().toLowerCase())

function matchingNodes(nodes, query) {
  return nodes.flatMap(node => {
    if (node.title.toLowerCase().includes(query)) return [node]
    const children = matchingNodes(node.children, query)
    return children.length ? [{...node,children}] : []
  })
}
function branchKeys(nodes) {
  return nodes.flatMap(node => node.children.length ? [node.value, ...branchKeys(node.children)] : [])
}
function parentKeys(nodes, selected) {
  for (const node of nodes) {
    if (node.value === selected) return []
    const parents = parentKeys(node.children, selected)
    if (parents) return [node.value, ...parents]
  }
}
const visibleNodes = computed(() => term.value ? matchingNodes(props.nodes, term.value) : props.nodes)
const rootKeys = computed(() => props.nodes.map(node=>node.value))
const defaultExpanded = computed(() => {
  const parents = props.selected ? parentKeys(props.nodes, props.selected) || [] : []
  return [...new Set([...rootKeys.value, ...parents])]
})
const expandedKeys = computed({
  get: () => term.value ? searchExpanded.value ?? branchKeys(visibleNodes.value) : expanded.value ?? defaultExpanded.value,
  set: keys => {
    if (term.value) searchExpanded.value = keys
    else expanded.value = keys
  },
})
watch(term, () => { searchExpanded.value = undefined })
watch(() => props.selected, unit => {
  if (!unit || !expanded.value) return
  expanded.value = [...new Set([...expanded.value, ...(parentKeys(props.nodes,unit) || [])])]
})
watch(treeViewport, (viewport, _, onCleanup) => {
  if (!props.fillHeight || !viewport) return
  const observer = new ResizeObserver(([entry]) => {
    const height = Math.floor(entry.contentRect.height)
    if (height > 0) treeHeight.value = height
  })
  observer.observe(viewport)
  onCleanup(() => observer.disconnect())
}, { flush: 'post' })
</script>

<template>
  <section class="scope-organization desktop-scope" aria-label="组织范围">
    <label v-if="!hideSearchLabel" for="workspace-organization-search" class="scope-title">组织范围</label>
    <RadioGroup v-if="directoryScope!==undefined" class="scope-type-filter" :value="directoryScope"
      :options="organizationScopeOptions" name="workspace-scope-desktop" role="radiogroup" aria-label="组织内外部筛选"
      option-type="button" button-style="solid" @update:value="emit('select-scope',$event)"/>
    <a-input id="workspace-organization-search" v-model:value="search" aria-label="搜索组织"
      placeholder="搜索公司、部门或小组" allow-clear />
    <div class="scope-options">
      <button type="button" :aria-pressed="!selected" @click="emit('select', undefined)">{{scopeLabel}}</button>
    </div>
    <div v-if="visibleNodes.length" ref="treeViewport" class="scope-tree-viewport">
      <Tree v-model:expanded-keys="expandedKeys" :selected-keys="selected ? [selected] : []"
        :tree-data="visibleNodes" :field-names="fieldNames" :height="treeHeight" block-node
        @select="keys=>keys.length && emit('select',keys[0])">
        <template #title="{title}"><span class="scope-node-title" :title="title">{{title}}</span></template>
      </Tree>
    </div>
    <p v-else class="organization-scope-status" role="status">{{ loading ? '正在读取组织' : term ? '未找到匹配组织' : '暂无组织' }}</p>
  </section>
  <div v-if="directoryScope!==undefined" class="mobile-scope">
    <RadioGroup class="scope-type-filter" :value="directoryScope" :options="organizationScopeOptions"
      name="workspace-scope-mobile" role="radiogroup" aria-label="组织内外部筛选"
      option-type="button" button-style="solid" @update:value="emit('select-scope',$event)"/>
  </div>
  <div class="scope-organization mobile-scope">
    <label for="workspace-organization">组织范围</label>
    <TreeSelect id="workspace-organization" :value="selected" :tree-data="nodes" :loading="loading"
      aria-label="组织范围" :placeholder="scopeLabel" tree-node-filter-prop="title" show-search allow-clear
      @change="emit('select', $event)" />
  </div>
</template>

<style scoped>
.scope-type-filter {display:flex;width:100%;margin-bottom:8px}
.scope-type-filter :deep(.ant-radio-button-wrapper) {flex:1;min-width:0;padding-inline:6px;text-align:center}
</style>

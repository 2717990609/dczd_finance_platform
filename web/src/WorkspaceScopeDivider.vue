<script setup>
import { ref } from 'vue'

const props = defineProps({ orientation:String, label:String, controls:String, size:Number, min:Number, max:Number })
const emit = defineEmits(['update:size'])
const drag = ref()

function position(event) {
  return props.orientation==='vertical' ? event.clientX : event.clientY
}
function resize(size) {
  emit('update:size', Math.min(props.max, Math.max(props.min, size)))
}
function startDrag(event) {
  if (!event.isPrimary || event.button!==0) return
  event.preventDefault()
  event.currentTarget.focus({ preventScroll:true })
  event.currentTarget.setPointerCapture(event.pointerId)
  drag.value = { id:event.pointerId, position:position(event), size:props.size }
}
function moveDrag(event) {
  if (drag.value?.id!==event.pointerId) return
  resize(drag.value.size + position(event) - drag.value.position)
}
function stopDrag(event) {
  if (drag.value?.id===event.pointerId) drag.value = undefined
}
function resizeByKey(event) {
  const vertical = props.orientation==='vertical'
  const step = 16
  const sizes = {
    Home:props.min,
    End:props.max,
    [vertical ? 'ArrowLeft' : 'ArrowUp']:props.size - step,
    [vertical ? 'ArrowRight' : 'ArrowDown']:props.size + step,
  }
  if (!Object.hasOwn(sizes, event.key)) return
  event.preventDefault()
  resize(sizes[event.key])
}
</script>

<template>
  <div class="scope-divider" :class="{ dragging:drag }" role="separator" tabindex="0" :title="label"
    :aria-label="label" :aria-controls="controls" :aria-orientation="orientation"
    :aria-valuemin="min" :aria-valuemax="max" :aria-valuenow="size"
    @pointerdown="startDrag" @pointermove="moveDrag" @lostpointercapture="stopDrag" @keydown="resizeByKey" />
</template>

<style scoped>
.scope-divider { position: relative; touch-action: none; user-select: none; }
.scope-divider[aria-orientation="vertical"] { position: absolute; cursor: col-resize; }
.scope-divider[aria-orientation="horizontal"] { cursor: row-resize; }
.scope-divider::before, .scope-divider::after { position: absolute; content: ''; pointer-events: none; }
.scope-divider[aria-orientation="vertical"]::before { inset: 0 4px; }
.scope-divider[aria-orientation="vertical"]::after { top: 108px; left: 3px; width: 3px; height: 32px; }
.scope-divider[aria-orientation="horizontal"]::before { inset: 6px 0 7px; background: var(--line); }
.scope-divider[aria-orientation="horizontal"]::after { top: 5px; left: calc(50% - 16px); width: 32px; height: 3px; }
.scope-divider::after { border-radius: 2px; background: var(--muted); }
.scope-divider:is(:hover, :focus-visible, .dragging)::before,
.scope-divider:is(:hover, :focus-visible, .dragging)::after { background: var(--accent); }
.scope-divider:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
</style>

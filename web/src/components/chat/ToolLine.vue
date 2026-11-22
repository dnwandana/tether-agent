<script setup lang="ts">
import type { ToolView } from "../../api/types.gen"

const props = defineProps<{ tool: ToolView; selected?: boolean }>()
const emit = defineEmits<{ select: [] }>()

// Returns the time of a tool call, for example "4.2 s".
function seconds(ms: number): string {
  return (ms / 1000).toFixed(1) + " s"
}

// Returns the state text: "✓ done", "✗ exit N", or "✗ error".
function stateText(t: ToolView): string {
  if (t.state === "running") return "running"
  if (t.state === "error") {
    const all = [...t.text.matchAll(/^\[exit code: (-?\d+)\]$/gm)]
    if (all.length > 0) return "✗ exit " + all[all.length - 1][1]
    return "✗ error"
  }
  return "✓ done"
}
</script>

<template>
  <button
    v-if="props.tool.name !== 'todo_list'"
    type="button"
    class="toolline"
    :class="['is-' + props.tool.state, { 'is-selected': props.selected }]"
    :aria-expanded="!!props.selected"
    @click="emit('select')"
  >
    <span class="em">{{ props.tool.emoji || "🛠" }}</span>
    <span class="name">{{ props.tool.name }}</span>
    <span v-if="props.tool.preview !== ''" class="args">"{{ props.tool.preview }}"</span>
    <span v-if="props.tool.state !== 'running'" class="times">{{
      seconds(props.tool.duration_ms)
    }}</span>
    <span class="st">
      <i v-if="props.tool.state === 'running'" class="spin" aria-hidden="true"></i>
      {{ stateText(props.tool) }}
    </span>
    <span v-if="props.tool.text !== ''" class="chev" aria-hidden="true">{{
      props.selected ? "▾" : "▸"
    }}</span>
  </button>
</template>

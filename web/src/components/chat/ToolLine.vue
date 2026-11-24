<script setup lang="ts">
import type { ToolView } from "../../api/types.gen"
import { TraceDescription, TraceIcon, TraceLine, TraceTitle } from "@/components/ui/trace"

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
    class="rounded-md px-1 data-[selected=true]:bg-muted"
    :aria-expanded="!!props.selected"
    :data-selected="props.selected || undefined"
    @click="emit('select')"
  >
    <TraceLine
      :status="
        props.tool.state === 'running'
          ? 'running'
          : props.tool.state === 'error'
            ? 'error'
            : undefined
      "
    >
      <TraceIcon>{{ props.tool.emoji || "🛠" }}</TraceIcon>
      <TraceTitle>
        {{ props.tool.name }}
        <template v-if="props.tool.preview !== ''"> "{{ props.tool.preview }}"</template>
      </TraceTitle>
      <TraceDescription>{{
        props.tool.state === "running"
          ? stateText(props.tool)
          : seconds(props.tool.duration_ms) + " · " + stateText(props.tool)
      }}</TraceDescription>
      <TraceIcon v-if="props.tool.text !== ''">{{ props.selected ? "▾" : "▸" }}</TraceIcon>
    </TraceLine>
  </button>
</template>

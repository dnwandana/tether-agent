<script setup lang="ts">
import type { ToolView } from "../../api/types.gen"
import { CheckIcon, ChevronDownIcon, ChevronRightIcon, XIcon } from "@lucide/vue"
import { toolIcon } from "../../lib/glyphs"
import { TraceDescription, TraceIcon, TraceLine, TraceTitle } from "@/components/ui/trace"

const props = defineProps<{ tool: ToolView; selected?: boolean }>()
const emit = defineEmits<{ select: [] }>()

// Returns the time of a tool call, for example "4.2 s".
function seconds(ms: number): string {
  return (ms / 1000).toFixed(1) + " s"
}

// Returns the state of a finished call: ok is false for an error, and word is "done",
// "exit N", or "error".
function stateOf(t: ToolView): { ok: boolean; word: string } {
  if (t.state !== "error") return { ok: true, word: "done" }
  const all = [...t.text.matchAll(/^\[exit code: (-?\d+)\]$/gm)]
  if (all.length > 0) return { ok: false, word: "exit " + all[all.length - 1][1] }
  return { ok: false, word: "error" }
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
      <TraceIcon><component :is="toolIcon(props.tool.name)" /></TraceIcon>
      <TraceTitle>
        {{ props.tool.name }}
        <template v-if="props.tool.preview !== ''"> "{{ props.tool.preview }}"</template>
      </TraceTitle>
      <TraceDescription>
        <template v-if="props.tool.state === 'running'">running</template>
        <template v-else>
          {{ seconds(props.tool.duration_ms) }} ·
          <CheckIcon v-if="stateOf(props.tool).ok" class="inline size-3 text-success" />
          <XIcon v-else class="inline size-3 text-destructive" />
          {{ stateOf(props.tool).word }}
        </template>
      </TraceDescription>
      <TraceIcon v-if="props.tool.text !== ''">
        <ChevronDownIcon v-if="props.selected" /><ChevronRightIcon v-else />
      </TraceIcon>
    </TraceLine>
  </button>
</template>

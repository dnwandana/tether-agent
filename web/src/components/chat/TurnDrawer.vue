<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue"
import type { ToolView } from "../../api/types.gen"
import { api } from "../../api/client"
import { session } from "../../stores/session"
import { toast } from "../../stores/ui"
import { turn } from "../../stores/turn"
import { cards } from "../../stores/cards"
import { status } from "../../stores/status"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import PanelFrame from "../PanelFrame.vue"
import ToolResult from "./ToolResult.vue"

const props = defineProps<{ open: boolean; tool: ToolView | null }>()
const emit = defineEmits<{ close: [] }>()

const MODES: Record<string, string> = {
  "read-only": "👁 Read-only",
  guarded: "🛡 Guarded",
  full: "⚡ Full",
}

// The full copy of the selected tool, after "Show all". It is null for another call.
const full = ref<ToolView | null>(null)
const shownTool = computed(() =>
  full.value && full.value.call === props.tool?.call ? full.value : props.tool,
)
watch(
  () => props.tool?.call,
  () => {
    full.value = null
  },
)

// Loads the full text of a tool result that the server cut at 16 KB.
async function showAll(t: ToolView): Promise<void> {
  try {
    const v = await api<ToolView>(
      "GET",
      "/api/sessions/" + encodeURIComponent(session.id) + "/tools/" + encodeURIComponent(t.call),
    )
    if (props.tool?.call === t.call) full.value = v
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

const argsText = computed(() => {
  const raw = props.tool?.args ?? ""
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
})
const stateText = computed(() =>
  props.tool?.state === "running" ? "running" : props.tool?.state === "error" ? "error" : "done",
)
const stateClass = computed(() =>
  props.tool?.state === "running"
    ? "text-info"
    : props.tool?.state === "error"
      ? "text-destructive"
      : "text-success",
)
const duration = computed(() => {
  const ms = props.tool?.duration_ms ?? 0
  return ms > 0 ? (ms / 1000).toFixed(1) + " s" : "—"
})
const modeLabel = computed(() => MODES[status.mode] ?? status.mode)
const modelLabel = computed(() =>
  status.entry ? `${status.model} (${status.entry})` : status.model,
)

// A side panel on Chat takes the side column, so the drawer closes.
function onPanelOpen(): void {
  if (props.open) emit("close")
}

onMounted(() => {
  document.addEventListener("tether:panel-open", onPanelOpen)
})
onUnmounted(() => {
  document.removeEventListener("tether:panel-open", onPanelOpen)
})
</script>

<template>
  <PanelFrame
    :open="open"
    title="Turn details"
    dock-query="(min-width: 1024px)"
    @request-close="emit('close')"
  >
    <div class="flex flex-col gap-3 text-sm">
      <template v-if="tool">
        <div class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Tool call
        </div>
        <div class="flex items-center gap-2">
          <span>{{ tool.emoji }}</span>
          <b class="font-mono font-semibold">{{ tool.name }}</b>
          <Badge variant="outline" :class="stateClass">{{ stateText }}</Badge>
        </div>
        <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
          <dt>Call ID</dt>
          <dd class="font-mono">{{ tool.call }}</dd>
          <dt>Duration</dt>
          <dd>{{ duration }}</dd>
        </dl>
        <div class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Arguments
        </div>
        <pre
          class="overflow-x-auto whitespace-pre-wrap rounded-md border bg-muted p-3 font-mono text-xs"
          >{{ argsText }}</pre>
        <div class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Result
        </div>
        <div v-if="tool.state === 'running'" class="text-muted-foreground">No result yet.</div>
        <ToolResult v-else :tool="shownTool ?? tool" @show-all="showAll" />
      </template>
      <div v-else class="text-muted-foreground">
        Select a tool line to see its full arguments and its result.
      </div>
      <Separator />
      <div class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        This session
      </div>
      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
        <dt>Mode</dt>
        <dd>{{ modeLabel }}</dd>
        <dt>Model</dt>
        <dd>{{ modelLabel }}</dd>
        <dt>Effort</dt>
        <dd>{{ session.effort || "default" }}</dd>
        <dt>Waiting cards</dt>
        <dd>{{ cards.open.length }}</dd>
        <dt>Queue</dt>
        <dd>{{ turn.queue.length }}</dd>
      </dl>
    </div>
  </PanelFrame>
</template>

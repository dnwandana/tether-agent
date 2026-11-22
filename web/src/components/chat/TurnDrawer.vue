<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue"
import type { ToolView } from "../../api/types.gen"
import { api } from "../../api/client"
import { session } from "../../stores/session"
import { toast } from "../../stores/ui"
import { turn } from "../../stores/turn"
import { cards } from "../../stores/cards"
import { status } from "../../stores/status"
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

const dy = ref(0)
let startY: number | null = null

const argsText = computed(() => {
  const raw = props.tool?.args ?? ""
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
})
const chipClass = computed(() => {
  const s = props.tool?.state
  return s === "running" ? "chip-info" : s === "error" ? "chip-bad" : "chip-ok"
})
const duration = computed(() => {
  const ms = props.tool?.duration_ms ?? 0
  return ms > 0 ? (ms / 1000).toFixed(1) + " s" : "—"
})
const modeLabel = computed(() => MODES[status.mode] ?? status.mode)
const modelLabel = computed(() =>
  status.entry ? `${status.model} (${status.entry})` : status.model,
)

function isPhone(): boolean {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return false
  return mm.call(globalThis, "(max-width: 599px)").matches
}

// Closes the drawer on Esc. It skips an event that a dialog or a popover took first.
function onKey(e: KeyboardEvent): void {
  if (e.key !== "Escape" || !props.open || e.defaultPrevented) return
  emit("close")
}

function yOf(e: TouchEvent): number {
  return e.touches[0]?.clientY ?? e.changedTouches?.[0]?.clientY ?? 0
}

// Start, move, and end of a drag on the grabber or the header. Only a phone width uses them.
function dragStart(e: TouchEvent): void {
  startY = isPhone() ? yOf(e) : null
}
function dragMove(e: TouchEvent): void {
  if (startY === null) return
  dy.value = Math.max(0, yOf(e) - startY)
}
function dragEnd(): void {
  if (startY === null) return
  const moved = dy.value
  startY = null
  dy.value = 0
  if (moved > 70) emit("close")
}

onMounted(() => window.addEventListener("keydown", onKey))
onUnmounted(() => window.removeEventListener("keydown", onKey))
</script>

<template>
  <div id="aside-scrim" class="aside-scrim" :class="{ 'is-open': open }" @click="emit('close')" />
  <aside
    class="aside"
    :class="{ 'is-open': open }"
    aria-label="Turn details"
    :style="dy > 0 ? { transform: `translateY(${dy}px)` } : undefined"
  >
    <div
      class="grabber"
      aria-hidden="true"
      @touchstart.passive="dragStart"
      @touchmove.passive="dragMove"
      @touchend.passive="dragEnd"
    />
    <div
      class="aside-h"
      @touchstart.passive="dragStart"
      @touchmove.passive="dragMove"
      @touchend.passive="dragEnd"
    >
      <span>Turn details</span><span class="grow" />
      <button
        class="icon-btn"
        type="button"
        aria-label="Close the details"
        title="Close (Esc)"
        @click="emit('close')"
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.75"
          stroke-linecap="round"
          aria-hidden="true"
        >
          <path d="M6 6l12 12M18 6L6 18" />
        </svg>
      </button>
    </div>
    <div class="aside-b">
      <template v-if="tool">
        <div class="eyebrow">Tool call</div>
        <div class="row" style="display: flex; align-items: center; gap: 8px">
          <span class="ei">{{ tool.emoji }}</span>
          <b class="mono">{{ tool.name }}</b>
          <span class="chip" :class="chipClass">{{
            tool.state === "running" ? "running" : tool.state === "error" ? "error" : "done"
          }}</span>
        </div>
        <dl class="kv">
          <dt>Call ID</dt>
          <dd class="mono">{{ tool.call }}</dd>
          <dt>Duration</dt>
          <dd>{{ duration }}</dd>
        </dl>
        <div class="eyebrow">Arguments</div>
        <div class="code-block">{{ argsText }}</div>
        <div class="eyebrow">Result</div>
        <div v-if="tool.state === 'running'" class="t-13 c-3">No result yet.</div>
        <ToolResult v-else :tool="shownTool ?? tool" @show-all="showAll" />
      </template>
      <div v-else class="t-13 c-3">
        Select a tool line to see its full arguments and its result.
      </div>
      <div class="divider" />
      <div class="eyebrow">This session</div>
      <dl class="kv">
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
  </aside>
</template>

<style scoped>
.divider {
  height: 1px;
  background: var(--border);
}
.eyebrow {
  font-size: var(--fs-12);
  color: var(--text-3);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  font-weight: 600;
}
</style>

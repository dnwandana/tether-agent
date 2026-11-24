<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { api } from "../api/client"
import type { HealthView, ModelEntryRow, ModelsView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { status } from "../stores/status"
import { toast } from "../stores/ui"

type ModelEntry = ModelEntryRow

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const view = ref<ModelsView | null>(null)
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

function hhmm(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

// Gives the time in ms of a date text, or 0 when it is not a real time.
function realTime(s: string): number {
  const t = new Date(s).getTime()
  return Number.isNaN(t) || new Date(s).getFullYear() < 2000 ? 0 : t
}

function skipEnd(e: { skip_until: string }): number {
  return realTime(e.skip_until)
}

// Gives the status text and its class from the Health snapshot of one entry.
function entryStatus(
  e: ModelEntry,
  at: number,
): { text: string; cls: "c-ok" | "c-warn" | "c-bad" } {
  if (e.status === "unusable") return { text: `✗ not usable: ${e.cause}`, cls: "c-bad" }
  const end = skipEnd(e)
  if (end > at) {
    const sec = Math.ceil((end - at) / 1000)
    return { text: `⚠ skipped for ${sec} s · ${e.cause} at ${hhmm(e.failed_at)}`, cls: "c-warn" }
  }
  if (e.status === "failed") {
    return { text: `⚠ last failed ${hhmm(e.failed_at)} · ${e.cause}`, cls: "c-warn" }
  }
  return { text: "✓ answers now", cls: "c-ok" }
}

function skipRuns(): boolean {
  return (view.value?.entries ?? []).some((e) => skipEnd(e) > now.value)
}

function tick(): void {
  now.value = Date.now()
  if (!skipRuns()) {
    stopTimer()
    void refresh()
  }
}

function stopTimer(): void {
  if (timer !== null) clearInterval(timer)
  timer = null
}

async function load(): Promise<void> {
  try {
    view.value = await api<ModelsView>("GET", "/api/models")
    now.value = Date.now()
    state.value = "ready"
    if (timer === null && skipRuns()) timer = setInterval(tick, 1000)
  } catch (e) {
    // Keep the data on screen after a failed reload. Only the first load shows the error state.
    if (state.value === "ready") {
      toast(friendlyError(e), "error")
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

// Gives the step state of one chain entry from its health row.
function stepState(name: string, at: number): { text: string; cls: "c-ok" | "c-warn" } {
  const hv: HealthView | undefined = (view.value?.health ?? []).find((x) => x.entry === name)
  if (hv) {
    if (realTime(hv.skip_until) > at) return { text: "⚠ skipped", cls: "c-warn" }
    if (realTime(hv.failed_at) > 0) return { text: "⚠ failed", cls: "c-warn" }
  }
  return { text: "✓ answers", cls: "c-ok" }
}

const chain = computed(() =>
  (view.value?.chain ?? []).map((name, i) => {
    const role = i === 0 ? "Primary" : `Fallback ${i}`
    return { role, name, same: name === role.toLowerCase(), state: stepState(name, now.value) }
  }),
)

const last = { fallback: status.fallback, entry: status.entry }
// Tells the live loader whether a status event changed the fallback or the entry.
function changed(_name: string, data: unknown): boolean {
  const d = (data ?? {}) as { fallback?: boolean; entry?: string }
  let diff = false
  if (d.fallback !== undefined && d.fallback !== last.fallback) {
    last.fallback = d.fallback
    diff = true
  }
  if (d.entry !== undefined && d.entry !== last.entry) {
    last.entry = d.entry
    diff = true
  }
  return diff
}

const banner = computed(() => {
  const f = view.value?.fallback
  if (!f || !f.active) return ""
  const at = hhmm(f.failed_at)
  return `⚠ Fallback active. ${f.fallback_name} (${f.fallback_model}) answers. ${f.first_name} failed at ${at}: ${f.cause}.`
})

// Tells whether a chain step or a role uses the entry.
function used(name: string): boolean {
  const v = view.value
  return (v?.chain ?? []).includes(name) || (v?.roles ?? []).some((r) => r.value === name)
}

// Formats a context window as "200k", or as "1M" from one million tokens.
function ctxLabel(n: number): string {
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}k`
  return String(n)
}

const columns = [
  { key: "name", label: "Entry" },
  { key: "preset", label: "Preset · format" },
  { key: "model", label: "Model" },
  { key: "base_url", label: "Base URL" },
  { key: "vision", label: "Vision" },
  { key: "context", label: "Context" },
  { key: "effort", label: "Effort" },
  { key: "status", label: "Status" },
]
const rows = computed(() =>
  (view.value?.entries ?? []).map((e) => ({
    name: e.name,
    preset: `${e.preset} · ${e.format}`,
    unused: !used(e.name),
    model: e.model,
    base_url: e.base_url || "—",
    vision: e.vision ? "yes" : "—",
    context: ctxLabel(e.context_window),
    effort: e.effort || "—",
    status: entryStatus(e, now.value),
  })),
)
const roleColumns = [
  { key: "role", label: "Role" },
  { key: "key", label: "Key" },
  { key: "value", label: "Entry" },
]
const roleRows = computed(() =>
  (view.value?.roles ?? []).map((r) => ({
    ...r,
    value: r.value || "—",
    usable: r.entry_usable || !r.value,
  })),
)

// Tells whether the entry of a role row is usable.
function usable(row: unknown): boolean {
  return (row as { usable: boolean }).usable
}

// Tells whether no chain step and no role uses the entry of a row.
function isUnused(row: unknown): boolean {
  return (row as { unused: boolean }).unused
}

// Narrows the cell value of the status column.
function st(v: unknown): { text: string; cls: string } {
  return v as { text: string; cls: string }
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["status"],
  filter: changed,
})
onBeforeUnmount(stopTimer)
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Models</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="errorText" @action="load" />
    <StateBlock
      v-else-if="rows.length === 0"
      state="empty"
      text="No model entries. Run `tether setup` on the host."
    />
    <template v-else>
      <div v-if="banner" class="warn-banner">{{ banner }}</div>
      <div class="section-h"><h2>Fallback chain</h2></div>
      <ol class="chain">
        <template v-for="(c, i) in chain" :key="c.name">
          <li v-if="i > 0" aria-hidden="true">→</li>
          <li class="chain-step">
            <span class="role">{{ c.role }}</span>
            <span v-if="!c.same" class="name">{{ c.name }}</span>
            <span class="step-state" :class="c.state.cls">{{ c.state.text }}</span>
          </li>
        </template>
      </ol>
      <DataTable :columns="columns" :rows="rows" row-key="name">
        <template #cell-name="{ value, row }"
          ><b>{{ value }}</b
          ><template v-if="isUnused(row)"
            >{{ " " }}<span class="chip">not used</span></template
          ></template
        >
        <template #cell-model="{ value }"
          ><span class="mono wrap-any">{{ value }}</span></template
        >
        <template #cell-base_url="{ value }"
          ><span class="mono wrap-any">{{ value }}</span></template
        >
        <template #cell-status="{ value }">
          <span class="status" :class="st(value).cls">{{ st(value).text }}</span>
        </template>
      </DataTable>
      <div class="section-h"><h2>Roles</h2></div>
      <DataTable :columns="roleColumns" :rows="roleRows" row-key="key">
        <template #cell-key="{ value }"
          ><span class="mono">{{ value }}</span></template
        >
        <template #cell-value="{ value, row }"
          >{{ value }}<span v-if="!usable(row)" class="c-warn"> ⚠️ entry unusable</span></template
        >
      </DataTable>
      <p class="pill-note">
        Entries and roles come from config.yaml. Edit the file on the host, then restart. The model
        of the dashboard session changes with the model chip in Chat.
      </p>
    </template>
  </div>
</template>

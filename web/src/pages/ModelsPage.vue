<script setup lang="ts">
import { computed, onBeforeUnmount, ref, type Component } from "vue"
import { CheckIcon, TriangleAlertIcon, XIcon } from "@lucide/vue"
import { api } from "../api/client"
import type { HealthView, ModelEntryRow, ModelsView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import type { Column } from "../lib/table"
import { useLiveData } from "../lib/live"
import { status } from "../stores/status"
import { toast } from "../stores/ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"

type ModelEntry = ModelEntryRow
type Tone = "ok" | "warn" | "bad"
// Maps a tone to its semantic text color.
const TONE: Readonly<Record<Tone, string>> = {
  ok: "text-success",
  warn: "text-warning",
  bad: "text-destructive",
}
// Maps a tone to the icon that goes before the status text.
const TONE_ICON: Readonly<Record<Tone, Component>> = {
  ok: CheckIcon,
  warn: TriangleAlertIcon,
  bad: XIcon,
}

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

// Gives the status text and its tone from the Health snapshot of one entry.
function entryStatus(e: ModelEntry, at: number): { text: string; tone: Tone } {
  if (e.status === "unusable") return { text: `not usable: ${e.cause}`, tone: "bad" }
  const end = skipEnd(e)
  if (end > at) {
    const sec = Math.ceil((end - at) / 1000)
    return { text: `skipped for ${sec} s · ${e.cause} at ${hhmm(e.failed_at)}`, tone: "warn" }
  }
  if (e.status === "failed") {
    return { text: `last failed ${hhmm(e.failed_at)} · ${e.cause}`, tone: "warn" }
  }
  return { text: "answers now", tone: "ok" }
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
function stepState(name: string, at: number): { text: string; tone: "ok" | "warn" } {
  const hv: HealthView | undefined = (view.value?.health ?? []).find((x) => x.entry === name)
  if (hv) {
    if (realTime(hv.skip_until) > at) return { text: "skipped", tone: "warn" }
    if (realTime(hv.failed_at) > 0) return { text: "failed", tone: "warn" }
  }
  return { text: "answers", tone: "ok" }
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
  return `Fallback active. ${f.fallback_name} (${f.fallback_model}) answers. ${f.first_name} failed at ${at}: ${f.cause}.`
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

const columns: Column[] = [
  { key: "name", label: "Entry", type: "wrap" },
  { key: "preset", label: "Preset · format", type: "text", optional: true },
  { key: "model", label: "Model", type: "wrap" },
  { key: "base_url", label: "Base URL", type: "wrap", optional: true },
  { key: "vision", label: "Vision", type: "badge", width: "5rem", optional: true },
  { key: "context", label: "Context", type: "badge", width: "5rem" },
  { key: "effort", label: "Effort", type: "badge", width: "6rem", optional: true },
  { key: "status", label: "Status", type: "wrap" },
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
const roleColumns: Column[] = [
  { key: "role", label: "Role", type: "text" },
  { key: "key", label: "Key", type: "text" },
  { key: "value", label: "Entry", type: "wrap" },
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
function st(v: unknown): { text: string; tone: Tone } {
  return v as { text: string; tone: Tone }
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["status"],
  filter: changed,
})
onBeforeUnmount(stopTimer)
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Models</h1>
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
      <Alert v-if="banner"
        ><TriangleAlertIcon class="text-warning" /><AlertDescription>{{
          banner
        }}</AlertDescription></Alert
      >
      <h2 id="models-chain-h" class="text-base font-semibold">Fallback chain</h2>
      <ol aria-labelledby="models-chain-h" class="flex flex-wrap items-center gap-2">
        <template v-for="(c, i) in chain" :key="c.name">
          <li v-if="i > 0" aria-hidden="true" class="text-muted-foreground">→</li>
          <li class="flex items-center gap-2 rounded-md border px-3 py-1.5 text-sm">
            <span class="font-medium">{{ c.role }}</span>
            <span v-if="!c.same" class="font-mono text-muted-foreground">{{ c.name }}</span>
            <span
              :data-tone="c.state.tone"
              :class="['inline-flex items-center gap-1', TONE[c.state.tone]]"
              ><component :is="TONE_ICON[c.state.tone]" class="size-3.5 shrink-0" />{{
                c.state.text
              }}</span
            >
          </li>
        </template>
      </ol>
      <DataTable
        :columns="columns"
        :rows="rows"
        row-key="name"
        page-key="models"
        page-label="Model entries"
      >
        <template #cell-name="{ value, row }"
          ><b class="font-medium">{{ value }}</b
          ><template v-if="isUnused(row)"
            >{{ " " }}<Badge variant="outline">not used</Badge></template
          ></template
        >
        <template #cell-model="{ value }"
          ><span class="font-mono wrap-anywhere">{{ value }}</span></template
        >
        <template #cell-base_url="{ value }"
          ><span class="font-mono wrap-anywhere">{{ value }}</span></template
        >
        <template #cell-status="{ value }">
          <span
            :data-tone="st(value).tone"
            :class="['inline-flex items-center gap-1', TONE[st(value).tone]]"
            ><component :is="TONE_ICON[st(value).tone]" class="size-3.5 shrink-0" />{{
              st(value).text
            }}</span
          >
        </template>
      </DataTable>
      <h2 class="text-base font-semibold">Roles</h2>
      <DataTable
        :columns="roleColumns"
        :rows="roleRows"
        row-key="key"
        page-key="model-roles"
        page-label="Model roles"
      >
        <template #cell-key="{ value }"
          ><span class="font-mono" :title="String(value)">{{ value }}</span></template
        >
        <template #cell-value="{ value, row }"
          >{{ value
          }}<span
            v-if="!usable(row)"
            data-tone="warn"
            class="ml-1 inline-flex items-center gap-1 text-warning"
            ><TriangleAlertIcon class="size-3.5" />entry unusable</span
          ></template
        >
      </DataTable>
      <p class="text-sm text-muted-foreground">
        Entries and roles come from config.yaml. Edit the file on the host, then restart. The model
        of the dashboard session changes with the model chip in Chat.
      </p>
    </template>
  </div>
</template>

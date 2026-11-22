<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import { api } from "../api/client"
import type { ModelEntryRow, ModelsView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"

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

function skipEnd(e: ModelEntry): number {
  const t = new Date(e.skip_until).getTime()
  return Number.isNaN(t) || new Date(e.skip_until).getFullYear() < 2000 ? 0 : t
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
  return { text: "✓ answers now", cls: "c-ok" }
}

function skipRuns(): boolean {
  return (view.value?.entries ?? []).some((e) => skipEnd(e) > now.value)
}

function tick(): void {
  now.value = Date.now()
  if (!skipRuns()) stopTimer()
}

function stopTimer(): void {
  if (timer !== null) clearInterval(timer)
  timer = null
}

async function load(): Promise<void> {
  state.value = "loading"
  try {
    view.value = await api<ModelsView>("GET", "/api/models")
    now.value = Date.now()
    state.value = "ready"
    if (timer === null && skipRuns()) timer = setInterval(tick, 1000)
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

const banner = computed(() => {
  const f = view.value?.fallback
  if (!f || !f.active) return ""
  const at = hhmm(f.failed_at)
  return `⚠ Fallback active. ${f.fallback_name} (${f.fallback_model}) answers. ${f.first_name} failed at ${at}: ${f.cause}.`
})

const columns = [
  { key: "name", label: "Entry" },
  { key: "preset", label: "Preset" },
  { key: "format", label: "Format" },
  { key: "model", label: "Model" },
  { key: "vision", label: "Vision" },
  { key: "context", label: "Context" },
  { key: "effort", label: "Effort" },
  { key: "status", label: "Status" },
]
const rows = computed(() =>
  (view.value?.entries ?? []).map((e) => ({
    name: e.name,
    preset: e.preset,
    format: e.format,
    model: e.model,
    vision: e.vision ? "yes" : "—",
    context:
      e.context_window >= 1000
        ? `${Math.round(e.context_window / 1000)}k`
        : String(e.context_window),
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
  (view.value?.roles ?? []).map((r) => ({ ...r, value: r.value || "—" })),
)

// Narrows the cell value of the status column.
function st(v: unknown): { text: string; cls: string } {
  return v as { text: string; cls: string }
}

onMounted(load)
onBeforeUnmount(stopTimer)
</script>

<template>
  <div class="page">
    <div class="page-h"><h1 class="t-20">Models</h1></div>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="errorText" @retry="load" />
    <StateBlock
      v-else-if="rows.length === 0"
      state="empty"
      text="No model entries in config.yaml."
    />
    <template v-else>
      <div v-if="banner" class="warn-banner">{{ banner }}</div>
      <DataTable :columns="columns" :rows="rows" row-key="name">
        <template #cell-name="{ value }"
          ><b>{{ value }}</b></template
        >
        <template #cell-model="{ value }"
          ><span class="mono">{{ value }}</span></template
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
      </DataTable>
      <p class="pill-note">
        Entries and roles come from config.yaml. Edit the file on the host, then restart. The model
        of the dashboard session changes with the model chip in Chat.
      </p>
    </template>
  </div>
</template>

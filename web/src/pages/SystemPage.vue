<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { DoctorView, McpRow, RuntimeView, Status } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import StateBlock from "../components/StateBlock.vue"
import Tabs from "../components/Tabs.vue"
import { friendlyError } from "../lib/errors"
import { useCommand, type ResultNoteData } from "../lib/command"
import { followTask, useLiveData } from "../lib/live"
import { confirm, toast } from "../stores/ui"

type Load = "loading" | "ready" | "error"

const router = useRouter()
const { run } = useCommand()

const LIST_CAP = 20
const LOST_TEXT = "The gateway restarted. The result of the task is lost."

const doctor = ref<DoctorView | null>(null)
const mcp = ref<McpRow[]>([])
const runtime = ref<RuntimeView | null>(null)
const doctorState = ref<Load>("loading")
const mcpState = ref<Load>("loading")
const runtimeState = ref<Load>("loading")
const doctorError = ref("")
const mcpError = ref("")
const runtimeError = ref("")
const doctorNote = ref<ResultNoteData | null>(null)
const doctorRunning = ref(false)
const mcpNote = ref<ResultNoteData | null>(null)
const mcpBusy = ref(false)
const showAllProcs = ref(false)
const showAllAcp = ref(false)
const openSummary = ref(new Set<string>())
const resumed = new Set<string>()
const stops = new Set<() => void>()

const mcpColumns = [
  { key: "name", label: "Server" },
  { key: "transport", label: "Transport" },
  { key: "status", label: "Status" },
  { key: "tools", label: "Tools", align: "right" as const },
  { key: "classes", label: "Classes" },
  { key: "settings", label: "Settings" },
  { key: "last", label: "Last error" },
]

const marks: Record<string, string> = { ok: "✓", warn: "⚠", fail: "✗" }

// Page names that a hint can link to, with the route name of each.
const pageNames: [string, string][] = [
  ["Approvals and security", "security"],
  ["System operations", "ops"],
  ["Getting started", "start"],
  ["Sessions", "sessions"],
  ["Jobs", "jobs"],
  ["Goals", "goals"],
  ["Skills", "skills"],
  ["Memory", "memory"],
  ["Usage", "usage"],
  ["Models", "models"],
  ["Access", "access"],
  ["System", "system"],
].sort((a, b) => b[0].length - a[0].length) as [string, string][]

const tab = computed({
  get: () => "system",
  set: (v: string) => void router.push({ name: v }),
})

const mcpRows = computed(() =>
  mcp.value.map((m) => ({
    name: m.name,
    transport: m.transport,
    tools: m.tools,
    state: !m.enabled ? "off" : m.connected ? "ok" : m.last_error ? "bad" : "idle",
    label: !m.enabled ? "off" : m.connected ? "connected" : m.last_error ? "error" : "idle",
    classes: classes(m),
    settings: settings(m),
    names: m.tool_names ?? [],
    last: m.last_error ? `${m.last_error} (${date(m.last_error_at)})` : "—",
  })),
)

// Returns the class counts of a server, for example "8 read · 3 write".
function classes(m: McpRow): string {
  const parts = Object.entries(m.classes ?? {})
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${k}`)
  return parts.length ? parts.join(" · ") : "—"
}

// Returns the setting strings of a server.
function settings(m: McpRow): string[] {
  const out = [m.always_on ? "always on" : `stops after ${m.idle_stop_minutes} min idle`]
  if (m.include?.length) out.push(`include: ${m.include.join(", ")}`)
  if (m.exclude?.length) out.push(`exclude: ${m.exclude.join(", ")}`)
  return out
}

function valid(s: string): Date | null {
  const d = new Date(s)
  return !s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? null : d
}

function date(s: string): string {
  const d = valid(s)
  return d ? d.toLocaleString() : "—"
}

const pad = (n: number): string => String(n).padStart(2, "0")

// Returns the local HH:MM of a time, or an empty string.
function hm(s: string): string {
  const d = valid(s)
  return d ? `${pad(d.getHours())}:${pad(d.getMinutes())}` : ""
}

function clock(s: string): string {
  const d = valid(s)
  return d ? d.toLocaleTimeString() : "—"
}

// Returns the number of checks of each status.
function counts(): { ok: number; warn: number; fail: number } {
  const checks = doctor.value?.checks ?? []
  const n = (s: string): number => checks.filter((c) => c.status === s).length
  return { ok: n("ok"), warn: n("warn"), fail: n("fail") }
}

// Splits a hint at the first "the <Name> page", so that the name part becomes a link.
function pageLinks(text: string): { text: string; to?: string }[] {
  for (const [name, to] of pageNames) {
    const needle = `the ${name} page`
    const i = text.indexOf(needle)
    if (i < 0) continue
    const out: { text: string; to?: string }[] = []
    if (i > 0) out.push({ text: text.slice(0, i) })
    out.push({ text: needle, to })
    if (i + needle.length < text.length) out.push({ text: text.slice(i + needle.length) })
    return out
  }
  return [{ text }]
}

function taskLine(b: RuntimeView["batches"][number], i: number): string {
  const r = b.results?.[i]
  const name = b.tasks[i]?.name ?? ""
  if (!r) return `${name} · waiting`
  return `${name} · ${r.status} · ${r.tool_calls} tool calls · ${r.duration_s} s`
}

function batchSummary(b: RuntimeView["batches"][number]): string {
  return (b.results ?? [])
    .map((r) => r.summary)
    .filter(Boolean)
    .join("\n")
}

function toggleSummary(id: string): void {
  const next = new Set(openSummary.value)
  next.add(id)
  openSummary.value = next
}

const procs = computed(() => {
  const all = runtime.value?.processes ?? []
  return showAllProcs.value ? all : all.slice(0, LIST_CAP)
})
const acps = computed(() => {
  const all = runtime.value?.acp ?? []
  return showAllAcp.value ? all : all.slice(0, LIST_CAP)
})

async function loadDoctor(): Promise<void> {
  try {
    doctor.value = await api<DoctorView>("GET", "/api/doctor")
    doctorState.value = "ready"
  } catch (e) {
    doctorError.value = friendlyError(e)
    doctorState.value = "error"
  }
}

async function loadMcp(): Promise<void> {
  try {
    mcp.value = await api<McpRow[]>("GET", "/api/mcp")
    mcpState.value = "ready"
  } catch (e) {
    mcpError.value = friendlyError(e)
    mcpState.value = "error"
  }
}

async function loadRuntime(): Promise<void> {
  try {
    runtime.value = await api<RuntimeView>("GET", "/api/runtime")
    runtimeState.value = "ready"
  } catch (e) {
    runtimeError.value = friendlyError(e)
    runtimeState.value = "error"
  }
}

async function load(): Promise<void> {
  await Promise.all([loadDoctor(), loadMcp(), loadRuntime()])
  resume()
}

// Follows a doctor task by the task event until it ends.
function trackDoctor(id: string): void {
  doctorRunning.value = true
  resumed.add(id)
  doctorNote.value = null
  let stop: (() => void) | null = null
  let ended = false
  const finish = (): void => {
    ended = true
    doctorRunning.value = false
    if (stop) {
      stop()
      stops.delete(stop)
    }
  }
  stop = followTask(id, (t) => {
    if (ended) return
    if (t.state === "done") {
      finish()
      void loadDoctor()
    } else if (t.state === "error" || t.state === "lost") {
      finish()
      const text = t.state === "lost" ? LOST_TEXT : t.text
      doctorNote.value = { title: "Doctor", at: new Date(), text, kind: "error" }
    }
  })
  if (ended) stop()
  else stops.add(stop)
}

async function runDoctor(): Promise<void> {
  if (doctorRunning.value) return
  let id = ""
  try {
    id = (await api<{ task_id: string }>("POST", "/api/doctor")).task_id
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  trackDoctor(id)
}

// Follows a doctor task that already runs, for example one that another tab started.
function resume(): void {
  const id = doctor.value?.task_id
  if (id && !resumed.has(id)) trackDoctor(id)
}

async function reloadMcp(): Promise<void> {
  if (mcpBusy.value) return
  const ok = await confirm({
    title: "Reload the MCP servers?",
    body: "The prompt cache of each session starts again, so the next turn of each session costs more.",
    confirmLabel: "Reload MCP",
  })
  if (!ok) return
  mcpBusy.value = true
  try {
    await run("/reload-mcp", { result: mcpNote, title: "Reload MCP" })
  } finally {
    mcpBusy.value = false
  }
  await loadMcp()
}

const last = { running_batches: 0, running_acp: 0, running_shell: 0, browser_on: false }

// Lets a doctor end and a change of the running work reload the page.
function isLive(name: string, data: unknown): boolean {
  if (name === "task") {
    const t = data as { kind?: string; state?: string } | null
    return t?.kind === "doctor" && (t.state === "done" || t.state === "error")
  }
  if (name === "status") {
    const s = (data ?? {}) as Partial<Status>
    let changed = false
    for (const k of ["running_batches", "running_acp", "running_shell", "browser_on"] as const) {
      if (s[k] === undefined) continue
      if (s[k] !== last[k]) changed = true
      ;(last as Record<string, unknown>)[k] = s[k]
    }
    return changed
  }
  return false
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["task", "status"],
  filter: isLive,
})
onBeforeUnmount(() => {
  stops.forEach((f) => f())
  stops.clear()
})
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">System</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <Tabs
      v-model="tab"
      :tabs="[
        { id: 'system', label: 'Health' },
        { id: 'ops', label: 'Operations' },
      ]"
    />

    <div class="card doctor-card">
      <div class="card-h">
        <h3>Doctor</h3>
        <span v-if="hm(doctor?.ran_at ?? '')" class="c-3 t-12">{{
          `ran ${hm(doctor?.ran_at ?? "")} · ${counts().ok} ✓ · ${counts().warn} ⚠ · ${counts().fail} ✗`
        }}</span>
        <button
          v-if="(doctor?.checks ?? []).length || doctorRunning"
          class="btn btn-secondary"
          type="button"
          :disabled="doctorRunning"
          @click="runDoctor"
        >
          {{ doctorRunning ? "Running…" : "Run again" }}
        </button>
      </div>
      <ResultNote :note="doctorNote" @close="doctorNote = null" />
      <StateBlock v-if="doctorState === 'loading'" state="loading" />
      <StateBlock
        v-else-if="doctorState === 'error'"
        state="error"
        :text="doctorError"
        action="Retry"
        @action="loadDoctor"
      />
      <StateBlock
        v-else-if="!(doctor?.checks ?? []).length && !doctorRunning"
        state="empty"
        text="The doctor has not run yet."
        action="Run doctor"
        @action="runDoctor"
      />
      <template v-else>
        <div class="checks" :class="{ 'is-stale': doctorRunning }">
          <div v-for="(c, i) in doctor?.checks ?? []" :key="i" class="li top">
            <span class="lead">{{ marks[c.status] ?? "•" }}</span>
            <span class="grow">
              <b class="mono">{{ c.name }}</b> <span class="c-2">{{ c.detail }}</span>
              <div v-if="c.next" class="c-3 t-12">
                next:
                <template v-for="(p, j) in pageLinks(c.next)" :key="j"
                  ><router-link v-if="p.to" :to="{ name: p.to }">{{ p.text }}</router-link
                  ><template v-else>{{ p.text }}</template></template
                >
              </div>
            </span>
          </div>
        </div>
        <div class="li c-3 t-12">
          “Run again” makes test calls to each provider. It runs once a minute at most.
        </div>
      </template>
    </div>

    <section class="mcp-section">
      <div class="section-h">
        <h2>MCP servers</h2>
        <button class="btn btn-secondary" type="button" :disabled="mcpBusy" @click="reloadMcp">
          Reload MCP
        </button>
      </div>
      <ResultNote :note="mcpNote" @close="mcpNote = null" />
      <StateBlock v-if="mcpState === 'loading'" state="loading" />
      <StateBlock
        v-else-if="mcpState === 'error'"
        state="error"
        :text="mcpError"
        action="Retry"
        @action="loadMcp"
      />
      <StateBlock
        v-else-if="mcpRows.length === 0"
        state="empty"
        text="No MCP servers. Add one under `mcp` in config.yaml, then tap Reload MCP."
      />
      <DataTable v-else :columns="mcpColumns" :rows="mcpRows" row-key="name">
        <template #cell-status="{ row }">
          <span class="status"><span :class="'dot dot-' + row.state" />{{ row.label }}</span>
        </template>
        <template #cell-tools="{ row }">
          {{ row.tools }}
          <details v-if="(row.names as string[]).length">
            <summary>{{ (row.names as string[]).length }} tools</summary>
            <span v-for="n in row.names as string[]" :key="n" class="mono">{{ n }} </span>
          </details>
        </template>
        <template #cell-settings="{ row }">{{ (row.settings as string[]).join(" · ") }}</template>
      </DataTable>
    </section>

    <div class="section-h"><h2>Running work</h2></div>
    <StateBlock v-if="runtimeState === 'loading'" state="loading" />
    <StateBlock
      v-else-if="runtimeState === 'error'"
      state="error"
      :text="runtimeError"
      action="Retry"
      @action="loadRuntime"
    />
    <div v-else class="grid-2">
      <div class="card">
        <div class="card-h"><h3>🔀 Delegation batches</h3></div>
        <div v-for="b in runtime?.batches ?? []" :key="b.id" class="li top">
          <span class="grow">
            <b class="mono">{{ b.id }}</b>
            <span class="c-2">{{ b.status }} · {{ b.tasks.length }} tasks</span>
            <div v-for="(_t, i) in b.tasks" :key="i" class="li">{{ taskLine(b, i) }}</div>
            <div
              v-for="(r, i) in b.results ?? []"
              v-show="r.error"
              :key="'e' + i"
              class="t-12 c-bad"
            >
              {{ r.error }}
            </div>
            <template v-if="batchSummary(b)">
              <div class="batch-summary c-3 t-12" :class="{ 'clamp-3': !openSummary.has(b.id) }">
                {{ batchSummary(b) }}
              </div>
              <button
                v-if="!openSummary.has(b.id)"
                class="btn btn-ghost btn-sm"
                type="button"
                @click="toggleSummary(b.id)"
              >
                Show all
              </button>
            </template>
          </span>
        </div>
        <div v-if="!(runtime?.batches ?? []).length" class="li c-3">Nothing runs.</div>
      </div>
      <div class="card">
        <div class="card-h"><h3>🤖 ACP sessions</h3></div>
        <div v-for="a in acps" :key="a.session_id" class="li">
          <span class="grow"
            ><b>{{ a.agent }}</b> <span class="c-2">{{ a.busy ? "busy" : "idle" }}</span></span
          >
          <span class="c-3 t-12 mono">{{ a.cwd }}</span>
        </div>
        <div v-if="!(runtime?.acp ?? []).length" class="li c-3">Nothing runs.</div>
        <button
          v-if="!showAllAcp && (runtime?.acp ?? []).length > LIST_CAP"
          class="btn btn-ghost btn-sm"
          type="button"
          @click="showAllAcp = true"
        >
          Show all {{ runtime?.acp.length }}
        </button>
      </div>
      <div class="card">
        <div class="card-h"><h3>💻 Background processes</h3></div>
        <div v-for="p in procs" :key="p.pid" class="li top proc-row">
          <span class="grow">
            <b class="mono">{{ p.pid }}</b> <code>{{ p.command }}</code>
            <div class="c-3 t-12">
              started {{ clock(p.started)
              }}<template v-if="p.exited"> · exit code {{ p.exit_code }}</template>
            </div>
            <div v-if="p.tail" class="log">{{ p.tail }}</div>
          </span>
        </div>
        <div v-if="!(runtime?.processes ?? []).length" class="li c-3">Nothing runs.</div>
        <button
          v-if="!showAllProcs && (runtime?.processes ?? []).length > LIST_CAP"
          class="btn btn-ghost btn-sm"
          type="button"
          @click="showAllProcs = true"
        >
          Show all {{ runtime?.processes.length }}
        </button>
      </div>
      <div class="card">
        <div class="card-h"><h3>🌐 Browser</h3></div>
        <div v-if="runtime?.browser" class="li">running</div>
        <div v-else class="li c-3">closed</div>
      </div>
    </div>
  </div>
</template>

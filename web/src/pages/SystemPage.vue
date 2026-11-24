<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { useRouter } from "vue-router"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
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
import { renderMarkdown } from "../lib/markdown"
import { duration, fullTime, shortTime } from "../lib/time"
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

// The dot color of each MCP server state.
const DOT: Readonly<Record<string, string>> = {
  ok: "bg-success",
  bad: "bg-destructive",
  idle: "bg-muted-foreground",
  off: "bg-border",
}

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
  return shortTime(s) || "—"
}

// Returns a count with the singular or the plural word.
function count(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

const pad = (n: number): string => String(n).padStart(2, "0")

// Returns the local HH:MM of a time, or an empty string.
function hm(s: string): string {
  const d = valid(s)
  return d ? `${pad(d.getHours())}:${pad(d.getMinutes())}` : ""
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
  return `${name} · ${r.status} · ${r.tool_calls} tool calls · ${duration(r.duration_s)}`
}

function batchSummary(b: RuntimeView["batches"][number]): string {
  return (b.results ?? [])
    .map((r) => r.summary)
    .filter(Boolean)
    .join("\n\n")
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
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">System</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <Tabs
      v-model="tab"
      :tabs="[
        { id: 'system', label: 'Health' },
        { id: 'ops', label: 'Operations' },
      ]"
    />

    <Card>
      <CardHeader>
        <CardTitle>Doctor</CardTitle>
        <CardDescription v-if="hm(doctor?.ran_at ?? '')">{{
          `ran ${hm(doctor?.ran_at ?? "")} · ${counts().ok} ✓ · ${counts().warn} ⚠ · ${counts().fail} ✗`
        }}</CardDescription>
        <CardAction v-if="(doctor?.checks ?? []).length || doctorRunning">
          <Button variant="outline" :disabled="doctorRunning" @click="runDoctor">
            <Spinner v-if="doctorRunning" aria-hidden="true" />
            {{ doctorRunning ? "Running…" : "Run again" }}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent class="flex flex-col gap-3">
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
        <ul
          v-else
          aria-label="Doctor checks"
          :aria-busy="doctorRunning"
          :class="cn('flex flex-col gap-2', doctorRunning && 'opacity-60')"
        >
          <li
            v-for="(c, i) in doctor?.checks ?? []"
            :key="i"
            class="flex items-start gap-3 text-sm"
          >
            <span>{{ marks[c.status] ?? "•" }}</span>
            <span class="flex-1">
              <b class="font-mono font-medium">{{ c.name }}</b>
              <span class="text-muted-foreground">{{ c.detail }}</span>
              <div v-if="c.next" class="text-xs text-muted-foreground">
                next:
                <template v-for="(p, j) in pageLinks(c.next)" :key="j"
                  ><router-link v-if="p.to" :to="{ name: p.to }" class="underline">{{
                    p.text
                  }}</router-link
                  ><template v-else>{{ p.text }}</template></template
                >
              </div>
            </span>
          </li>
        </ul>
      </CardContent>
      <CardFooter
        v-if="doctorState === 'ready' && ((doctor?.checks ?? []).length || doctorRunning)"
      >
        <p class="text-xs text-muted-foreground">
          “Run again” makes test calls to each provider. It runs once a minute at most.
        </p>
      </CardFooter>
    </Card>

    <section aria-labelledby="mcp-h" class="flex flex-col gap-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 id="mcp-h" class="text-base font-semibold">MCP servers</h2>
        <Button variant="outline" :disabled="mcpBusy" @click="reloadMcp">Reload MCP</Button>
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
        title="No MCP servers."
        text="Add one under `mcp` in config.yaml, then tap Reload MCP."
      />
      <DataTable v-else :columns="mcpColumns" :rows="mcpRows" row-key="name">
        <template #cell-status="{ row }">
          <span class="inline-flex items-center gap-1.5"
            ><span
              aria-hidden="true"
              :data-tone="row.state"
              :class="cn('size-2 rounded-full', DOT[row.state as string])"
            />{{ row.label }}</span
          >
        </template>
        <template #cell-tools="{ row }">
          <Collapsible v-if="(row.names as string[]).length">
            <CollapsibleTrigger as-child>
              <Button variant="link" size="sm">{{
                count((row.names as string[]).length, "tool", "tools")
              }}</Button>
            </CollapsibleTrigger>
            <CollapsibleContent class="flex flex-wrap gap-x-2 text-left font-mono text-xs">
              <span v-for="n in row.names as string[]" :key="n">{{ n }}</span>
            </CollapsibleContent>
          </Collapsible>
          <template v-else>{{ count(row.tools as number, "tool", "tools") }}</template>
        </template>
        <template #cell-settings="{ row }">{{ (row.settings as string[]).join(" · ") }}</template>
      </DataTable>
    </section>

    <h2 class="text-base font-semibold">Running work</h2>
    <StateBlock v-if="runtimeState === 'loading'" state="loading" />
    <StateBlock
      v-else-if="runtimeState === 'error'"
      state="error"
      :text="runtimeError"
      action="Retry"
      @action="loadRuntime"
    />
    <div v-else class="grid gap-4 tab:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>🔀 Delegation batches</CardTitle></CardHeader>
        <CardContent class="flex flex-col gap-3 text-sm">
          <div v-for="b in runtime?.batches ?? []" :key="b.id" class="flex flex-col gap-1">
            <div>
              <b class="font-mono">{{ b.id }}</b
              >{{ " "
              }}<span class="text-muted-foreground"
                >{{ b.status }} · {{ count(b.tasks.length, "task", "tasks") }}</span
              >
            </div>
            <div v-for="(_t, i) in b.tasks" :key="i">{{ taskLine(b, i) }}</div>
            <div
              v-for="(r, i) in b.results ?? []"
              v-show="r.error"
              :key="'e' + i"
              class="text-xs text-destructive"
            >
              {{ r.error }}
            </div>
            <template v-if="batchSummary(b)">
              <div
                :class="
                  cn(
                    'typeset typeset-chat text-xs text-muted-foreground',
                    !openSummary.has(b.id) && 'line-clamp-3',
                  )
                "
                v-html="renderMarkdown(batchSummary(b))"
              />
              <div v-if="!openSummary.has(b.id)">
                <Button variant="ghost" size="sm" @click="toggleSummary(b.id)">Show all</Button>
              </div>
            </template>
          </div>
          <p v-if="!(runtime?.batches ?? []).length" class="text-sm text-muted-foreground">
            Nothing runs.
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>🤖 ACP sessions</CardTitle></CardHeader>
        <CardContent class="flex flex-col gap-3 text-sm">
          <div v-for="a in acps" :key="a.session_id" class="flex flex-col gap-0.5">
            <span
              ><b>{{ a.agent }}</b>
              <span class="text-muted-foreground">{{ a.busy ? "busy" : "idle" }}</span></span
            >
            <span class="font-mono text-xs text-muted-foreground">{{ a.cwd }}</span>
          </div>
          <p v-if="!(runtime?.acp ?? []).length" class="text-sm text-muted-foreground">
            Nothing runs.
          </p>
          <div v-if="!showAllAcp && (runtime?.acp ?? []).length > LIST_CAP">
            <Button variant="ghost" size="sm" @click="showAllAcp = true">
              Show all {{ runtime?.acp.length }}
            </Button>
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>💻 Background processes</CardTitle></CardHeader>
        <CardContent class="flex flex-col gap-3 text-sm">
          <ul v-if="procs.length" aria-label="Background processes" class="flex flex-col gap-3">
            <li v-for="p in procs" :key="p.pid">
              <b class="font-mono">{{ p.pid }}</b> <code>{{ p.command }}</code>
              <div class="text-xs text-muted-foreground">
                started
                <span class="whitespace-nowrap" :title="fullTime(p.started)">{{
                  date(p.started)
                }}</span
                ><template v-if="p.exited"> · exit code {{ p.exit_code }}</template>
              </div>
              <pre
                v-if="p.tail"
                class="max-h-40 overflow-auto rounded-md bg-muted p-2 font-mono text-xs"
                >{{ p.tail }}</pre>
            </li>
          </ul>
          <p v-else class="text-sm text-muted-foreground">Nothing runs.</p>
          <div v-if="!showAllProcs && (runtime?.processes ?? []).length > LIST_CAP">
            <Button variant="ghost" size="sm" @click="showAllProcs = true">
              Show all {{ runtime?.processes.length }}
            </Button>
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>🌐 Browser</CardTitle></CardHeader>
        <CardContent class="text-sm">
          <span v-if="runtime?.browser">running</span>
          <span v-else class="text-muted-foreground">closed</span>
        </CardContent>
      </Card>
    </div>
  </div>
</template>

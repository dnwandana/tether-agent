<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import { api, ApiError, runCommand } from "../api/client"
import type { DoctorView, McpRow, ReplyView, RuntimeView, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"
import { toast } from "../stores/ui"

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const doctor = ref<DoctorView | null>(null)
const mcp = ref<McpRow[]>([])
const runtime = ref<RuntimeView | null>(null)
const doctorRunning = ref(false)

let pollTimer: ReturnType<typeof setInterval> | null = null

const mcpColumns = [
  { key: "name", label: "Server" },
  { key: "transport", label: "Transport" },
  { key: "status", label: "Status" },
  { key: "tools", label: "Tools", align: "right" as const },
  { key: "classes", label: "Classes" },
  { key: "last", label: "Last error" },
]

const marks: Record<string, string> = { ok: "✓", warn: "⚠", fail: "✗" }

const mcpRows = computed(() =>
  mcp.value.map((m) => ({
    name: m.name,
    transport: m.transport,
    tools: m.tools,
    state: m.connected ? "ok" : m.last_error ? "bad" : "idle",
    label: m.connected ? "connected" : m.last_error ? "error" : "idle",
    classes: classes(m),
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

function batchSummary(b: RuntimeView["batches"][number]): string {
  return (b.results ?? [])
    .map((r) => r.summary)
    .filter(Boolean)
    .join(" · ")
}

async function load(): Promise<void> {
  try {
    const [d, m, r] = await Promise.all([
      api<DoctorView>("GET", "/api/doctor"),
      api<McpRow[]>("GET", "/api/mcp"),
      api<RuntimeView>("GET", "/api/runtime"),
    ])
    doctor.value = d
    mcp.value = m
    runtime.value = r
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

function stopPoll(): void {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function finishDoctor(): Promise<void> {
  stopPoll()
  try {
    doctor.value = await api<DoctorView>("GET", "/api/doctor")
  } catch (e) {
    doctorRunning.value = false
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return
  }
  doctorRunning.value = false
  const checks = doctor.value?.checks ?? []
  const n = (s: string): number => checks.filter((c) => c.status === s).length
  toast(`Doctor: ${n("ok")} ✓ · ${n("warn")} ⚠ · ${n("fail")} ✗.`, "ok")
}

// Polls the doctor task every 2 s for 90 s at most.
function followDoctor(taskId: string): void {
  doctorRunning.value = true
  const start = Date.now()
  // Skips a tick while the last GET runs, so that finishDoctor runs one time only.
  let busy = false
  stopPoll()
  pollTimer = setInterval(async () => {
    if (busy) return
    busy = true
    try {
      const t = await api<TaskView>("GET", `/api/tasks/${taskId}`)
      if (t.state === "error") {
        stopPoll()
        doctorRunning.value = false
        toast(t.text, "error")
        return
      }
      if (t.state === "done") {
        await finishDoctor()
        return
      }
    } catch {
      // Keep the poll. The next try can work.
    } finally {
      busy = false
    }
    if (pollTimer !== null && Date.now() - start >= 90000) {
      stopPoll()
      doctorRunning.value = false
      toast("The doctor did not finish in 90 s. Reload the page later.", "error")
    }
  }, 2000)
}

async function runDoctor(): Promise<void> {
  if (doctorRunning.value) return
  let taskId = ""
  try {
    taskId = (await api<{ task_id: string }>("POST", "/api/doctor")).task_id
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return
  }
  followDoctor(taskId)
}

// Follows a doctor task that already runs, for example one that another tab started.
async function resumeDoctor(): Promise<void> {
  const id = doctor.value?.task_id
  if (!id) return
  try {
    const t = await api<TaskView>("GET", `/api/tasks/${id}`)
    if (t.state === "running") followDoctor(id)
  } catch {
    // The task is gone. Show the kept result.
  }
}

async function act(text: string): Promise<void> {
  try {
    const r = await runCommand(text)
    if (!("text" in r) || r.text === undefined) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      toast((r as ReplyView).text, "ok")
    }
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

onMounted(async () => {
  await load()
  await resumeDoctor()
})
onBeforeUnmount(stopPoll)
</script>

<template>
  <div class="page">
    <div class="page-h"><h1 class="t-20">System</h1></div>
    <div class="tabs">
      <router-link :to="{ name: 'system' }" class="tab is-on">Health</router-link>
      <router-link :to="{ name: 'ops' }" class="tab">Operations</router-link>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @retry="load"
    />
    <template v-else>
      <div class="card">
        <div class="card-h">
          <h3>Doctor</h3>
          <span class="c-3 t-12">{{
            hm(doctor?.ran_at ?? "") ? "last run " + hm(doctor?.ran_at ?? "") : "not run yet"
          }}</span>
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="doctorRunning"
            @click="runDoctor"
          >
            {{ doctorRunning ? "Running…" : "Run again" }}
          </button>
        </div>
        <div v-for="(c, i) in doctor?.checks ?? []" :key="i" class="li top">
          <span class="lead">{{ marks[c.status] ?? "•" }}</span>
          <span class="grow">
            <b class="mono">{{ c.name }}</b> <span class="c-2">{{ c.detail }}</span>
            <div v-if="c.next" class="c-3 t-12">next: {{ c.next }}</div>
          </span>
        </div>
        <div class="li c-3 t-12">
          “Run again” makes test calls to each provider. It runs once a minute at most.
        </div>
      </div>

      <div class="section-h">
        <h2>MCP servers</h2>
        <button class="btn btn-secondary" type="button" @click="act('/reload-mcp')">
          Reload MCP
        </button>
      </div>
      <p class="c-3 t-12">
        The prompt cache of every session starts again, so the next turn costs more.
      </p>
      <StateBlock v-if="mcpRows.length === 0" state="empty" text="No MCP servers." />
      <DataTable v-else :columns="mcpColumns" :rows="mcpRows" row-key="name">
        <template #cell-status="{ row }">
          <span class="status"><span :class="'dot ' + row.state" />{{ row.label }}</span>
        </template>
      </DataTable>

      <div class="section-h"><h2>Running work</h2></div>
      <div class="grid-2">
        <div class="card">
          <div class="card-h"><h3>🔀 Delegation batches</h3></div>
          <div v-for="b in runtime?.batches ?? []" :key="b.id" class="li top">
            <span class="grow"
              ><b class="mono">{{ b.id }}</b>
              <span class="c-2">{{ b.status }} · {{ b.tasks.length }} tasks</span>
              <div v-if="batchSummary(b)" class="c-3 t-12">{{ batchSummary(b) }}</div></span
            >
          </div>
          <div v-if="!(runtime?.batches ?? []).length" class="li c-3">Nothing runs.</div>
        </div>
        <div class="card">
          <div class="card-h"><h3>🤖 ACP sessions</h3></div>
          <div v-for="a in runtime?.acp ?? []" :key="a.session_id" class="li">
            <span class="grow"
              ><b>{{ a.agent }}</b> <span class="c-2">{{ a.busy ? "busy" : "idle" }}</span></span
            >
            <span class="c-3 t-12 mono">{{ a.cwd }}</span>
          </div>
          <div v-if="!(runtime?.acp ?? []).length" class="li c-3">Nothing runs.</div>
        </div>
        <div class="card">
          <div class="card-h"><h3>💻 Background processes</h3></div>
          <div v-for="p in runtime?.processes ?? []" :key="p.pid" class="li top">
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
        </div>
        <div class="card">
          <div class="card-h"><h3>🌐 Browser</h3></div>
          <div v-if="runtime?.browser" class="li">running</div>
          <div v-else class="li c-3">closed</div>
        </div>
      </div>
    </template>
  </div>
</template>

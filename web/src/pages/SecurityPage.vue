<script setup lang="ts">
import { computed, onMounted, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, runCommand } from "../api/client"
import type { LogEntry, LogPage, ReplyView, SecurityView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import DevicesTable from "../components/DevicesTable.vue"
import StateBlock from "../components/StateBlock.vue"
import { confirm, toast } from "../stores/ui"

type Tab = "modes" | "grants" | "log" | "devices" | "secrets" | "rules"
const TABS: Tab[] = ["modes", "grants", "log", "devices", "secrets", "rules"]
const PAGE = 20

const route = useRoute()
const router = useRouter()

const tab = computed<Tab>(() => {
  const q = route.query.tab
  const v = Array.isArray(q) ? q[0] : q
  return TABS.includes(v as Tab) ? (v as Tab) : "modes"
})

const view = ref<SecurityView | null>(null)
const state = ref<"loading" | "error" | "ready">("loading")
const cause = ref("")
const log = ref<LogPage | null>(null)
const logScope = ref("")
const logOffset = ref(0)
const logState = ref<"loading" | "error" | "ready">("loading")
const logCause = ref("")

const MODE_LABEL: Record<string, string> = {
  "read-only": "👁 Read-only",
  guarded: "🛡 Guarded",
  full: "⚡ Full",
}
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

// Pads a number to two digits.
function pad2(n: number): string {
  return String(n).padStart(2, "0")
}

// Formats a time as "Nov 20 09:12". A zero time gives an empty text.
function stamp(iso: string | undefined): string {
  if (!iso) return ""
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return ""
  return `${MONTHS[d.getMonth()]} ${d.getDate()} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

async function load(): Promise<void> {
  try {
    view.value = await api<SecurityView>("GET", "/api/security")
    state.value = "ready"
  } catch (e) {
    cause.value = errText(e)
    state.value = "error"
  }
}

async function loadLog(offset: number): Promise<void> {
  logOffset.value = offset
  logState.value = "loading"
  try {
    const qs = new URLSearchParams({
      scope: logScope.value,
      offset: String(offset),
      limit: String(PAGE),
    })
    log.value = await api<LogPage>("GET", "/api/security/log?" + qs.toString())
    logState.value = "ready"
  } catch (e) {
    logCause.value = errText(e)
    logState.value = "error"
  }
}

async function act(text: string): Promise<void> {
  try {
    const r = await runCommand(text)
    if (!("text" in r)) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const ask = reply.buttons.flat().find((b) => b.data.endsWith("confirm"))
      if (ask) {
        const [title, ...rest] = reply.text.split("\n")
        const ok = await confirm({
          title,
          body: rest.join("\n").trim(),
          confirmLabel: ask.text,
          danger: true,
        })
        if (ok) await act(ask.data)
        return
      }
      toast(reply.text, "ok")
    }
  } catch (e) {
    toast(errText(e), "error")
  }
  await load()
  if (tab.value === "log") await loadLog(logOffset.value)
}

function setTab(t: Tab): void {
  void router.replace({ query: { ...route.query, tab: t } })
  if (t === "log" && log.value === null) void loadLog(0)
}

function setScope(s: string): void {
  logScope.value = s
  void loadLog(0)
}

const modeRows = computed(() => {
  const v = view.value
  if (!v) return []
  const dash = v.modes.find((m) => m.scope === "dashboard")
  const rows = [{ scope: "dashboard", mode: v.mode, set_at: dash?.set_at ?? "", dashboard: true }]
  for (const m of v.modes) if (m.scope !== "dashboard") rows.push({ ...m, dashboard: false })
  return rows as Record<string, unknown>[]
})
const grantRows = computed(
  () => (view.value?.grants ?? []).map((g, i) => ({ ...g, idx: i })) as Record<string, unknown>[],
)
const secretRows = computed(
  () => (view.value?.secrets ?? []) as unknown as Record<string, unknown>[],
)
const logRows = computed(
  () => (log.value?.rows ?? []).map((r, i) => ({ ...r, idx: i })) as Record<string, unknown>[],
)
const logTo = computed(() => (log.value?.offset ?? 0) + (log.value?.rows.length ?? 0))
const logTotal = computed(() => log.value?.total ?? 0)
const mcpRows = computed(() => {
  const out: { key: string; server: string; tool: string; class: string }[] = []
  for (const [server, classes] of Object.entries(view.value?.fixed.mcp_classes ?? {}))
    for (const [cls, n] of Object.entries(classes))
      out.push({
        key: server + ":" + cls,
        server,
        tool: n === 1 ? "1 tool" : `${n} tools`,
        class: cls,
      })
  return out as Record<string, unknown>[]
})

const modeCols = [
  { key: "scope", label: "Scope" },
  { key: "mode", label: "Mode" },
  { key: "actions", label: "", align: "right" as const },
]
const grantCols = [
  { key: "rule", label: "Rule" },
  { key: "preview", label: "Command" },
  { key: "workdir", label: "Workdir" },
  { key: "via", label: "Via" },
  { key: "programs", label: "Programs" },
  { key: "mcp", label: "MCP" },
  { key: "created", label: "Added" },
  { key: "actions", label: "", align: "right" as const },
]
const logCols = [
  { key: "at", label: "Time" },
  { key: "scope", label: "Scope" },
  { key: "kind", label: "Kind" },
  { key: "tool", label: "Tool" },
  { key: "rule", label: "Rule" },
  { key: "class", label: "Class" },
  { key: "decision", label: "Decision" },
  { key: "source", label: "Source" },
  { key: "answer", label: "Answer" },
  { key: "preview", label: "Preview" },
]
const secretCols = [
  { key: "name", label: "Name" },
  { key: "state", label: "State" },
  { key: "commands", label: "Bound to" },
  { key: "programs", label: "Programs" },
  { key: "actions", label: "", align: "right" as const },
]
const mcpCols = [
  { key: "server", label: "Server" },
  { key: "tool", label: "Tools" },
  { key: "class", label: "Class" },
]

const DECISION: Record<string, string> = {
  allowed: "chip-ok",
  denied: "chip-bad",
  blocked: "chip-warn",
}

onMounted(() => {
  void load()
  if (tab.value === "log") void loadLog(0)
})
</script>

<template>
  <div class="page">
    <div class="page-h"><h1 class="t-20">Approvals and security</h1></div>
    <div class="tabs" role="tablist">
      <button
        type="button"
        role="tab"
        :class="{ 'is-on': tab === 'modes' }"
        @click="setTab('modes')"
      >
        Modes
      </button>
      <button
        type="button"
        role="tab"
        :class="{ 'is-on': tab === 'grants' }"
        @click="setTab('grants')"
      >
        Always grants
        <span v-if="view && view.grants.length" class="badge">{{ view.grants.length }}</span>
      </button>
      <button type="button" role="tab" :class="{ 'is-on': tab === 'log' }" @click="setTab('log')">
        Approval log
      </button>
      <button
        type="button"
        role="tab"
        :class="{ 'is-on': tab === 'devices' }"
        @click="setTab('devices')"
      >
        Signed-in devices
      </button>
      <button
        type="button"
        role="tab"
        :class="{ 'is-on': tab === 'secrets' }"
        @click="setTab('secrets')"
      >
        Secrets
      </button>
      <button
        type="button"
        role="tab"
        :class="{ 'is-on': tab === 'rules' }"
        @click="setTab('rules')"
      >
        Rules
      </button>
    </div>

    <DevicesTable v-if="tab === 'devices'" />
    <StateBlock v-else-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @retry="load" />
    <template v-else-if="view">
      <!-- Modes -->
      <div v-if="tab === 'modes'">
        <DataTable :columns="modeCols" :rows="modeRows" row-key="scope">
          <template #cell-scope="{ row }">
            {{ row.scope }}
            <span v-if="row.dashboard" class="chip chip-acc">this dashboard</span>
          </template>
          <template #cell-mode="{ row }">
            {{ MODE_LABEL[row.mode as string] ?? row.mode
            }}<template v-if="stamp(row.set_at as string)">
              · since {{ stamp(row.set_at as string) }}</template
            >
          </template>
          <template #cell-actions="{ row }">
            <div v-if="row.dashboard" class="seg">
              <button
                type="button"
                :class="{ 'is-on': row.mode === 'read-only' }"
                @click="act('/mode read-only')"
              >
                👁 Read-only
              </button>
              <button
                type="button"
                :class="{ 'is-on': row.mode === 'guarded' }"
                @click="act('/mode guarded')"
              >
                🛡 Guarded
              </button>
              <button
                type="button"
                :class="{ 'is-on': row.mode === 'full' }"
                @click="act('/mode full')"
              >
                ⚡ Full…
              </button>
            </div>
            <span v-else>read-only · change it in that chat</span>
          </template>
        </DataTable>
        <div class="card card-pad">
          <b>Process mode</b> <span class="chip chip-plain">{{ view.process_mode }}</span>
          <div>Set in <code>approvals.mode</code>.</div>
        </div>
        <div class="card card-pad">
          <b>Cron mode</b> <span class="chip chip-plain">{{ view.cron_mode }}</span>
          <div>Set in <code>approvals.cron_mode</code>.</div>
        </div>
      </div>

      <!-- Always grants -->
      <div v-else-if="tab === 'grants'">
        <div v-if="view.grants_off" class="bad-banner">
          Saved answers are off, because the answer store is damaged: {{ view.grants_off_cause }}.
          Restart the gateway after you repair it.
        </div>
        <div class="page-h">
          <span>“✅ Always” answers. Session grants live in memory and are not listed.</span>
          <button
            class="btn btn-danger-soft"
            type="button"
            :disabled="view.grants.length === 0"
            @click="act(view.remove_all)"
          >
            Remove all…
          </button>
        </div>
        <StateBlock v-if="view.grants.length === 0" state="empty" text="No Always grants" />
        <p v-if="view.grants.length === 0" class="hint">
          An “✅ Always” answer on an approval card adds a grant here.
        </p>
        <DataTable v-else :columns="grantCols" :rows="grantRows" row-key="idx">
          <template #cell-preview="{ row }"
            ><code>{{ row.preview }}</code></template
          >
          <template #cell-programs="{ row }">{{ (row.programs as string[]).join(", ") }}</template>
          <template #cell-mcp="{ row }">{{
            [row.server, row.tool_name].filter(Boolean).join(" · ")
          }}</template>
          <template #cell-created="{ row }">{{ stamp(row.created as string) }}</template>
          <template #cell-actions="{ row }">
            <button
              class="btn btn-danger-soft btn-sm"
              type="button"
              @click="act(row.remove as string)"
            >
              Remove
            </button>
          </template>
        </DataTable>
      </div>

      <!-- Approval log -->
      <div v-else-if="tab === 'log'">
        <div class="page-h">
          <select
            class="input"
            aria-label="Scope"
            :value="logScope"
            @change="setScope(($event.target as HTMLSelectElement).value)"
          >
            <option value="">All scopes</option>
            <option v-for="m in view.modes" :key="m.scope" :value="m.scope">{{ m.scope }}</option>
          </select>
          <span>Kept 180 days.</span>
        </div>
        <StateBlock v-if="logState === 'loading' && !log" state="loading" />
        <StateBlock
          v-else-if="logState === 'error'"
          state="error"
          :text="logCause"
          @retry="loadLog(logOffset)"
        />
        <template v-else>
          <DataTable :columns="logCols" :rows="logRows" row-key="idx" class="table-dense">
            <template #cell-at="{ row }">{{ stamp(row.at as string) }}</template>
            <template #cell-class="{ row }">{{
              (row as unknown as LogEntry).class || "—"
            }}</template>
            <template #cell-decision="{ row }">
              <span class="chip" :class="DECISION[row.decision as string] ?? 'chip-plain'">{{
                row.decision
              }}</span>
            </template>
          </DataTable>
          <div class="pager">
            <span class="grow"
              >{{ logTotal === 0 ? "0" : (log?.offset ?? 0) + 1 }}–{{ logTo }} of
              {{ logTotal }}</span
            >
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="logOffset === 0"
              @click="loadLog(Math.max(0, logOffset - PAGE))"
            >
              ◀ Prev
            </button>
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="logTo >= logTotal"
              @click="loadLog(logOffset + PAGE)"
            >
              Next ▶
            </button>
          </div>
        </template>
      </div>

      <!-- Secrets -->
      <div v-else-if="tab === 'secrets'">
        <p>Bound exec_secrets names. Values are never shown here, in a log, or to the model.</p>
        <DataTable :columns="secretCols" :rows="secretRows" row-key="name">
          <template #cell-state="{ row }">
            <span v-if="row.state === 'set'" class="chip chip-ok">✓ set</span>
            <span v-else-if="row.state === 'check'" class="chip chip-warn"
              >✓ set · auth failed {{ stamp(row.failed_at as string) }}</span
            >
            <span v-else class="chip chip-warn">not set</span>
          </template>
          <template #cell-commands="{ row }">{{ (row.commands as string[]).join(", ") }}</template>
          <template #cell-programs="{ row }">{{ (row.programs as string[]).join(", ") }}</template>
          <template #cell-actions="{ row }">
            <button class="btn btn-secondary btn-sm" type="button" @click="act(row.set as string)">
              Set value…
            </button>
            <button
              v-if="(row.programs as string[]).length"
              class="btn btn-danger-soft btn-sm"
              type="button"
              @click="act(row.unpin as string)"
            >
              Unpin
            </button>
          </template>
        </DataTable>
      </div>

      <!-- Rules -->
      <div v-else>
        <div class="card card-pad">
          <b>Protected paths</b>
          <div class="code-block">{{ view.fixed.protected_paths.join("\n") }}</div>
        </div>
        <div class="card card-pad">
          <b>Human-only rules</b>
          <ul class="list">
            <li v-for="r in view.fixed.human_only" :key="r">🔒 {{ r }}</li>
          </ul>
        </div>
        <div class="card card-pad">
          <b>MCP classes</b>
          <DataTable :columns="mcpCols" :rows="mcpRows" row-key="key" />
        </div>
        <div class="card card-pad">
          <b>ACP work folders</b>
          <div class="code-block">{{ view.fixed.acp_workdirs.join("\n") }}</div>
        </div>
        <div class="card card-pad">
          <b>Toolsets</b>
          <ul class="list">
            <li v-for="(sets, ch) in view.fixed.toolsets" :key="ch">
              <b>{{ ch }}</b
              >: {{ sets.join(", ") }}
            </li>
          </ul>
        </div>
      </div>
    </template>
  </div>
</template>

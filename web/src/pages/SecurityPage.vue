<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { SecurityView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import ApprovalLog from "../components/security/ApprovalLog.vue"
import RulesTab from "../components/security/RulesTab.vue"
import SecretsTab from "../components/security/SecretsTab.vue"
import DevicesTable from "../components/DevicesTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import StateBlock from "../components/StateBlock.vue"
import Tabs from "../components/Tabs.vue"
import { friendlyError } from "../lib/errors"
import { useCommand, type ResultNoteData } from "../lib/command"
import { useLiveData } from "../lib/live"
import { confirm } from "../stores/ui"

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

async function load(): Promise<void> {
  try {
    view.value = await api<SecurityView>("GET", "/api/security")
    state.value = "ready"
  } catch (e) {
    cause.value = friendlyError(e)
    state.value = "error"
  }
}

const { run } = useCommand()
const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["card.done"],
  filter: (_n, d) => ["secret", "approval"].includes((d as { kind?: string }).kind ?? ""),
})
const modesNote = ref<ResultNoteData | null>(null)
const grantsNote = ref<ResultNoteData | null>(null)
// A template unwraps a ref, so the commands name the note with a key.
const notes = { modes: modesNote, grants: grantsNote }
type NoteKey = keyof typeof notes

// Runs a command, shows the reply in the note of the tab, and loads the data again.
async function command(text: string, which: NoteKey, title: string): Promise<void> {
  await run(text, { result: notes[which], title })
  await refresh()
}

// Sets a mode. The Full mode asks first, and then sends the confirm form of the command.
async function setMode(m: "read-only" | "guarded" | "full"): Promise<void> {
  if (m !== "full") return command(`/mode ${m}`, "modes", "Mode")
  const now = MODE_LABEL[view.value?.mode ?? ""] ?? view.value?.mode ?? ""
  const ok = await confirm({
    title: "Set ⚡ Full for the Dashboard?",
    body:
      `Now: ${now}.\n` +
      "Dangerous commands run with no question in this scope. Human-only rules still ask.\n" +
      "A script can then add a dashboard session.",
    confirmLabel: "⚡ Set Full",
    danger: true,
  })
  if (ok) await command("/mode full confirm", "modes", "Mode")
}

function setTab(t: Tab): void {
  void router.replace({ query: { ...route.query, tab: t } })
}

const modeRows = computed(() => {
  const v = view.value
  if (!v) return []
  const dash = v.modes.find((m) => m.scope === "dashboard")
  const rows = [
    {
      scope: "dashboard",
      scope_label: dash?.scope_label || "Dashboard",
      mode: v.mode,
      set_at: dash?.set_at ?? "",
      dashboard: true,
    },
  ]
  for (const m of v.modes) if (m.scope !== "dashboard") rows.push({ ...m, dashboard: false })
  return rows as Record<string, unknown>[]
})
const grantOffset = ref(0)
const grantRows = computed(
  () => (view.value?.grants ?? []).map((g, i) => ({ ...g, idx: i })) as Record<string, unknown>[],
)
// Moves the offset to the last page when the rows shrink, for example after a Remove.
watch(
  () => grantRows.value.length,
  (n) => {
    if (grantOffset.value >= n) grantOffset.value = Math.max(0, Math.floor((n - 1) / PAGE) * PAGE)
  },
)
const grantPage = computed(() => grantRows.value.slice(grantOffset.value, grantOffset.value + PAGE))
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
const tabs = computed(() => [
  { id: "modes", label: "Modes" },
  { id: "grants", label: "Always answers", count: view.value?.grants.length },
  { id: "log", label: "Approval log" },
  { id: "devices", label: "Signed-in devices" },
  { id: "secrets", label: "Secrets" },
  { id: "rules", label: "Rules" },
])
const tabModel = computed({
  get: () => tab.value,
  set: (t: string) => setTab(t as Tab),
})
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Approvals and security</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <Tabs v-model="tabModel" :tabs="tabs">
      <div :aria-busy="stale || undefined">
        <DevicesTable v-if="tab === 'devices'" />
        <StateBlock v-else-if="state === 'loading'" state="loading" />
        <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="refresh" />
        <template v-else-if="view">
          <!-- Modes -->
          <div v-if="tab === 'modes'">
            <DataTable :columns="modeCols" :rows="modeRows" row-key="scope">
              <template #cell-scope="{ row }">
                {{ row.scope_label || row.scope }}
                <span v-if="row.dashboard" class="chip chip-acc">this dashboard</span>
              </template>
              <template #cell-mode="{ row }">
                {{ MODE_LABEL[row.mode as string] ?? row.mode
                }}<template v-if="stamp(row.set_at as string)">
                  · since {{ stamp(row.set_at as string) }}</template
                >
              </template>
              <template #cell-actions="{ row }">
                <div
                  v-if="row.dashboard"
                  class="seg seg-touch"
                  role="group"
                  aria-label="Mode of the Dashboard"
                >
                  <button
                    type="button"
                    :class="{ 'is-on': row.mode === 'read-only' }"
                    :aria-pressed="row.mode === 'read-only'"
                    @click="setMode('read-only')"
                  >
                    👁 Read-only
                  </button>
                  <button
                    type="button"
                    :class="{ 'is-on': row.mode === 'guarded' }"
                    :aria-pressed="row.mode === 'guarded'"
                    @click="setMode('guarded')"
                  >
                    🛡 Guarded
                  </button>
                  <button
                    type="button"
                    :class="{ 'is-on': row.mode === 'full' }"
                    :aria-pressed="row.mode === 'full'"
                    @click="setMode('full')"
                  >
                    ⚡ Full
                  </button>
                </div>
                <span v-else class="c-3">View only. Send /mode in that chat.</span>
              </template>
            </DataTable>
            <ResultNote :note="modesNote" @close="modesNote = null" />
            <div class="card card-pad">
              <b>Process mode</b> <span class="chip chip-plain">{{ view.process_mode }}</span>
              <div>Set in <code>approvals.mode</code>.</div>
            </div>
            <div class="card card-pad">
              <b>Cron mode</b> <span class="chip chip-plain">{{ view.cron_mode }}</span>
              <div>Set in <code>approvals.cron_mode</code>.</div>
            </div>
          </div>

          <!-- Always answers -->
          <div v-else-if="tab === 'grants'">
            <div v-if="view.grants_off" class="bad-banner">
              Saved answers are off, because the answer store is damaged:
              {{ view.grants_off_cause }}. Restart the gateway after you repair it.
            </div>
            <div class="page-h">
              <span>“✅ Always” answers. Session answers live in memory and are not listed.</span>
              <button
                class="btn btn-danger-soft"
                type="button"
                :disabled="view.grants.length === 0"
                @click="command(view.remove_all, 'grants', 'Remove all')"
              >
                Remove all…
              </button>
            </div>
            <ResultNote :note="grantsNote" @close="grantsNote = null" />
            <StateBlock
              v-if="view.grants.length === 0"
              state="empty"
              text="An ✅ Always answer shows here."
            />
            <template v-else>
              <DataTable :columns="grantCols" :rows="grantPage" row-key="idx">
                <template #cell-rule="{ row }">
                  <b>{{ row.rule }}</b>
                  <div class="t-12 c-3">{{ row.description }}</div>
                </template>
                <template #cell-preview="{ row }"
                  ><code>{{ row.preview }}</code></template
                >
                <template #cell-programs="{ row }">{{
                  (row.programs as string[]).join(", ")
                }}</template>
                <template #cell-mcp="{ row }">{{
                  [row.server, row.tool_name].filter(Boolean).join(" · ")
                }}</template>
                <template #cell-created="{ row }">{{ stamp(row.created as string) }}</template>
                <template #cell-actions="{ row }">
                  <button
                    class="btn btn-danger-soft btn-sm"
                    type="button"
                    @click="command(row.remove as string, 'grants', 'Remove')"
                  >
                    Remove
                  </button>
                </template>
              </DataTable>
              <div class="pager">
                <span class="grow"
                  >{{ grantOffset + 1 }}–{{ grantOffset + grantPage.length }} of
                  {{ grantRows.length }}</span
                >
                <button
                  class="btn btn-secondary btn-sm"
                  type="button"
                  :disabled="grantOffset === 0"
                  @click="grantOffset = Math.max(0, grantOffset - PAGE)"
                >
                  ◀ Prev
                </button>
                <button
                  class="btn btn-secondary btn-sm"
                  type="button"
                  :disabled="grantOffset + PAGE >= grantRows.length"
                  @click="grantOffset += PAGE"
                >
                  Next ▶
                </button>
              </div>
            </template>
          </div>

          <!-- Approval log -->
          <ApprovalLog v-else-if="tab === 'log'" :scopes="view.log_scopes ?? []" />

          <SecretsTab v-else-if="tab === 'secrets'" :secrets="view.secrets" @changed="refresh" />

          <!-- Rules -->
          <RulesTab
            v-else
            :fixed="view.fixed"
            :process-mode="view.process_mode"
            :cron-mode="view.cron_mode"
          />
        </template>
      </div>
    </Tabs>
  </div>
</template>

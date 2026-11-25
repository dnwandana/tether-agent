<script setup lang="ts">
import { computed, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
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
import type { Column } from "../lib/table"
import { fullTime, shortTime } from "../lib/time"
import { useCommand, type ResultNoteData } from "../lib/command"
import { useLiveData } from "../lib/live"
import { confirm } from "../stores/ui"

type Tab = "modes" | "grants" | "log" | "devices" | "secrets" | "rules"
const TABS: Tab[] = ["modes", "grants", "log", "devices", "secrets", "rules"]

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

// Sets the mode from the toggle group. A tap on the pressed item gives no value.
function onMode(v: unknown): void {
  if (v === "read-only" || v === "guarded" || v === "full") void setMode(v)
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
const grantRows = computed(
  () => (view.value?.grants ?? []).map((g, i) => ({ ...g, idx: i })) as Record<string, unknown>[],
)
const modeCols: Column[] = [
  { key: "scope", label: "Scope", type: "text" },
  { key: "mode", label: "Mode", type: "text" },
  { key: "set", label: "", type: "block" },
]
const grantCols: Column[] = [
  { key: "rule", label: "Rule", type: "block" },
  { key: "preview", label: "Command", type: "text" },
  { key: "workdir", label: "Workdir", type: "text" },
  { key: "via", label: "Via", type: "text", optional: true },
  { key: "programs", label: "Programs", type: "text", optional: true },
  { key: "mcp", label: "MCP", type: "text", optional: true },
  { key: "created", label: "Added", type: "date" },
  { key: "actions", label: "", type: "act", width: "7rem" },
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
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Approvals and security</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <Tabs v-model="tabModel" :tabs="tabs">
      <div :aria-busy="stale || undefined">
        <DevicesTable v-if="tab === 'devices'" />
        <StateBlock v-else-if="state === 'loading'" state="loading" />
        <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="refresh" />
        <template v-else-if="view">
          <!-- Modes -->
          <div v-if="tab === 'modes'" class="flex flex-col gap-4">
            <DataTable
              :columns="modeCols"
              :rows="modeRows"
              row-key="scope"
              page-key="modes"
              page-label="Modes"
            >
              <template #cell-scope="{ row }">
                <div class="flex min-w-0 items-center gap-2">
                  <bdi class="min-w-0 truncate" :title="String(row.scope_label || row.scope)">{{
                    row.scope_label || row.scope
                  }}</bdi>
                  <Badge v-if="row.dashboard" variant="secondary" class="shrink-0"
                    >this dashboard</Badge
                  >
                </div>
              </template>
              <template #cell-mode="{ row }">
                {{ MODE_LABEL[row.mode as string] ?? row.mode
                }}<template v-if="shortTime(row.set_at as string)">
                  · since {{ shortTime(row.set_at as string) }}</template
                >
              </template>
              <template #cell-set="{ row }">
                <ToggleGroup
                  v-if="row.dashboard"
                  type="single"
                  variant="outline"
                  aria-label="Mode of the Dashboard"
                  :model-value="row.mode as string"
                  @update:model-value="onMode"
                >
                  <ToggleGroupItem value="read-only" class="max-tab:h-11">
                    👁 Read-only
                  </ToggleGroupItem>
                  <ToggleGroupItem value="guarded" class="max-tab:h-11">
                    🛡 Guarded
                  </ToggleGroupItem>
                  <ToggleGroupItem value="full" class="max-tab:h-11"> ⚡ Full </ToggleGroupItem>
                </ToggleGroup>
                <span v-else class="text-muted-foreground"
                  >View only. Send /mode in that chat.</span
                >
              </template>
            </DataTable>
            <ResultNote :note="modesNote" @close="modesNote = null" />
            <Card>
              <CardContent class="flex flex-col gap-1">
                <p>
                  Process mode: <code>{{ view.process_mode }}</code>
                </p>
                <p>
                  Cron mode: <code>{{ view.cron_mode }}</code>
                </p>
                <p class="text-xs text-muted-foreground">
                  Set in config.yaml.
                  <Button variant="link" class="h-auto p-0" @click="setTab('rules')">
                    What each mode does
                  </Button>
                </p>
              </CardContent>
            </Card>
          </div>

          <!-- Always answers -->
          <div v-else-if="tab === 'grants'" class="flex flex-col gap-4">
            <Alert v-if="view.grants_off" variant="destructive">
              <AlertDescription>
                Saved answers are off, because the answer store is damaged:
                {{ view.grants_off_cause }}. Restart the gateway after you repair it.
              </AlertDescription>
            </Alert>
            <div class="flex flex-wrap items-center gap-3">
              <span>“✅ Always” answers. Session answers live in memory and are not listed.</span>
              <Button
                variant="destructive"
                :disabled="view.grants.length === 0"
                @click="command(view.remove_all, 'grants', 'Remove all')"
              >
                Remove all…
              </Button>
            </div>
            <ResultNote :note="grantsNote" @close="grantsNote = null" />
            <StateBlock
              v-if="view.grants.length === 0"
              state="empty"
              text="An ✅ Always answer shows here."
            />
            <template v-else>
              <DataTable
                :columns="grantCols"
                :rows="grantRows"
                row-key="idx"
                page-key="grants"
                page-label="Always answers"
              >
                <template #cell-rule="{ row }">
                  <b class="font-medium">{{ row.rule }}</b>
                  <div class="text-xs text-muted-foreground">{{ row.description }}</div>
                </template>
                <template #cell-preview="{ row }"
                  ><code class="block truncate font-mono text-xs" :title="String(row.preview)">{{
                    row.preview
                  }}</code></template
                >
                <template #cell-programs="{ row }">{{
                  (row.programs as string[]).join(", ")
                }}</template>
                <template #cell-mcp="{ row }">{{
                  [row.server, row.tool_name].filter(Boolean).join(" · ")
                }}</template>
                <template #cell-created="{ row }"
                  ><span class="whitespace-nowrap" :title="fullTime(row.created as string)">{{
                    shortTime(row.created as string)
                  }}</span></template
                >
                <template #cell-actions="{ row }">
                  <Button
                    variant="destructive"
                    size="sm"
                    @click="command(row.remove as string, 'grants', 'Remove')"
                  >
                    Remove
                  </Button>
                </template>
              </DataTable>
            </template>
          </div>

          <!-- Approval log -->
          <ApprovalLog
            v-else-if="tab === 'log'"
            :scopes="view.log_scopes ?? []"
            :scope-labels="view.log_scope_labels ?? {}"
          />

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

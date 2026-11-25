<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { GoalDetail } from "../api/types.gen"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { confirm, toast } from "../stores/ui"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import DataTable from "../components/DataTable.vue"
import SidePanel from "../components/SidePanel.vue"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardAction, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Progress } from "@/components/ui/progress"
import { cn } from "@/lib/utils"
import type { Column } from "../lib/table"

const route = useRoute()
const router = useRouter()
const sid = computed(() => String(route.params.session))
const { run } = useCommand()
const d = ref<GoalDetail | null>(null)
const state = ref<"loading" | "error" | "ready">("loading")
const errText = ref("")
const missing = ref(false)
const setText = ref("")
const gateFull = ref("")
const gateText = ref("")
const subText = ref("")
const waitOpen = ref(false)
const pid = ref("")
const reason = ref("")

const SOURCES: Record<string, string> = { dashboard: "Dashboard", telegram: "Telegram", cli: "CLI" }
const label = computed(() => (d.value ? (SOURCES[d.value.row.source] ?? d.value.row.source) : ""))

const own = computed(() => !!d.value?.row.can_resume)
const remote = computed(() => !!d.value && d.value.row.can_stop && !d.value.row.can_resume)
const cli = computed(() => d.value?.row.source === "cli")

// Adds --session for a goal of another session, so the command acts on that goal.
function scoped(cmd: string): string {
  return remote.value ? `${cmd} --session ${sid.value}` : cmd
}

// Returns the time text, or a dash when there is no time.
function when(s: string | null | undefined): string {
  return shortTime(s) || "—"
}

const parked = computed(() => !!d.value && d.value.row.status === "active" && d.value.row.waiting)
const hasContract = computed(() => {
  const g = d.value
  return (
    !!g &&
    [g.outcome, g.verification, g.constraints, g.boundaries, g.stop_when].some((s) => s !== "")
  )
})
const contract = computed(() => {
  const g = d.value
  if (!g) return []
  return [
    ["Outcome", g.outcome],
    ["Verification", g.verification],
    ["Constraints", g.constraints],
    ["Boundaries", g.boundaries],
    ["Stop when", g.stop_when],
  ]
})
const pct = computed(() => {
  const r = d.value?.row
  return r && r.max_turns > 0 ? Math.min(100, Math.round((r.turns_used / r.max_turns) * 100)) : 0
})
const gateCols: Column[] = [
  { key: "command", label: "Command", type: "text" },
  { key: "timeout", label: "Timeout", type: "num" },
  { key: "retries", label: "Retries", type: "num" },
  { key: "exit", label: "Last exit", type: "num" },
  { key: "tail", label: "Output tail", type: "block" },
  { key: "actions", label: "", type: "act", width: "7rem" },
]
// Returns the output tail of a gate row. The table slot types a row as unknown.
function tailOf(row: unknown): string {
  return String((row as { tail?: string }).tail ?? "")
}
const gateRows = computed(() =>
  (d.value?.gates ?? []).map((g, i) => ({
    ...g,
    n: i + 1,
    timeout: `${g.timeout_seconds} s`,
    retries: `${g.attempts} / ${g.max_retries}`,
    exit: g.last_exit_code,
    tail: g.last_output_tail,
  })),
)

async function load(): Promise<void> {
  try {
    d.value = await api<GoalDetail>("GET", `/api/goals/${encodeURIComponent(sid.value)}`)
    missing.value = false
    state.value = "ready"
  } catch (e) {
    missing.value = e instanceof ApiError && e.status === 404
    errText.value = friendlyError(e)
    state.value = "error"
  }
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["goal.state"],
  filter: (_n, data) => (data as { session_id?: string }).session_id === sid.value,
})

async function command(text: string): Promise<void> {
  await run(text)
  await refresh()
}

async function pause(): Promise<void> {
  await command(scoped("/goal pause"))
}

async function resume(): Promise<void> {
  await command("/goal resume")
}

async function unwait(): Promise<void> {
  await command("/goal unwait")
}

async function clearGoal(): Promise<void> {
  const ok = await confirm({
    title: "Clear the goal?",
    body: "The goal stops, and its budget, gates, and subgoals are cleared.",
    confirmLabel: "Clear the goal",
    danger: true,
  })
  if (ok) await command(scoped("/goal clear"))
}

async function setGoal(): Promise<void> {
  const t = setText.value.trim()
  if (!t) return
  setText.value = ""
  // The "--" makes the text a goal, even when it starts with a verb like "pause".
  await command(`/goal -- ${t}`)
}

async function draft(): Promise<void> {
  if (d.value) await command(`/goal draft ${d.value.row.goal}`)
}

async function removeSubgoal(i: number): Promise<void> {
  if (!d.value) return
  await command(scoped(`/subgoal remove ${i + 1} ${d.value.subgoal_hashes[i]}`))
}

async function removeGate(i: number): Promise<void> {
  if (!d.value) return
  const ok = await confirm({
    title: "Remove this gate?",
    body: "The judge no longer runs this command.",
    confirmLabel: "Remove",
    danger: true,
    items: [d.value.gates[i].command],
  })
  if (ok) await command(scoped(`/goal gate remove ${i + 1} ${d.value.gate_hashes[i]}`))
}

async function clearSubgoals(): Promise<void> {
  const ok = await confirm({
    title: "Remove all subgoals?",
    body: "Each subgoal of this goal is removed.",
    confirmLabel: "Clear all",
    danger: true,
  })
  if (ok) await command(scoped("/subgoal clear"))
}

async function clearGates(): Promise<void> {
  const ok = await confirm({
    title: "Remove all gates?",
    body: "Each gate of this goal is removed.",
    confirmLabel: "Clear all",
    danger: true,
  })
  if (ok) await command(scoped("/goal gate clear"))
}

async function addGate(): Promise<void> {
  const c = gateText.value.trim()
  if (!c) return
  gateText.value = ""
  await command(`/goal gate add ${c}`)
}

async function addSubgoal(): Promise<void> {
  const t = subText.value.trim()
  if (!t) return
  subText.value = ""
  await command(`/subgoal ${t}`)
}

function closeWait(): void {
  waitOpen.value = false
  pid.value = ""
  reason.value = ""
}

async function wait(): Promise<void> {
  const p = pid.value.trim()
  if (!/^\d+$/.test(p)) {
    toast("Enter the PID as digits.", "warn")
    return
  }
  const r = reason.value.trim()
  closeWait()
  await command(r ? `/goal wait ${p} ${r}` : `/goal wait ${p}`)
}

// The page stays mounted for a short time after the user leaves it. Load only on the goal route.
watch(
  () => route.params.session,
  () => {
    if (route.name !== "goal") return
    // Drop the old goal, so scoped() never mixes the new session with the old goal.
    d.value = null
    missing.value = false
    state.value = "loading"
    void refresh()
  },
)
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <RouterLink class="text-sm text-muted-foreground hover:text-foreground" :to="{ name: 'goals' }"
      >‹ Goals</RouterLink
    >
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="missing"
      state="empty"
      :text="errText"
      action="Back to Goals"
      @action="router.push({ name: 'goals' })"
    />
    <StateBlock
      v-else-if="state === 'error' || !d"
      state="error"
      :text="errText"
      @action="refresh"
    />
    <div v-else class="flex flex-col gap-6" :aria-busy="stale || undefined">
      <div class="flex flex-wrap items-center gap-2">
        <h1 class="text-xl font-semibold tracking-tight">{{ d.row.goal }}</h1>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <template v-if="own">
          <Button v-if="d.row.status === 'paused'" variant="secondary" @click="resume">
            ▶ Resume (new budget)
          </Button>
          <Button v-else-if="d.row.status === 'active'" variant="secondary" @click="pause">
            ⏸ Pause
          </Button>
          <Button v-if="parked" variant="secondary" @click="unwait">Unwait</Button>
          <Button variant="secondary" @click="waitOpen = true">Wait…</Button>
          <Button variant="secondary" @click="draft">
            {{ hasContract ? "Draft again" : "Draft" }}
          </Button>
          <Button variant="destructive" @click="clearGoal">Clear</Button>
        </template>
        <template v-else-if="remote">
          <Button v-if="d.row.status === 'active'" variant="secondary" @click="pause">
            ⏸ Pause
          </Button>
          <Button variant="destructive" @click="clearGoal">Clear</Button>
        </template>
        <Badge v-else variant="outline">read-only</Badge>
      </div>
      <div v-if="own" class="flex gap-2">
        <Input
          v-model="setText"
          aria-label="New goal"
          placeholder="A new goal text"
          @keydown.enter="setGoal"
        />
        <Button variant="secondary" size="sm" @click="setGoal">Set</Button>
      </div>
      <Alert v-if="remote">
        <AlertDescription>To resume, set, or wait, send /goal in that chat.</AlertDescription>
      </Alert>
      <Alert v-else-if="cli">
        <AlertDescription>This goal runs in tether chat. Change it there.</AlertDescription>
      </Alert>
      <div class="grid gap-4 tab:grid-cols-3">
        <Card>
          <CardContent class="flex flex-col gap-1">
            <span class="text-xs text-muted-foreground">Turns</span>
            <span class="text-lg font-semibold"
              >{{ d.row.turns_used }} / {{ d.row.max_turns }}</span
            >
            <Progress :model-value="pct" />
          </CardContent>
        </Card>
        <Card>
          <CardContent class="flex flex-col gap-1">
            <span class="text-xs text-muted-foreground">Last verdict</span>
            <span class="text-lg font-semibold">{{ d.last_verdict || "—" }}</span>
            <span class="text-xs text-muted-foreground">{{ d.last_reason }}</span>
          </CardContent>
        </Card>
        <Card>
          <CardContent class="flex flex-col gap-1">
            <span class="text-xs text-muted-foreground">Session</span>
            <span class="text-lg font-semibold">{{ d.row.title }}</span>
            <span class="text-xs text-muted-foreground">{{ label }}</span>
          </CardContent>
        </Card>
      </div>
      <Alert v-if="parked" class="text-warning">
        <AlertDescription>⏸ Parked: waits on {{ d.row.wait_target }}.</AlertDescription>
      </Alert>
      <div class="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Contract</CardTitle>
          </CardHeader>
          <CardContent>
            <dl v-if="hasContract" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <template v-for="[k, v] in contract" :key="k">
                <dt class="text-muted-foreground">{{ k }}</dt>
                <dd>{{ v }}</dd>
              </template>
            </dl>
            <p v-else class="text-sm text-muted-foreground">No contract.</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Times</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <dt class="text-muted-foreground">Last turn</dt>
              <dd>
                <span class="whitespace-nowrap" :title="fullTime(d.row.last_turn_at)">{{
                  when(d.row.last_turn_at)
                }}</span>
              </dd>
              <dt class="text-muted-foreground">Changed</dt>
              <dd>
                <span class="whitespace-nowrap" :title="fullTime(d.row.updated_at)">{{
                  when(d.row.updated_at)
                }}</span>
              </dd>
              <template v-if="parked">
                <dt class="text-muted-foreground">Waits on</dt>
                <dd>{{ d.row.wait_target }}</dd>
                <dt class="text-muted-foreground">Reason</dt>
                <dd>{{ d.waiting_reason || "—" }}</dd>
                <dt class="text-muted-foreground">Since</dt>
                <dd>
                  <span class="whitespace-nowrap" :title="fullTime(d.waiting_since)">{{
                    when(d.waiting_since)
                  }}</span>
                </dd>
                <dt class="text-muted-foreground">Ends at the latest</dt>
                <dd>
                  <span class="whitespace-nowrap" :title="fullTime(d.waiting_until)">{{
                    when(d.waiting_until)
                  }}</span>
                </dd>
              </template>
            </dl>
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Subgoals</CardTitle>
          <CardAction v-if="(own || remote) && d.subgoals.length > 0">
            <Button variant="ghost" size="sm" @click="clearSubgoals">Clear all</Button>
          </CardAction>
        </CardHeader>
        <CardContent class="flex flex-col gap-2">
          <div v-for="(s, i) in d.subgoals" :key="i" class="flex items-center gap-2">
            <span class="text-muted-foreground">{{ i + 1 }}</span>
            <span class="flex-1">{{ s }}</span>
            <Button v-if="own || remote" variant="ghost" size="sm" @click="removeSubgoal(i)">
              Remove
            </Button>
          </div>
          <div v-if="own" class="flex gap-2">
            <Input v-model="subText" placeholder="Add a subgoal" @keydown.enter="addSubgoal" />
            <Button variant="secondary" size="sm" @click="addSubgoal">Add</Button>
          </div>
        </CardContent>
      </Card>
      <div class="flex flex-wrap items-baseline gap-2">
        <h2 class="text-lg font-semibold">Gates</h2>
        <span class="text-sm text-muted-foreground"
          >Commands that must pass before the judge can say “done”.</span
        >
        <Button
          v-if="(own || remote) && d.gates.length > 0"
          variant="ghost"
          size="sm"
          @click="clearGates"
        >
          Clear all
        </Button>
      </div>
      <DataTable
        :columns="gateCols"
        :rows="gateRows"
        row-key="n"
        page-key="goal-gates"
        page-label="Gates"
      >
        <template #cell-command="{ row }"
          ><code class="block truncate font-mono text-sm" :title="String(row.command)">{{
            row.command
          }}</code></template
        >
        <template #cell-exit="{ row }">
          <span :class="cn(row.exit !== null && row.exit !== 0 && 'text-destructive')">{{
            row.exit === null ? "—" : row.exit
          }}</span>
        </template>
        <template #cell-tail="{ row }">
          <span class="font-mono whitespace-pre-wrap">{{
            tailOf(row).split("\n").slice(-2).join("\n")
          }}</span>
          <Button
            v-if="tailOf(row).split('\n').length > 2"
            variant="ghost"
            size="sm"
            @click="gateFull = tailOf(row)"
          >
            Show all
          </Button>
        </template>
        <template #cell-actions="{ row }">
          <Button
            v-if="own || remote"
            variant="ghost"
            size="sm"
            @click="removeGate(Number(row.n) - 1)"
          >
            Remove
          </Button>
        </template>
      </DataTable>
      <div v-if="own" class="flex gap-2">
        <Input
          v-model="gateText"
          class="font-mono"
          placeholder="Add a gate, for example: go vet ./..."
          @keydown.enter="addGate"
        />
        <Button variant="secondary" size="sm" @click="addGate">Add gate</Button>
      </div>
      <p class="text-xs text-muted-foreground">
        Parse failures {{ d.parse_failures }} · transport failures {{ d.transport_failures }} · turn
        failures {{ d.turn_failures }}
      </p>
      <SidePanel :open="gateFull !== ''" title="Gate output" @close="gateFull = ''">
        <pre class="overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 font-mono text-sm">{{
          gateFull
        }}</pre>
      </SidePanel>
      <SidePanel
        :open="waitOpen"
        title="Wait on a background process"
        :dirty="pid !== '' || reason !== ''"
        @close="closeWait"
      >
        <p class="text-sm text-muted-foreground">
          The goal parks until the process ends, at most 30 minutes.
        </p>
        <FieldGroup>
          <Field>
            <FieldLabel for="goal-wait-pid">PID</FieldLabel>
            <Input
              id="goal-wait-pid"
              v-model="pid"
              data-field="pid"
              inputmode="numeric"
              @input="pid = pid.replace(/\D/g, '')"
            />
          </Field>
          <Field>
            <FieldLabel for="goal-wait-reason">Reason</FieldLabel>
            <Input id="goal-wait-reason" v-model="reason" data-field="reason" />
          </Field>
        </FieldGroup>
        <Button @click="wait">Wait</Button>
      </SidePanel>
    </div>
  </div>
</template>

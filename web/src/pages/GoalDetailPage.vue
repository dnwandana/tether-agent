<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { GoalDetail } from "../api/types.gen"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { confirm, toast } from "../stores/ui"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import DataTable from "../components/DataTable.vue"
import SidePanel from "../components/SidePanel.vue"

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
  if (!s || s.startsWith("0001-")) return "—"
  const t = new Date(s)
  return Number.isNaN(t.getTime()) ? "—" : t.toLocaleString()
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
const gateCols = [
  { key: "command", label: "Command" },
  { key: "timeout", label: "Timeout" },
  { key: "retries", label: "Retries" },
  { key: "exit", label: "Last exit" },
  { key: "tail", label: "Output tail" },
  { key: "actions", label: "", align: "right" as const },
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
  <div class="page">
    <router-link class="back" :to="{ name: 'goals' }">‹ Goals</router-link>
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
    <div v-else :aria-busy="stale || undefined">
      <div class="page-h">
        <h1 class="t-20">{{ d.row.goal }}</h1>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <template v-if="own">
          <button
            v-if="d.row.status === 'paused'"
            class="btn btn-secondary"
            type="button"
            @click="resume"
          >
            ▶ Resume (new budget)
          </button>
          <button
            v-else-if="d.row.status === 'active'"
            class="btn btn-secondary"
            type="button"
            @click="pause"
          >
            ⏸ Pause
          </button>
          <button v-if="parked" class="btn btn-secondary" type="button" @click="unwait">
            Unwait
          </button>
          <button class="btn btn-secondary" type="button" @click="waitOpen = true">Wait…</button>
          <button class="btn btn-secondary" type="button" @click="draft">
            {{ hasContract ? "Draft again" : "Draft" }}
          </button>
          <button class="btn btn-danger-soft" type="button" @click="clearGoal">Clear</button>
        </template>
        <template v-else-if="remote">
          <button
            v-if="d.row.status === 'active'"
            class="btn btn-secondary"
            type="button"
            @click="pause"
          >
            ⏸ Pause
          </button>
          <button class="btn btn-danger-soft" type="button" @click="clearGoal">Clear</button>
        </template>
        <span v-else class="chip chip-plain">read-only</span>
      </div>
      <div v-if="own" class="li">
        <input
          v-model="setText"
          class="input grow"
          aria-label="New goal"
          placeholder="A new goal text"
          @keydown.enter="setGoal"
        />
        <button class="btn btn-secondary btn-sm" type="button" @click="setGoal">Set</button>
      </div>
      <div v-if="remote" class="ro-banner">To resume, set, or wait, send /goal in that chat.</div>
      <div v-else-if="cli" class="ro-banner">This goal runs in tether chat. Change it there.</div>
      <div class="grid-3">
        <div class="tile">
          <span class="t-12 c-3">Turns</span>
          <span class="v">{{ d.row.turns_used }} / {{ d.row.max_turns }}</span>
          <div class="meter"><span :style="{ width: pct + '%' }" /></div>
        </div>
        <div class="tile">
          <span class="t-12 c-3">Last verdict</span>
          <span class="v">{{ d.last_verdict || "—" }}</span>
          <span class="t-12 c-3">{{ d.last_reason }}</span>
        </div>
        <div class="tile">
          <span class="t-12 c-3">Session</span>
          <span class="v">{{ d.row.title }}</span>
          <span class="t-12 c-3">{{ label }}</span>
        </div>
      </div>
      <div v-if="parked" class="warn-banner">⏸ Parked: waits on {{ d.row.wait_target }}.</div>
      <div class="grid-2">
        <div class="card card-pad">
          <div class="card-h"><h3>Contract</h3></div>
          <dl v-if="hasContract" class="kv">
            <template v-for="[k, v] in contract" :key="k">
              <dt>{{ k }}</dt>
              <dd>{{ v }}</dd>
            </template>
          </dl>
          <p v-else class="c-3">No contract.</p>
        </div>
        <div class="card card-pad">
          <div class="card-h"><h3>Times</h3></div>
          <dl class="kv">
            <dt>Last turn</dt>
            <dd>{{ when(d.row.last_turn_at) }}</dd>
            <dt>Changed</dt>
            <dd>{{ when(d.row.updated_at) }}</dd>
            <template v-if="parked">
              <dt>Waits on</dt>
              <dd>{{ d.row.wait_target }}</dd>
              <dt>Reason</dt>
              <dd>{{ d.waiting_reason || "—" }}</dd>
              <dt>Since</dt>
              <dd>{{ when(d.waiting_since) }}</dd>
              <dt>Ends at the latest</dt>
              <dd>{{ when(d.waiting_until) }}</dd>
            </template>
          </dl>
        </div>
      </div>
      <div class="card card-pad">
        <div class="card-h">
          <h3>Subgoals</h3>
          <button
            v-if="(own || remote) && d.subgoals.length > 0"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="clearSubgoals"
          >
            Clear all
          </button>
        </div>
        <div v-for="(s, i) in d.subgoals" :key="i" class="li">
          <span class="c-3">{{ i + 1 }}</span>
          <span class="grow">{{ s }}</span>
          <button
            v-if="own || remote"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="removeSubgoal(i)"
          >
            Remove
          </button>
        </div>
        <div v-if="own" class="li">
          <input
            v-model="subText"
            class="input grow"
            placeholder="Add a subgoal"
            @keydown.enter="addSubgoal"
          />
          <button class="btn btn-secondary btn-sm" type="button" @click="addSubgoal">Add</button>
        </div>
      </div>
      <div class="section-h">
        <h2>Gates</h2>
        <span class="pill-note">Commands that must pass before the judge can say “done”.</span>
        <button
          v-if="(own || remote) && d.gates.length > 0"
          class="btn btn-ghost btn-sm"
          type="button"
          @click="clearGates"
        >
          Clear all
        </button>
      </div>
      <DataTable :columns="gateCols" :rows="gateRows" row-key="n">
        <template #cell-command="{ row }"
          ><code>{{ row.command }}</code></template
        >
        <template #cell-exit="{ row }">
          <span :class="{ 'c-bad': row.exit !== null && row.exit !== 0 }">{{
            row.exit === null ? "—" : row.exit
          }}</span>
        </template>
        <template #cell-tail="{ row }">
          <span class="mono" style="white-space: pre-wrap">{{
            tailOf(row).split("\n").slice(-2).join("\n")
          }}</span>
          <button
            v-if="tailOf(row).split('\n').length > 2"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="gateFull = tailOf(row)"
          >
            Show all
          </button>
        </template>
        <template #cell-actions="{ row }">
          <button
            v-if="own || remote"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="removeGate(Number(row.n) - 1)"
          >
            Remove
          </button>
        </template>
      </DataTable>
      <div v-if="own" class="li">
        <input
          v-model="gateText"
          class="input mono grow"
          placeholder="Add a gate, for example: go vet ./..."
          @keydown.enter="addGate"
        />
        <button class="btn btn-secondary btn-sm" type="button" @click="addGate">Add gate</button>
      </div>
      <p class="t-12 c-3">
        Parse failures {{ d.parse_failures }} · transport failures {{ d.transport_failures }} · turn
        failures {{ d.turn_failures }}
      </p>
      <SidePanel :open="gateFull !== ''" title="Gate output" @close="gateFull = ''">
        <pre class="code-block gate-full">{{ gateFull }}</pre>
      </SidePanel>
      <SidePanel
        :open="waitOpen"
        title="Wait on a background process"
        :dirty="pid !== '' || reason !== ''"
        @close="closeWait"
      >
        <p class="c-3">The goal parks until the process ends, at most 30 minutes.</p>
        <div class="field">
          <label for="goal-wait-pid">PID</label>
          <input
            id="goal-wait-pid"
            v-model="pid"
            class="input"
            data-field="pid"
            inputmode="numeric"
            @input="pid = pid.replace(/\D/g, '')"
          />
        </div>
        <div class="field">
          <label for="goal-wait-reason">Reason</label>
          <input id="goal-wait-reason" v-model="reason" class="input" data-field="reason" />
        </div>
        <button class="btn btn-primary" type="button" @click="wait">Wait</button>
      </SidePanel>
    </div>
  </div>
</template>

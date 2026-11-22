<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue"
import { useRoute } from "vue-router"
import { api, ApiError, runCommand } from "../api/client"
import type { Button, GoalDetail, ReplyView } from "../api/types.gen"
import { session } from "../stores/session"
import { confirm, toast } from "../stores/ui"
import StateBlock from "../components/StateBlock.vue"
import DataTable from "../components/DataTable.vue"
import SidePanel from "../components/SidePanel.vue"

const route = useRoute()
const d = ref<GoalDetail | null>(null)
const state = ref<"loading" | "error" | "ready">("loading")
const errText = ref("")
const gateText = ref("")
const subText = ref("")
const waitOpen = ref(false)
const pid = ref("")
const reason = ref("")

const SOURCES: Record<string, string> = { dashboard: "Dashboard", telegram: "Telegram", cli: "CLI" }
const label = computed(() => (d.value ? (SOURCES[d.value.row.source] ?? d.value.row.source) : ""))

// True for the goal of the active dashboard session while it can still change.
function mine(g: GoalDetail): boolean {
  const r = g.row
  return (
    r.session_id !== "" &&
    r.session_id === session.id &&
    (r.status === "active" || r.status === "paused")
  )
}
const isMine = computed(() => (d.value ? mine(d.value) : false))
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
const waitUntil = computed(() => {
  const s = d.value?.waiting_until
  if (!s || s.startsWith("0001-")) return ""
  const t = new Date(s)
  if (Number.isNaN(t.getTime())) return ""
  return t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })
})
const gateCols = [
  { key: "command", label: "Command" },
  { key: "timeout", label: "Timeout" },
  { key: "retries", label: "Retries" },
  { key: "exit", label: "Last exit" },
  { key: "tail", label: "Output tail" },
  { key: "actions", label: "", align: "right" as const },
]
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

function isConfirm(b: Button): boolean {
  return b.data.endsWith("confirm") || b.data.startsWith("/goal gate ok ")
}

async function act(text: string): Promise<void> {
  try {
    const r = await runCommand(text)
    if (!("text" in r)) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const all = (reply.buttons ?? []).flat()
      const yes = all.find(isConfirm)
      if (yes) {
        const nl = reply.text.indexOf("\n")
        const title = nl < 0 ? reply.text : reply.text.slice(0, nl)
        const body = nl < 0 ? "" : reply.text.slice(nl + 1)
        if (await confirm({ title, body, confirmLabel: yes.text, danger: true })) {
          await act(yes.data)
          return
        }
        const no = all.find((b) => b.data.startsWith("/goal gate no "))
        if (no) await act(no.data)
      } else {
        toast(reply.text, "ok")
      }
    }
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
  await load()
}

async function load(): Promise<void> {
  try {
    d.value = await api<GoalDetail>(
      "GET",
      `/api/goals/${encodeURIComponent(String(route.params.session))}`,
    )
    state.value = "ready"
  } catch (e) {
    errText.value = e instanceof ApiError || e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function clearGoal(): Promise<void> {
  const ok = await confirm({
    title: "Clear the goal?",
    body: "The goal stops, and its budget, gates, and subgoals are cleared.",
    confirmLabel: "Clear the goal",
    danger: true,
  })
  if (ok) await act("/goal clear")
}

async function addGate(): Promise<void> {
  const c = gateText.value.trim()
  if (!c) return
  gateText.value = ""
  await act(`/goal gate add ${c}`)
}

async function addSubgoal(): Promise<void> {
  const t = subText.value.trim()
  if (!t) return
  subText.value = ""
  await act(`/subgoal ${t}`)
}

async function wait(): Promise<void> {
  const p = pid.value.trim()
  if (!/^\d+$/.test(p)) {
    toast("Enter the PID as digits.", "error")
    return
  }
  const r = reason.value.trim()
  waitOpen.value = false
  pid.value = ""
  reason.value = ""
  await act(r ? `/goal wait ${p} ${r}` : `/goal wait ${p}`)
}

onMounted(load)
// The page stays mounted for a short time after the user leaves it. Load only on the goal route.
watch(
  () => route.params.session,
  () => {
    if (route.name === "goal") void load()
  },
)
</script>

<template>
  <div class="page">
    <router-link class="back" :to="{ name: 'goals' }">‹ Goals</router-link>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error' || !d" state="error" :text="errText" @retry="load" />
    <template v-else>
      <div class="page-h">
        <h1 class="t-20">{{ d.row.goal }}</h1>
        <template v-if="isMine">
          <button
            v-if="d.row.status === 'paused'"
            class="btn btn-secondary"
            type="button"
            @click="act('/goal resume')"
          >
            ▶ Resume
          </button>
          <button v-else class="btn btn-secondary" type="button" @click="act('/goal pause')">
            ⏸ Pause
          </button>
          <button class="btn btn-secondary" type="button" @click="waitOpen = true">Wait…</button>
          <button class="btn btn-danger-soft" type="button" @click="clearGoal">Clear</button>
        </template>
        <span v-else class="chip chip-plain">read-only</span>
      </div>
      <div v-if="!isMine && d.row.status === 'active'" class="ro-banner">
        👁 This goal belongs to {{ label }}. Pause it there, or fork the session into the dashboard.
      </div>
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
      <div v-if="parked" class="warn-banner">
        ⏸ Parked: waits on {{ d.row.wait_target }}.<template v-if="waitUntil">
          The wait ends at the latest at {{ waitUntil }}.</template
        >
      </div>
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
          <div class="card-h"><h3>Subgoals</h3></div>
          <div v-for="(s, i) in d.subgoals" :key="i" class="li">
            <span class="c-3">{{ i + 1 }}</span>
            <span class="grow">{{ s }}</span>
            <button
              v-if="isMine"
              class="btn btn-ghost btn-sm"
              type="button"
              @click="act(`/subgoal remove ${i + 1}`)"
            >
              Remove
            </button>
          </div>
          <div v-if="isMine" class="li">
            <input
              v-model="subText"
              class="input grow"
              placeholder="Add a subgoal"
              @keydown.enter="addSubgoal"
            />
            <button class="btn btn-secondary btn-sm" type="button" @click="addSubgoal">Add</button>
          </div>
        </div>
      </div>
      <div class="section-h">
        <h2>Gates</h2>
        <span class="pill-note">Commands that must pass before the judge can say “done”.</span>
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
        <template #cell-tail="{ row }"
          ><span class="mono">{{ row.tail }}</span></template
        >
        <template #cell-actions="{ row }">
          <button
            v-if="isMine"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="act(`/goal gate remove ${row.n}`)"
          >
            Remove
          </button>
        </template>
      </DataTable>
      <div v-if="isMine" class="li">
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
      <SidePanel :open="waitOpen" title="Wait on a background process" @close="waitOpen = false">
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
    </template>
  </div>
</template>

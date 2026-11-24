<script setup lang="ts">
import { computed, ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { GoalRowView } from "../api/types.gen"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { confirm } from "../stores/ui"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"

type Filter = "all" | "active" | "paused" | "parked"

const router = useRouter()
const { run } = useCommand()
const finished = ref(false)
const filter = ref<Filter>("all")
const rows = ref<GoalRowView[]>([])
const state = ref<"loading" | "error" | "ready">("loading")
const errText = ref("")

const columns = [
  { key: "state", label: "State" },
  { key: "goal", label: "Goal" },
  { key: "session", label: "Session" },
  { key: "turns", label: "Turns" },
  { key: "last_verdict", label: "Last verdict" },
  { key: "last_turn", label: "Last turn" },
  { key: "actions", label: "" },
]

const SOURCES: Record<string, string> = { dashboard: "Dashboard", telegram: "Telegram", cli: "CLI" }
function sourceLabel(s: string): string {
  return SOURCES[s] ?? s
}

// Returns the state shown in the chip.
function chipState(g: GoalRowView): string {
  return g.status === "active" && g.waiting ? "parked" : g.status
}
const CHIP: Record<string, string> = {
  active: "chip-acc",
  parked: "chip-info",
  paused: "chip-warn",
  done: "chip-ok",
  cleared: "chip-plain",
}

function line(g: GoalRowView): string {
  let t = `${sourceLabel(g.source)} · “${g.title}” · ${g.turns_used}/${g.max_turns} turns`
  if (g.status === "paused" && g.paused_reason) t += ` · paused: ${g.paused_reason}`
  if (g.waiting) t += ` · waits on ${g.wait_target}`
  return t
}

// Returns the time text, or a dash when there is no time.
function when(s: string | null): string {
  if (!s) return "—"
  const d = new Date(s)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? "—" : d.toLocaleString()
}

// Returns the goal command for the row. The active dashboard session uses the plain form.
// Another dashboard or Telegram session uses --session, so the command acts on its goal.
function goalCmd(g: GoalRowView, verb: "pause" | "clear"): string {
  return g.can_resume ? `/goal ${verb}` : `/goal ${verb} --session ${g.session_id}`
}

async function command(text: string): Promise<void> {
  await run(text)
  await refresh()
}

async function clearGoal(g: GoalRowView): Promise<void> {
  const ok = await confirm({
    title: "Clear the goal?",
    body: "The goal stops, and its budget, gates, and subgoals are cleared.",
    confirmLabel: "Clear the goal",
    danger: true,
  })
  if (ok) await command(goalCmd(g, "clear"))
}

const shown = computed(() =>
  rows.value.filter((g) => filter.value === "all" || chipState(g) === filter.value),
)
const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "active", label: "Active" },
  { key: "paused", label: "Paused" },
  { key: "parked", label: "Parked" },
]

async function load(): Promise<void> {
  try {
    rows.value = await api<GoalRowView[]>(
      "GET",
      finished.value ? "/api/goals?finished=1" : "/api/goals",
    )
    state.value = "ready"
  } catch (e) {
    errText.value = friendlyError(e)
    state.value = "error"
  }
}

async function pick(f: boolean): Promise<void> {
  finished.value = f
  state.value = "loading"
  await refresh()
}

const { stale, updatedAt, refresh } = useLiveData(load, { events: ["goal.state"] })
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Goals</h1>
      <div class="seg">
        <button type="button" :class="{ 'is-on': !finished }" @click="pick(false)">
          Active · parked · paused
        </button>
        <button type="button" :class="{ 'is-on': finished }" @click="pick(true)">
          Finished · 30 days
        </button>
      </div>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <div v-if="!finished" class="seg">
      <button
        v-for="f in FILTERS"
        :key="f.key"
        type="button"
        :class="{ 'is-on': filter === f.key }"
        :aria-pressed="filter === f.key"
        @click="filter = f.key"
      >
        {{ f.label }}
      </button>
    </div>
    <p v-if="finished" class="pill-note">Last goal of each session</p>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="errText" @action="refresh" />
    <StateBlock
      v-else-if="rows.length === 0 && !finished"
      state="empty"
      text="No open goals."
      hint="Send /goal <text> in Chat to start one."
      action="Open chat"
      @action="router.push({ name: 'chat' })"
    />
    <StateBlock
      v-else-if="rows.length === 0"
      state="empty"
      text="No finished goals in the last 30 days."
    />
    <StateBlock
      v-else-if="shown.length === 0"
      state="empty"
      text="No goals match the filter."
      action="Show all"
      @action="filter = 'all'"
    />
    <div v-else :aria-busy="stale || undefined">
      <DataTable
        :columns="columns"
        :rows="shown as unknown as Record<string, unknown>[]"
        row-key="session_id"
      >
        <template #cell-state="{ row }"
          ><span class="chip" :class="CHIP[chipState(row as unknown as GoalRowView)]">{{
            chipState(row as unknown as GoalRowView)
          }}</span></template
        >
        <template #cell-goal="{ row }"
          ><b
            ><router-link
              :to="{
                name: 'goal',
                params: { session: (row as unknown as GoalRowView).session_id },
              }"
              >{{ (row as unknown as GoalRowView).goal }}</router-link
            ></b
          ></template
        >
        <template #cell-session="{ row }"
          ><span class="t-12 c-3">{{ line(row as unknown as GoalRowView) }}</span></template
        >
        <template #cell-turns="{ row }"
          >{{ (row as unknown as GoalRowView).turns_used }}/{{
            (row as unknown as GoalRowView).max_turns
          }}</template
        >
        <template #cell-last_verdict="{ row }">{{
          (row as unknown as GoalRowView).last_verdict || "—"
        }}</template>
        <template #cell-last_turn="{ row }">{{
          when((row as unknown as GoalRowView).last_turn_at)
        }}</template>
        <template #cell-actions="{ row }">
          <template v-for="g in [row as unknown as GoalRowView]" :key="g.session_id">
            <button
              v-if="g.can_resume && g.status === 'paused'"
              class="btn btn-secondary btn-sm"
              type="button"
              @click="command('/goal resume')"
            >
              ▶ Resume
            </button>
            <button
              v-if="g.can_resume && chipState(g) === 'parked'"
              class="btn btn-secondary btn-sm"
              type="button"
              @click="command('/goal unwait')"
            >
              Unwait
            </button>
            <button
              v-if="g.can_stop && g.status === 'active'"
              class="btn btn-secondary btn-sm"
              type="button"
              @click="command(goalCmd(g, 'pause'))"
            >
              ⏸ Pause
            </button>
            <button
              v-if="g.can_stop"
              class="btn btn-danger-soft btn-sm"
              type="button"
              @click="clearGoal(g)"
            >
              Clear
            </button>
            <template v-if="!g.can_stop && !g.can_resume">
              <span class="c-3">view only</span>
              <router-link
                class="btn btn-secondary btn-sm"
                :to="{ name: 'transcript', params: { id: g.session_id } }"
                >Open session</router-link
              >
            </template>
          </template>
        </template>
      </DataTable>
    </div>
    <p class="pill-note">
      Pause and Clear work for dashboard and Telegram goals. Resume and Unwait work only for the
      goal of this dashboard session. CLI goals are view only.
    </p>
  </div>
</template>

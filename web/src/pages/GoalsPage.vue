<script setup lang="ts">
import { computed, ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { GoalRowView } from "../api/types.gen"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { useRoutePage } from "../lib/routePage"
import type { Column } from "../lib/table"
import { fullTime, shortTime } from "../lib/time"
import { confirm } from "../stores/ui"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"

type Filter = "all" | "active" | "parked" | "paused" | "finished"

const router = useRouter()
const { page, setPage } = useRoutePage("goals")
const { run } = useCommand()
const filter = ref<Filter>("all")
const finished = computed(() => filter.value === "finished")
const rows = ref<GoalRowView[]>([])
const state = ref<"loading" | "error" | "ready">("loading")
const errText = ref("")

const columns: Column[] = [
  { key: "state", label: "State", type: "badge" },
  { key: "goal", label: "Goal", type: "text" },
  { key: "session", label: "Session", type: "text" },
  { key: "turns", label: "Turns", type: "num", optional: true },
  { key: "last_verdict", label: "Last verdict", type: "badge", optional: true },
  { key: "last_turn", label: "Last turn", type: "date" },
  { key: "actions", label: "", type: "act", width: "14rem" },
]

const SOURCES: Record<string, string> = { dashboard: "Dashboard", telegram: "Telegram", cli: "CLI" }
function sourceLabel(s: string): string {
  return SOURCES[s] ?? s
}

// Returns the state shown in the chip.
function chipState(g: GoalRowView): string {
  return g.status === "active" && g.waiting ? "parked" : g.status
}
const BADGE: Record<string, { variant: "default" | "secondary" | "outline"; cls: string }> = {
  active: { variant: "default", cls: "" },
  parked: { variant: "outline", cls: "text-info" },
  paused: { variant: "outline", cls: "text-warning" },
  done: { variant: "outline", cls: "text-success" },
  cleared: { variant: "secondary", cls: "" },
}

function line(g: GoalRowView): string {
  let t = `${sourceLabel(g.source)} · “${g.title}” · ${g.turns_used}/${g.max_turns} turns`
  if (g.status === "paused" && g.paused_reason) t += ` · paused: ${g.paused_reason}`
  if (g.waiting) t += ` · waits on ${g.wait_target}`
  return t
}

// Returns the time text, or a dash when there is no time.
function when(s: string | null): string {
  return shortTime(s) || "—"
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
  rows.value.filter(
    (g) => finished.value || filter.value === "all" || chipState(g) === filter.value,
  ),
)
const FILTERS: { key: Filter; label: string }[] = [
  { key: "all", label: "All open" },
  { key: "active", label: "Active" },
  { key: "parked", label: "Parked" },
  { key: "paused", label: "Paused" },
  { key: "finished", label: "Finished (30 d)" },
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

// Sets the filter. Only a change to or from Finished loads again, because only Finished uses
// another route.
async function pick(f: Filter): Promise<void> {
  const changed = finished.value !== (f === "finished")
  filter.value = f
  setPage(1)
  if (!changed) return
  state.value = "loading"
  await refresh()
}

// Handles a change of the filter group. An empty value comes from a tap on the pressed item.
function onFilter(v: unknown): void {
  if (typeof v === "string" && FILTERS.some((f) => f.key === v)) void pick(v as Filter)
}

const { stale, updatedAt, refresh } = useLiveData(load, { events: ["goal.state"] })
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-2">
      <h1 class="text-xl font-semibold tracking-tight">Goals</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <ToggleGroup
      type="single"
      variant="outline"
      class="max-w-full flex-wrap"
      :model-value="filter"
      @update:model-value="onFilter"
    >
      <ToggleGroupItem v-for="f in FILTERS" :key="f.key" :value="f.key">
        {{ f.label }}
      </ToggleGroupItem>
    </ToggleGroup>
    <p v-if="finished" class="text-sm text-muted-foreground">Last goal of each session</p>
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
      @action="pick('all')"
    />
    <div v-else class="flex flex-col gap-6" :aria-busy="stale || undefined">
      <DataTable
        :columns="columns"
        :rows="shown as unknown as Record<string, unknown>[]"
        row-key="session_id"
        page-key="goals"
        page-label="Goals"
        :page="page"
        @update:page="setPage"
      >
        <template #cell-state="{ row }">
          <Badge
            :variant="BADGE[chipState(row as unknown as GoalRowView)].variant"
            :class="cn(BADGE[chipState(row as unknown as GoalRowView)].cls)"
            >{{ chipState(row as unknown as GoalRowView) }}</Badge
          >
        </template>
        <template #cell-goal="{ row }">
          <RouterLink
            class="block truncate font-semibold"
            :title="(row as unknown as GoalRowView).goal"
            :to="{
              name: 'goal',
              params: { session: (row as unknown as GoalRowView).session_id },
            }"
            >{{ (row as unknown as GoalRowView).goal }}</RouterLink
          >
        </template>
        <template #cell-session="{ row }">
          <span
            class="block truncate text-xs text-muted-foreground"
            :title="line(row as unknown as GoalRowView)"
            >{{ line(row as unknown as GoalRowView) }}</span
          >
        </template>
        <template #cell-turns="{ row }"
          >{{ (row as unknown as GoalRowView).turns_used }}/{{
            (row as unknown as GoalRowView).max_turns
          }}</template
        >
        <template #cell-last_verdict="{ row }">{{
          (row as unknown as GoalRowView).last_verdict || "—"
        }}</template>
        <template #cell-last_turn="{ row }">
          <span
            class="whitespace-nowrap"
            :title="fullTime((row as unknown as GoalRowView).last_turn_at)"
            >{{ when((row as unknown as GoalRowView).last_turn_at) }}</span
          >
        </template>
        <template #cell-actions="{ row }">
          <template v-for="g in [row as unknown as GoalRowView]" :key="g.session_id">
            <Button
              v-if="g.can_resume && g.status === 'paused'"
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              @click="command('/goal resume')"
            >
              Resume
            </Button>
            <Button
              v-if="g.can_resume && chipState(g) === 'parked'"
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              @click="command('/goal unwait')"
            >
              Unwait
            </Button>
            <Button
              v-if="g.can_stop && g.status === 'active'"
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              @click="command(goalCmd(g, 'pause'))"
            >
              Pause
            </Button>
            <Button
              v-if="g.can_stop"
              variant="destructive"
              size="sm"
              class="max-tab:min-h-11"
              @click="clearGoal(g)"
            >
              Clear
            </Button>
            <template v-if="!g.can_stop && !g.can_resume">
              <span class="text-muted-foreground">view only</span>
              <Button as-child variant="secondary" size="sm" class="max-tab:min-h-11">
                <RouterLink :to="{ name: 'transcript', params: { id: g.session_id } }"
                  >Open session</RouterLink
                >
              </Button>
            </template>
          </template>
        </template>
      </DataTable>
    </div>
    <p class="text-sm text-muted-foreground">
      Pause and Clear work for dashboard and Telegram goals. Resume and Unwait work only for the
      goal of this dashboard session. CLI goals are view only.
    </p>
  </div>
</template>

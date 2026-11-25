<script setup lang="ts">
import { ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { BriefView, HeldView, JobRow, ReplyView, RunRow } from "../api/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { cn } from "@/lib/utils"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { replyKind, useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { costText, lastSeven, stateGlyph, type Bar } from "../lib/jobbars"
import { useLiveData } from "../lib/live"
import { useRoutePage } from "../lib/routePage"
import { readableSchedule } from "../lib/schedule"
import type { Column } from "../lib/table"
import { fullTime, shortTime } from "../lib/time"
import { host } from "../stores/host"
import { confirm, toast } from "../stores/ui"

const router = useRouter()
const { page, setPage } = useRoutePage("jobs")
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const brief = ref<BriefView | null>(null)
const jobs = ref<JobRow[]>([])
const held = ref<HeldView[]>([])
const formOpen = ref(false)
const formDirty = ref(false)

const columns: Column[] = [
  { key: "name", label: "Job", type: "text" },
  { key: "schedule", label: "Schedule", type: "text" },
  { key: "next_run", label: "Next run", type: "date" },
  { key: "runs7", label: "Last 7", type: "text", width: "9rem", optional: true },
  { key: "cost_7d", label: "Cost · 7 d", type: "num", optional: true },
  { key: "actions", label: "", type: "act", width: "13rem" },
]

const kindIcon: Record<string, string> = {
  failing: "⚠",
  paused_job: "⏸",
  bad_schedule: "⚠️",
  held: "🛡",
  goal: "🎯",
  cost: "💸",
}

// Returns the schedule text in the browser zone, with the raw text in the title.
function sched(raw: string): { text: string; title: string } {
  return readableSchedule(raw, host.zone, host.offsetMin, -new Date().getTimezoneOffset())
}

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string): string {
  return shortTime(s) || "—"
}

function age(sec: number): string {
  if (sec < 90) return `${Math.max(sec, 0)} s`
  if (sec < 5400) return `${Math.round(sec / 60)} min`
  if (sec < 172800) return `${Math.round(sec / 3600)} h`
  return `${Math.round(sec / 86400)} d`
}

function heldAge(s: string): string {
  const t = new Date(s).getTime()
  return Number.isNaN(t) ? "" : age(Math.floor((Date.now() - t) / 1000))
}

// Returns one bar class for each of the newest 7 runs. JobRow.last7 is oldest first.
function barsOf(j: JobRow): Bar[] {
  return lastSeven([...(j.last7 ?? [])].reverse().map((status) => ({ status }) as RunRow))
}

// Returns the status token class of one run bar.
function barClass(b: Bar): string {
  if (b === "ok") return "bg-success"
  if (b === "bad") return "bg-destructive"
  if (b === "stop") return "bg-warning"
  return "bg-muted"
}

// Returns the count of runs with the status "ok" in the newest 7 runs.
function okOf(j: JobRow): number {
  return (j.last7 ?? []).slice(-7).filter((st) => st === "ok").length
}

async function load(): Promise<void> {
  try {
    const [b, j, h] = await Promise.all([
      api<BriefView>("GET", "/api/brief"),
      api<JobRow[]>("GET", "/api/jobs"),
      api<HeldView[]>("GET", "/api/held"),
    ])
    brief.value = b
    jobs.value = j
    held.value = h
    state.value = "ready"
  } catch (e) {
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

const { run } = useCommand()
const { stale, updatedAt, refresh } = useLiveData(load, { events: ["held", "job.run"] })

async function command(text: string): Promise<void> {
  await run(text)
  await refresh()
}

async function resolveHeld(h: HeldView, action: "post" | "drop"): Promise<void> {
  if (action === "drop") {
    const ok = await confirm({
      title: "Drop this held send?",
      body: "The text is deleted.",
      confirmLabel: "❌ Drop",
      danger: true,
    })
    if (!ok) return
  }
  try {
    const r = await api<ReplyView>("POST", `/api/held/${h.id}`, { action })
    toast(r.text, replyKind(r.text), { markdown: true })
  } catch (e) {
    toast(friendlyError(e), "error")
  }
  await refresh()
}

// Returns the link of a brief line: the job, else the goal.
function briefTarget(l: { job_id: string; session_id: string }): Record<string, unknown> {
  if (l.job_id) return { to: { name: "job", params: { id: l.job_id } } }
  if (l.session_id) return { to: { name: "goal", params: { session: l.session_id } } }
  return {}
}

async function saved(): Promise<void> {
  formOpen.value = false
  formDirty.value = false
  await refresh()
}
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-2">
      <h1 class="text-xl font-semibold tracking-tight">Jobs</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <Button variant="secondary" type="button" @click="router.push({ name: 'chat' })">
        Ask in chat
      </Button>
      <Button type="button" @click="formOpen = true">+ New job</Button>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <div v-else class="flex flex-col gap-6" :aria-busy="stale || undefined">
      <Card v-if="brief">
        <CardHeader>
          <CardTitle>Brief</CardTitle>
          <CardDescription>what needs attention · no model call</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-col gap-1">
          <p v-if="brief.lines.length === 0" class="text-sm text-muted-foreground">
            Nothing needs your attention.
          </p>
          <component
            :is="l.job_id || l.session_id ? 'router-link' : 'div'"
            v-for="(l, i) in brief.lines"
            :key="i"
            class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-muted"
            v-bind="briefTarget(l)"
          >
            <span aria-hidden="true">{{ kindIcon[l.kind] ?? "•" }}</span>
            <span class="flex-1"
              ><b>{{ l.name }}</b> <span class="text-muted-foreground">{{ l.detail }}</span></span
            >
            <Badge v-if="l.count > 1" variant="outline">×{{ l.count }}</Badge>
            <span v-if="l.cost_usd > 0" class="text-xs text-muted-foreground">{{
              usd(l.cost_usd)
            }}</span>
            <span class="text-xs text-muted-foreground">{{ age(l.age_seconds) }}</span>
          </component>
        </CardContent>
        <CardFooter class="flex flex-wrap gap-2 text-sm text-muted-foreground">
          <span v-if="brief.history_off">Run history is off.</span>
          <span v-else>Jobs: {{ brief.runs_ok }} of {{ brief.runs_total }} runs OK in 24 h</span
          ><span class="flex-1" />
          <span
            >Spend in 24 h: ${{ brief.spend_usd.toFixed(4)
            }}{{ brief.spend_unpriced ? " · unpriced" : "" }}</span
          >
        </CardFooter>
      </Card>

      <StateBlock
        v-if="jobs.length === 0"
        state="empty"
        text="No jobs yet."
        hint="Ask in chat, or add a job here."
        action="+ New job"
        @action="formOpen = true"
      />
      <DataTable
        v-else
        :columns="columns"
        :rows="jobs as unknown as Record<string, unknown>[]"
        row-key="id"
        page-key="jobs"
        page-label="Jobs"
        :page="page"
        @update:page="setPage"
      >
        <template #cell-name="{ row }">
          <div class="flex min-w-0 items-center gap-1">
            <span aria-hidden="true" class="shrink-0">{{
              stateGlyph[(row as unknown as JobRow).state]
            }}</span>
            <router-link
              class="min-w-0 truncate font-medium"
              :title="(row as unknown as JobRow).name"
              :to="{ name: 'job', params: { id: (row as unknown as JobRow).id } }"
              >{{ (row as unknown as JobRow).name }}</router-link
            >
            <Badge
              v-if="(row as unknown as JobRow).watch"
              variant="outline"
              class="shrink-0 text-info"
              >watch</Badge
            >
          </div>
        </template>
        <template #cell-schedule="{ row }"
          ><span class="block truncate" :title="sched((row as unknown as JobRow).schedule).title">{{
            sched((row as unknown as JobRow).schedule).text
          }}</span></template
        >
        <template #cell-next_run="{ row }"
          ><span class="whitespace-nowrap" :title="fullTime((row as unknown as JobRow).next_run)">{{
            when((row as unknown as JobRow).next_run)
          }}</span></template
        >
        <template #cell-runs7="{ row }">
          <span v-if="(row as unknown as JobRow).history_off" class="text-muted-foreground">—</span>
          <span
            v-else-if="((row as unknown as JobRow).last7 ?? []).length === 0"
            class="text-xs text-muted-foreground"
            >No runs</span
          >
          <template v-else>
            <span data-runs7 class="inline-flex gap-0.5"
              ><i
                v-for="(b, n) in barsOf(row as unknown as JobRow)"
                :key="n"
                :data-bar="b"
                :class="cn('h-3 w-1.5 rounded-sm', barClass(b))"
            /></span>
            {{ " "
            }}<span class="text-xs text-muted-foreground"
              >{{ okOf(row as unknown as JobRow) }}/{{
                ((row as unknown as JobRow).last7 ?? []).length
              }}
              ok</span
            >
          </template>
        </template>
        <template #cell-cost_7d="{ row }">
          {{ costText(row as unknown as JobRow) }}
        </template>
        <template #cell-actions="{ row }">
          <span v-if="(row as unknown as JobRow).state === 'done'" class="text-muted-foreground"
            >done</span
          >
          <template v-else>
            <Button
              v-if="(row as unknown as JobRow).enabled"
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              type="button"
              @click.stop="command(`/jobs pause ${(row as unknown as JobRow).id}`)"
            >
              ⏸ Pause
            </Button>
            <Button
              v-else
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              type="button"
              @click.stop="command(`/jobs resume ${(row as unknown as JobRow).id}`)"
            >
              ▶ Resume
            </Button>
            <Button
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              type="button"
              :disabled="!(row as unknown as JobRow).enabled"
              :title="(row as unknown as JobRow).enabled ? undefined : 'Resume the job first'"
              @click.stop="command(`/jobs run ${(row as unknown as JobRow).id}`)"
            >
              ⚡ Run now
            </Button>
          </template>
        </template>
      </DataTable>

      <div class="flex flex-wrap items-baseline gap-2">
        <h2 class="text-lg font-semibold">Held sends</h2>
        <span class="text-sm text-muted-foreground"
          >The guard held these deliveries. Each lives 24 h.</span
        >
      </div>
      <StateBlock
        v-if="held.length === 0"
        state="empty"
        text="No held sends."
        hint="A delivery that the outbound guard holds shows here."
      />
      <Card v-for="h in held" :key="h.id">
        <CardHeader>
          <CardTitle>🛡 {{ h.job_name }} → {{ h.target_title }}</CardTitle>
          <CardDescription>{{ heldAge(h.created) }}</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-col gap-2">
          <p>
            <Badge variant="outline">{{ h.kind }}</Badge>
            <span v-if="h.sources.length > 0" class="text-xs text-muted-foreground">
              Sources: {{ h.sources.join(", ") }}</span
            >
          </p>
          <p class="text-sm text-muted-foreground">{{ h.reason }}</p>
          <pre class="whitespace-pre-wrap rounded-md bg-muted p-3 font-mono text-sm">{{
            h.text
          }}</pre>
        </CardContent>
        <CardFooter class="flex flex-wrap gap-2">
          <span v-if="!h.can_post" class="text-xs text-muted-foreground">Telegram is off.</span>
          <span class="flex-1" />
          <Button
            v-for="a in h.actions"
            :key="a.data"
            size="sm"
            :variant="a.data === 'post' ? 'default' : 'secondary'"
            class="max-tab:min-h-11"
            type="button"
            :disabled="a.data === 'post' && !h.can_post"
            @click="resolveHeld(h, a.data === 'drop' ? 'drop' : 'post')"
          >
            {{ a.text }}
          </Button>
        </CardFooter>
      </Card>
    </div>

    <SidePanel
      :open="formOpen"
      title="New job"
      :dirty="formDirty"
      @close="((formOpen = false), (formDirty = false))"
    >
      <JobForm
        v-if="formOpen"
        :job="null"
        @saved="saved"
        @cancel="((formOpen = false), (formDirty = false))"
        @dirty="formDirty = $event"
      />
    </SidePanel>
  </div>
</template>

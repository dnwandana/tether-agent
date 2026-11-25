<script setup lang="ts">
import { computed, ref } from "vue"
import {
  ChevronLeftIcon,
  CircleDollarSignIcon,
  PauseIcon,
  ScrollTextIcon,
  TriangleAlertIcon,
  XIcon,
} from "@lucide/vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { JobDetail, RunRow } from "../api/types.gen"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { GLYPH_TONE_CLASS, splitGlyph } from "../lib/glyphs"
import { costText, lastSeven, stateIcon, type Bar } from "../lib/jobbars"
import { useLiveData } from "../lib/live"
import { clampPage, readSize, writeSize, type PageSize } from "../lib/pager"
import { readableSchedule } from "../lib/schedule"
import type { Column } from "../lib/table"
import { fullTime, shortTime } from "../lib/time"
import { host } from "../stores/host"
import { toast } from "../stores/ui"

// Returns the schedule text in the browser zone, with the raw text in the title.
function sched(raw: string): { text: string; title: string } {
  return readableSchedule(raw, host.zone, host.offsetMin, -new Date().getTimezoneOffset())
}

// Returns a Go text with no known lead mark. The Alert or the column already shows the state.
// An unknown lead emoji stays, because the last output is often model text.
function plain(s: string): string {
  const g = splitGlyph(s)
  return g.glyph === null ? s : g.rest
}

const route = useRoute()
const router = useRouter()
const id = computed(() => String(route.params.id))

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const detail = ref<JobDetail | null>(null)
const runs = ref<RunRow[]>([])
const page = ref(1)
const size = ref<PageSize>(readSize("job-runs"))
const loading = ref(false)
// Counts the runs requests. Only the reply of the newest request changes the runs.
let seq = 0
const output = ref<RunRow | null>(null)
const formOpen = ref(false)
const formDirty = ref(false)
const deleting = ref(false)
const phone = ref(false)

const columns: Column[] = [
  { key: "started", label: "Started", type: "date" },
  { key: "finished", label: "Finished", type: "date", optional: true },
  { key: "duration", label: "Duration", type: "num" },
  { key: "status", label: "Status", type: "badge" },
  { key: "delivery", label: "Delivery", type: "text", optional: true },
  { key: "delivery_err", label: "Delivery error", type: "text" },
  { key: "cost", label: "Cost", type: "num" },
  { key: "actions", label: "", type: "act", width: "11rem" },
]

const j = computed(() => detail.value?.job)
const done = computed(() => j.value?.state === "done")
const bars = computed(() => (detail.value ? lastSeven(detail.value.last7 ?? []) : []))
const pauseText = computed(() =>
  j.value?.pause_reason === "failures"
    ? `Paused after ${j.value.fail_streak} failures`
    : j.value?.pause_reason === "schedule"
      ? "The schedule is not valid"
      : "—",
)

// Returns the color class of a bar of the last 7 runs.
function barClass(b: Bar): string {
  return b === "ok"
    ? "bg-success"
    : b === "bad"
      ? "bg-destructive"
      : b === "stop"
        ? "bg-warning"
        : "bg-muted"
}

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string | undefined): string {
  return shortTime(s) || "—"
}

function hhmm(s: string): string {
  return new Date(s).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  })
}

const firstRunText = computed(() => {
  const t = shortTime(j.value?.next_run)
  return t ? `No runs yet. The first run is at ${t}.` : "No runs yet."
})

const outputTitle = computed(() =>
  output.value ? "Output of the run at " + hhmm(output.value.started_at) : "",
)

function showOutput(r: RunRow): void {
  phone.value = globalThis.matchMedia?.("(max-width: 599px)").matches ?? false
  output.value = r
}

// Reads one page of runs. The page is clamped to the last page of runs_total. A reply of an
// older request is dropped. An empty page after page 1 means that the history got smaller
// after the detail request, so the page before loads.
async function loadRuns(want: number): Promise<void> {
  const p = clampPage(want, detail.value?.runs_total ?? 0, size.value)
  const my = ++seq
  page.value = p
  loading.value = true
  try {
    const r = await api<RunRow[]>(
      "GET",
      `/api/jobs/${id.value}/runs?offset=${(p - 1) * size.value}&limit=${size.value}`,
    )
    if (my !== seq) return
    if (r.length === 0 && p > 1) return loadRuns(p - 1)
    runs.value = r
  } catch (e) {
    if (my === seq) toast(friendlyError(e), "error")
  } finally {
    if (my === seq) loading.value = false
  }
}

async function load(): Promise<void> {
  try {
    detail.value = await api<JobDetail>("GET", `/api/jobs/${id.value}`)
  } catch (e) {
    if (deleting.value && e instanceof ApiError && e.status === 404) {
      await router.push({ name: "jobs" })
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
    return
  }
  await loadRuns(page.value)
  state.value = "ready"
}

// Reads the page that the pager asks for.
function goPage(p: number): void {
  if (p !== page.value) void loadRuns(p)
}

// Keeps the new size and reads page 1.
function setSize(n: PageSize): void {
  size.value = n
  writeSize("job-runs", n)
  void loadRuns(1)
}

const { run } = useCommand()
const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["job.run"],
  filter: (_n, d) => (d as { job_id?: string }).job_id === id.value,
})

async function command(text: string): Promise<void> {
  await run(text)
  await refresh()
}

async function del(): Promise<void> {
  deleting.value = true
  try {
    await command(`/jobs delete ${j.value?.id ?? id.value}`)
  } finally {
    deleting.value = false
  }
}

async function saved(): Promise<void> {
  formOpen.value = false
  formDirty.value = false
  await refresh()
}

function closeForm(): void {
  formOpen.value = false
  formDirty.value = false
}
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <RouterLink
      class="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
      :to="{ name: 'jobs' }"
      ><ChevronLeftIcon class="size-4" />Jobs</RouterLink
    >
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <div v-else-if="detail && j" class="flex flex-col gap-6" :aria-busy="stale || undefined">
      <div class="flex flex-wrap items-center gap-2">
        <h1 class="flex items-center gap-2 text-xl font-semibold tracking-tight">
          <component
            :is="stateIcon[j.state].icon"
            :class="cn('size-5 shrink-0', GLYPH_TONE_CLASS[stateIcon[j.state].tone])"
          /><span class="sr-only">{{ stateIcon[j.state].word }}</span> {{ j.name }}
        </h1>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <span v-if="done" class="text-muted-foreground">done</span>
        <template v-else>
          <Button
            v-if="j.enabled"
            variant="secondary"
            type="button"
            @click="command(`/jobs pause ${j.id}`)"
          >
            Pause
          </Button>
          <Button v-else variant="secondary" type="button" @click="command(`/jobs resume ${j.id}`)">
            Resume
          </Button>
          <Button
            variant="secondary"
            type="button"
            :disabled="!j.enabled"
            @click="command(`/jobs run ${j.id}`)"
          >
            Run now
          </Button>
          <span v-if="!j.enabled" class="text-xs text-muted-foreground">Resume the job first.</span>
        </template>
        <Button variant="secondary" type="button" @click="formOpen = true">Edit</Button>
        <Button variant="destructive" type="button" @click="del()">Delete…</Button>
      </div>

      <Alert v-if="detail.last_error" variant="destructive">
        <TriangleAlertIcon />
        <AlertTitle>Failing · {{ j.fail_streak }} in a row.</AlertTitle>
        <AlertDescription class="font-mono">{{ plain(detail.last_error) }}</AlertDescription>
      </Alert>
      <Alert v-if="j.pause_reason === 'schedule'" class="text-warning">
        <PauseIcon />
        <AlertDescription>
          Paused: the schedule {{ j.schedule }} is not valid. Edit the schedule to resume.
        </AlertDescription>
      </Alert>

      <div class="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Definition</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <dt class="text-muted-foreground">Schedule</dt>
              <dd class="min-w-0 break-words" :title="sched(j.schedule).title">
                {{ sched(j.schedule).text }}
              </dd>
              <template v-if="detail.no_agent">
                <dt class="text-muted-foreground">Script</dt>
                <dd class="min-w-0 font-mono break-words">{{ detail.script }}</dd>
              </template>
              <template v-else>
                <dt class="text-muted-foreground">Prompt</dt>
                <dd class="min-w-0 break-words">{{ detail.prompt }}</dd>
                <template v-if="detail.script">
                  <dt class="text-muted-foreground">Script</dt>
                  <dd class="min-w-0 font-mono break-words">{{ detail.script }}</dd>
                </template>
              </template>
              <dt class="text-muted-foreground">Deliver to</dt>
              <dd class="min-w-0 break-words">
                {{ detail.deliver_label || detail.deliver || "—" }}
              </dd>
              <dt class="text-muted-foreground">Model · effort</dt>
              <dd class="min-w-0 break-words">
                {{ detail.model || "default" }} · {{ detail.effort || "default" }}
              </dd>
              <dt class="text-muted-foreground">Skills</dt>
              <dd class="min-w-0 break-words">{{ (detail.skills ?? []).join(", ") || "—" }}</dd>
              <dt class="text-muted-foreground">Toolsets</dt>
              <dd class="min-w-0 break-words">{{ (detail.toolsets ?? []).join(", ") || "—" }}</dd>
              <dt class="text-muted-foreground">Workdir</dt>
              <dd class="min-w-0 font-mono break-words">{{ detail.workdir || "—" }}</dd>
              <dt class="text-muted-foreground">Options</dt>
              <dd class="min-w-0 break-words">
                {{
                  [
                    j.watch && "watch",
                    j.draft_only && "draft only",
                    detail.continuity && "continuity",
                    detail.no_agent && "no agent",
                  ]
                    .filter(Boolean)
                    .join(", ") || "—"
                }}
              </dd>
              <dt class="text-muted-foreground">Repeat</dt>
              <dd class="min-w-0 break-words">
                {{ detail.repeat_left > 0 ? `${detail.repeat_left} left` : "forever" }}
              </dd>
            </dl>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Health</CardTitle>
          </CardHeader>
          <CardContent>
            <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
              <dt class="text-muted-foreground">Last run</dt>
              <dd class="min-w-0 break-words">
                <span class="whitespace-nowrap" :title="fullTime(j.last_run)">{{
                  when(j.last_run)
                }}</span
                >{{ j.last_status ? ` · ${j.last_status}` : "" }}
              </dd>
              <dt class="text-muted-foreground">Next run</dt>
              <dd class="min-w-0 break-words">
                <span class="whitespace-nowrap" :title="fullTime(j.next_run)">{{
                  when(j.next_run)
                }}</span>
              </dd>
              <dt class="text-muted-foreground">Last 7</dt>
              <dd class="min-w-0 break-words">
                <span data-runs7 class="inline-flex gap-0.5"
                  ><i
                    v-for="(b, n) in bars"
                    :key="n"
                    :data-bar="b"
                    :class="cn('h-3 w-1.5 rounded-sm', barClass(b))"
                /></span>
              </dd>
              <dt class="text-muted-foreground">This week</dt>
              <dd class="min-w-0 break-words">
                {{ detail.week_ok }} of {{ detail.week_total }} OK · {{ costText(j) }}
              </dd>
              <dt class="text-muted-foreground">Fail streak</dt>
              <dd class="min-w-0 break-words">
                {{ j.fail_streak
                }}{{ detail.fail_limit > 0 ? ` (pauses at ${detail.fail_limit})` : "" }}
              </dd>
              <dt class="text-muted-foreground">Pause reason</dt>
              <dd class="min-w-0 break-words">{{ pauseText }}</dd>
              <dt class="text-muted-foreground">Last output</dt>
              <dd class="min-w-0 font-mono break-words">{{ plain(detail.last_output) || "—" }}</dd>
              <template v-if="detail.watch_limit_hit"
                ><dt class="text-muted-foreground">Watch</dt>
                <dd class="min-w-0 break-words text-warning">
                  The watch limit is reached.
                </dd></template
              >
              <template v-if="detail.watch_output"
                ><dt class="text-muted-foreground">Watch output</dt>
                <dd class="min-w-0 break-words">{{ detail.watch_output }}</dd></template
              >
            </dl>
          </CardContent>
        </Card>
      </div>

      <h2 class="flex items-center gap-2 text-lg font-semibold">
        <ScrollTextIcon class="size-4 text-muted-foreground" />Runs
      </h2>
      <StateBlock
        v-if="j.history_off"
        state="empty"
        text="Run history is off (cron.run_history is 0)."
      />
      <StateBlock
        v-else-if="detail.runs_total === 0"
        state="empty"
        :text="firstRunText"
        :action="j.enabled ? 'Run now' : undefined"
        @action="command(`/jobs run ${j.id}`)"
      />
      <template v-else>
        <DataTable
          :columns="columns"
          :rows="runs as unknown as Record<string, unknown>[]"
          row-key="id"
          page-key="job-runs"
          page-label="Runs"
          :total="detail.runs_total"
          :page="page"
          :size="size"
          :loading="loading"
          @update:page="goPage"
          @update:size="setSize"
        >
          <template #cell-started="{ row }">{{
            when((row as unknown as RunRow).started_at)
          }}</template>
          <template #cell-finished="{ row }">{{
            when((row as unknown as RunRow).finished_at)
          }}</template>
          <template #cell-duration="{ row }"
            >{{ ((row as unknown as RunRow).duration_ms / 1000).toFixed(1) }} s</template
          >
          <template #cell-status="{ row }">
            <Badge
              :variant="(row as unknown as RunRow).status === 'ok' ? 'secondary' : 'destructive'"
              >{{ (row as unknown as RunRow).status }}</Badge
            >
          </template>
          <template #cell-delivery_err="{ row }">
            <span
              v-if="(row as unknown as RunRow).delivery_err"
              class="block truncate font-mono text-destructive"
              :title="plain((row as unknown as RunRow).delivery_err)"
              >{{ plain((row as unknown as RunRow).delivery_err) }}</span
            >
          </template>
          <template #cell-cost="{ row }">
            {{
              (row as unknown as RunRow).cost_known
                ? usd((row as unknown as RunRow).cost_usd)
                : "—"
            }}<template v-if="(row as unknown as RunRow).cost_flag"
              ><CircleDollarSignIcon class="ml-1 inline size-3.5 text-warning" /><span
                class="sr-only"
                >cost alert</span
              ></template
            >
          </template>
          <template #cell-actions="{ row }">
            <Button
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              type="button"
              @click.stop="showOutput(row as unknown as RunRow)"
            >
              Output
            </Button>
            <Button
              v-if="(row as unknown as RunRow).session_id"
              variant="secondary"
              size="sm"
              class="max-tab:min-h-11"
              type="button"
              @click.stop="
                router.push({
                  name: 'transcript',
                  params: { id: (row as unknown as RunRow).session_id },
                })
              "
            >
              Session
            </Button>
          </template>
        </DataTable>
      </template>
      <Card v-if="output && phone" role="region" :aria-label="outputTitle">
        <CardHeader class="flex items-center justify-between gap-2">
          <CardTitle>{{ outputTitle }}</CardTitle>
          <Button
            variant="ghost"
            size="icon"
            type="button"
            aria-label="Close"
            @click="output = null"
          >
            <XIcon />
          </Button>
        </CardHeader>
        <CardContent>
          <pre
            class="max-h-[60dvh] overflow-auto rounded-md bg-muted p-3 font-mono text-sm whitespace-pre-wrap"
            >{{ output.output }}</pre>
        </CardContent>
      </Card>
    </div>

    <SidePanel :open="!!output && !phone" :title="outputTitle" @close="output = null">
      <pre
        class="max-h-[60dvh] overflow-auto rounded-md bg-muted p-3 font-mono text-sm whitespace-pre-wrap"
        >{{ output?.output }}</pre>
    </SidePanel>
    <SidePanel
      :open="formOpen"
      :title="`Edit ${j?.name ?? ''}`"
      :dirty="formDirty"
      @close="closeForm"
    >
      <JobForm
        v-if="formOpen && detail"
        :job="detail"
        @saved="saved"
        @cancel="closeForm"
        @dirty="formDirty = $event"
      />
    </SidePanel>
  </div>
</template>

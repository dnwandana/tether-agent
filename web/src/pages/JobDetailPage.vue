<script setup lang="ts">
import { computed, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { JobDetail, RunRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { lastSeven } from "../lib/jobbars"
import { useLiveData } from "../lib/live"
import { toast } from "../stores/ui"

const PAGE = 10
const route = useRoute()
const router = useRouter()
const id = computed(() => String(route.params.id))

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const detail = ref<JobDetail | null>(null)
const runs = ref<RunRow[]>([])
const offset = ref(0)
const output = ref<RunRow | null>(null)
const formOpen = ref(false)
const formDirty = ref(false)
const deleting = ref(false)
const phone = ref(false)

const columns = [
  { key: "started", label: "Started" },
  { key: "finished", label: "Finished" },
  { key: "duration", label: "Duration" },
  { key: "status", label: "Status" },
  { key: "delivery", label: "Delivery" },
  { key: "delivery_err", label: "Delivery error" },
  { key: "cost", label: "Cost" },
  { key: "actions", label: "" },
]

const j = computed(() => detail.value?.job)
const done = computed(() => j.value?.icon === "✔")
const bars = computed(() => (detail.value ? lastSeven(detail.value.last7 ?? []) : []))
const pauseText = computed(() =>
  j.value?.pause_reason === "failures"
    ? `Paused after ${j.value.fail_streak} failures`
    : j.value?.pause_reason === "schedule"
      ? "The schedule is not valid"
      : "—",
)

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string | undefined): string {
  const d = new Date(s ?? "")
  if (!s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—"
  return d.toLocaleString()
}

function hhmm(s: string): string {
  return new Date(s).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })
}

const firstRunText = computed(() => {
  const d = new Date(j.value?.next_run ?? "")
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "No runs yet."
  return "No runs yet. The first run is at " + hhmm(j.value!.next_run) + "."
})

const outputTitle = computed(() =>
  output.value ? "Output of the run at " + hhmm(output.value.started_at) : "",
)

function showOutput(r: RunRow): void {
  phone.value = globalThis.matchMedia?.("(max-width: 599px)").matches ?? false
  output.value = r
}

async function loadRuns(o: number): Promise<void> {
  try {
    runs.value = await api<RunRow[]>("GET", `/api/jobs/${id.value}/runs?offset=${o}&limit=${PAGE}`)
    offset.value = o
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

async function load(): Promise<void> {
  try {
    detail.value = await api<JobDetail>("GET", `/api/jobs/${id.value}`)
    runs.value = await api<RunRow[]>(
      "GET",
      `/api/jobs/${id.value}/runs?offset=${offset.value}&limit=${PAGE}`,
    )
    state.value = "ready"
  } catch (e) {
    if (deleting.value && e instanceof ApiError && e.status === 404) {
      await router.push({ name: "jobs" })
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
  }
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
  <div class="page">
    <router-link class="back" :to="{ name: 'jobs' }">‹ Jobs</router-link>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <div v-else-if="detail && j" :aria-busy="stale || undefined">
      <div class="page-h">
        <h1 class="t-20">{{ j.icon }} {{ j.name }}</h1>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <span v-if="done" class="chip chip-plain">done</span>
        <template v-else>
          <button
            v-if="j.enabled"
            class="btn btn-secondary"
            type="button"
            @click="command(`/jobs pause ${j.id}`)"
          >
            ⏸ Pause
          </button>
          <button
            v-else
            class="btn btn-secondary"
            type="button"
            @click="command(`/jobs resume ${j.id}`)"
          >
            ▶ Resume
          </button>
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="!j.enabled"
            @click="command(`/jobs run ${j.id}`)"
          >
            ⚡ Run now
          </button>
          <span v-if="!j.enabled" class="t-12 c-3">Resume the job first.</span>
        </template>
        <button class="btn btn-secondary" type="button" @click="formOpen = true">✎ Edit</button>
        <button class="btn btn-danger-soft" type="button" @click="del()">🗑 Delete…</button>
      </div>

      <div v-if="detail.last_error" class="bad-banner">
        <span>⚠ Failing · {{ j.fail_streak }} in a row.</span
        ><span class="mono">{{ detail.last_error }}</span>
      </div>
      <div v-if="j.pause_reason === 'schedule'" class="warn-banner">
        ⏸ Paused: the schedule {{ j.schedule }} is not valid. Edit the schedule to resume.
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-h"><h3>Definition</h3></div>
          <dl class="kv card-b">
            <dt>Schedule</dt>
            <dd class="mono">{{ j.schedule }}</dd>
            <template v-if="detail.no_agent">
              <dt>Script</dt>
              <dd class="mono">{{ detail.script }}</dd>
            </template>
            <template v-else>
              <dt>Prompt</dt>
              <dd>{{ detail.prompt }}</dd>
              <template v-if="detail.script">
                <dt>Script</dt>
                <dd class="mono">{{ detail.script }}</dd>
              </template>
            </template>
            <dt>Deliver to</dt>
            <dd>{{ detail.deliver_label || detail.deliver || "—" }}</dd>
            <dt>Model · effort</dt>
            <dd>{{ detail.model || "default" }} · {{ detail.effort || "default" }}</dd>
            <dt>Skills</dt>
            <dd>{{ (detail.skills ?? []).join(", ") || "—" }}</dd>
            <dt>Toolsets</dt>
            <dd>{{ (detail.toolsets ?? []).join(", ") || "—" }}</dd>
            <dt>Workdir</dt>
            <dd class="mono">{{ detail.workdir || "—" }}</dd>
            <dt>Options</dt>
            <dd>
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
            <dt>Repeat</dt>
            <dd>{{ detail.repeat_left > 0 ? `${detail.repeat_left} left` : "forever" }}</dd>
          </dl>
        </div>
        <div class="card">
          <div class="card-h"><h3>Health</h3></div>
          <dl class="kv card-b">
            <dt>Last run</dt>
            <dd>{{ when(j.last_run) }}{{ j.last_status ? ` · ${j.last_status}` : "" }}</dd>
            <dt>Next run</dt>
            <dd>{{ when(j.next_run) }}</dd>
            <dt>Last 7</dt>
            <dd>
              <span class="runs7"
                ><i v-for="(b, n) in bars" :key="n" :class="b === 'ok' ? '' : b"
              /></span>
            </dd>
            <dt>This week</dt>
            <dd>
              {{ detail.week_ok }} of {{ detail.week_total }} OK · {{ usd(detail.week_cost_usd) }}
            </dd>
            <dt>Fail streak</dt>
            <dd>
              {{ j.fail_streak
              }}{{ detail.fail_limit > 0 ? ` (pauses at ${detail.fail_limit})` : "" }}
            </dd>
            <dt>Pause reason</dt>
            <dd>{{ pauseText }}</dd>
            <dt>Last output</dt>
            <dd class="mono">{{ detail.last_output || "—" }}</dd>
            <template v-if="detail.watch_limit_hit"
              ><dt>Watch</dt>
              <dd class="c-warn">The watch limit is reached.</dd></template
            >
            <template v-if="detail.watch_output"
              ><dt>Watch output</dt>
              <dd>{{ detail.watch_output }}</dd></template
            >
          </dl>
        </div>
      </div>

      <div class="section-h"><h2>📜 Runs</h2></div>
      <StateBlock
        v-if="j.history_off"
        state="empty"
        text="Run history is off (cron.run_history is 0)."
      />
      <StateBlock
        v-else-if="runs.length === 0 && offset === 0"
        state="empty"
        :text="firstRunText"
        :action="j.enabled ? '⚡ Run now' : undefined"
        @action="command(`/jobs run ${j.id}`)"
      />
      <template v-else>
        <DataTable
          :columns="columns"
          :rows="runs as unknown as Record<string, unknown>[]"
          row-key="id"
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
            <span
              class="chip"
              :class="(row as unknown as RunRow).status === 'ok' ? 'chip-ok' : 'chip-bad'"
              >{{ (row as unknown as RunRow).status }}</span
            >
          </template>
          <template #cell-delivery_err="{ row }">
            <span v-if="(row as unknown as RunRow).delivery_err" class="mono c-bad">{{
              (row as unknown as RunRow).delivery_err
            }}</span>
          </template>
          <template #cell-cost="{ row }">
            {{
              (row as unknown as RunRow).cost_known
                ? usd((row as unknown as RunRow).cost_usd)
                : "—"
            }}{{ (row as unknown as RunRow).cost_flag ? " 💸" : "" }}
          </template>
          <template #cell-actions="{ row }">
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              @click.stop="showOutput(row as unknown as RunRow)"
            >
              Output
            </button>
            <button
              v-if="(row as unknown as RunRow).session_id"
              class="btn btn-secondary btn-sm"
              type="button"
              @click.stop="
                router.push({
                  name: 'transcript',
                  params: { id: (row as unknown as RunRow).session_id },
                })
              "
            >
              Session
            </button>
          </template>
        </DataTable>
        <div class="pager">
          <span class="grow c-2 t-12"
            >{{ runs.length === 0 ? 0 : offset + 1 }}–{{ offset + runs.length }} of
            {{ detail.runs_total }}</span
          >
          <button
            class="btn btn-secondary btn-sm"
            type="button"
            :disabled="offset === 0"
            @click="loadRuns(Math.max(0, offset - PAGE))"
          >
            ◀ Prev
          </button>
          <button
            class="btn btn-secondary btn-sm"
            type="button"
            :disabled="offset + runs.length >= detail.runs_total"
            @click="loadRuns(offset + PAGE)"
          >
            Next ▶
          </button>
        </div>
      </template>
      <div v-if="output && phone" class="card run-output-card">
        <div class="card-h">
          <h3 class="h">{{ outputTitle }}</h3>
          <button
            class="btn btn-ghost btn-icon"
            type="button"
            aria-label="Close"
            @click="output = null"
          >
            ✕
          </button>
        </div>
        <div class="card-b">
          <pre class="code-block run-output">{{ output.output }}</pre>
        </div>
      </div>
    </div>

    <SidePanel :open="!!output && !phone" :title="outputTitle" @close="output = null">
      <pre class="code-block run-output">{{ output?.output }}</pre>
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

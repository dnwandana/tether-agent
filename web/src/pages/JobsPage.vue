<script setup lang="ts">
import { ref } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import type { BriefView, HeldView, JobRow, ReplyView, RunRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import LiveHeader from "../components/LiveHeader.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { replyKind, useCommand } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { lastSeven } from "../lib/jobbars"
import { useLiveData } from "../lib/live"
import { confirm, toast } from "../stores/ui"

const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const brief = ref<BriefView | null>(null)
const jobs = ref<JobRow[]>([])
const held = ref<HeldView[]>([])
const formOpen = ref(false)
const formDirty = ref(false)

const columns = [
  { key: "name", label: "Job" },
  { key: "schedule", label: "Schedule" },
  { key: "next_run", label: "Next run" },
  { key: "runs7", label: "Last 7" },
  { key: "last_cost", label: "Last cost", align: "right" as const },
  { key: "actions", label: "" },
]

const kindIcon: Record<string, string> = {
  failing: "⚠",
  paused_job: "⏸",
  bad_schedule: "⚠️",
  held: "🛡",
  goal: "🎯",
  cost: "💸",
}

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string): string {
  const d = new Date(s)
  if (!s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—"
  return d.toLocaleString()
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
function barsOf(j: JobRow): ("ok" | "bad" | "none")[] {
  return lastSeven([...(j.last7 ?? [])].reverse().map((status) => ({ status }) as RunRow))
}

function okCount(j: JobRow): number {
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
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Jobs</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <button class="btn btn-secondary" type="button" @click="router.push({ name: 'chat' })">
        Ask in chat
      </button>
      <button class="btn btn-primary" type="button" @click="formOpen = true">+ New job</button>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <div v-else :aria-busy="stale || undefined">
      <div v-if="brief" class="card">
        <div class="card-h">
          <h3>Brief</h3>
          <span class="c-3 t-12">what needs attention · no model call</span>
        </div>
        <p v-if="brief.lines.length === 0" class="c-3 card-b">Nothing needs your attention.</p>
        <component
          :is="l.job_id || l.session_id ? 'router-link' : 'div'"
          v-for="(l, i) in brief.lines"
          :key="i"
          class="li"
          v-bind="briefTarget(l)"
        >
          <span aria-hidden="true">{{ kindIcon[l.kind] ?? "•" }}</span>
          <span class="grow"
            ><b>{{ l.name }}</b> <span class="c-2">{{ l.detail }}</span></span
          >
          <span v-if="l.count > 1" class="chip chip-plain">×{{ l.count }}</span>
          <span v-if="l.cost_usd > 0" class="c-2 t-12">{{ usd(l.cost_usd) }}</span>
          <span class="c-3 t-12">{{ age(l.age_seconds) }}</span>
        </component>
        <div class="card-f">
          <span v-if="brief.history_off">Run history is off.</span>
          <span v-else>Jobs: {{ brief.runs_ok }} of {{ brief.runs_total }} runs OK in 24 h</span
          ><span class="grow" />
          <span
            >Spend in 24 h: ${{ brief.spend_usd.toFixed(4)
            }}{{ brief.spend_unpriced ? " · unpriced" : "" }}</span
          >
        </div>
      </div>

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
      >
        <template #cell-name="{ row }">
          <span aria-hidden="true">{{ (row as unknown as JobRow).icon }}</span>
          <router-link :to="{ name: 'job', params: { id: (row as unknown as JobRow).id } }">{{
            (row as unknown as JobRow).name
          }}</router-link>
          <span v-if="(row as unknown as JobRow).watch" class="chip chip-info">watch</span>
        </template>
        <template #cell-schedule="{ row }"
          ><span class="mono">{{ (row as unknown as JobRow).schedule }}</span></template
        >
        <template #cell-next_run="{ row }">{{
          when((row as unknown as JobRow).next_run)
        }}</template>
        <template #cell-runs7="{ row }">
          <span v-if="(row as unknown as JobRow).history_off" class="c-3">—</span>
          <template v-else>
            <span class="runs7"
              ><i
                v-for="(b, n) in barsOf(row as unknown as JobRow)"
                :key="n"
                :class="b === 'ok' ? '' : b"
            /></span>
            {{ " " }}<span class="c-2 t-12">{{ okCount(row as unknown as JobRow) }}/7 ok</span>
          </template>
        </template>
        <template #cell-last_cost="{ row }">
          {{
            (row as unknown as JobRow).last_cost_known
              ? usd((row as unknown as JobRow).last_cost_usd)
              : "—"
          }}
        </template>
        <template #cell-actions="{ row }">
          <span v-if="(row as unknown as JobRow).icon === '✔'" class="chip chip-plain">done</span>
          <template v-else>
            <button
              v-if="(row as unknown as JobRow).enabled"
              class="btn btn-secondary btn-sm"
              type="button"
              @click.stop="command(`/jobs pause ${(row as unknown as JobRow).id}`)"
            >
              ⏸ Pause
            </button>
            <button
              v-else
              class="btn btn-secondary btn-sm"
              type="button"
              @click.stop="command(`/jobs resume ${(row as unknown as JobRow).id}`)"
            >
              ▶ Resume
            </button>
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="!(row as unknown as JobRow).enabled"
              :title="(row as unknown as JobRow).enabled ? undefined : 'Resume the job first'"
              @click.stop="command(`/jobs run ${(row as unknown as JobRow).id}`)"
            >
              ⚡ Run now
            </button>
          </template>
        </template>
      </DataTable>

      <div class="section-h">
        <h2>Held sends</h2>
        <span class="c-3 t-12">The guard held these deliveries. Each lives 24 h.</span>
      </div>
      <StateBlock
        v-if="held.length === 0"
        state="empty"
        text="No held sends."
        hint="A delivery that the outbound guard holds shows here."
      />
      <div v-for="h in held" :key="h.id" class="card">
        <div class="card-h">
          <h3>🛡 {{ h.job_name }} → {{ h.target_title }}</h3>
          <span class="c-3 t-12">{{ heldAge(h.created) }}</span>
        </div>
        <div class="card-b">
          <p>
            <span class="chip chip-plain">{{ h.kind }}</span>
            <span v-if="h.sources.length > 0" class="t-12 c-3">
              Sources: {{ h.sources.join(", ") }}</span
            >
          </p>
          <p class="c-2 t-12">{{ h.reason }}</p>
          <div class="cmd">{{ h.text }}</div>
        </div>
        <div class="card-f">
          <span v-if="!h.can_post" class="t-12 c-3">Telegram is off.</span>
          <span class="grow" />
          <button
            v-for="a in h.actions"
            :key="a.data"
            class="btn btn-sm"
            :class="a.data === 'post' ? 'btn-primary' : 'btn-secondary'"
            type="button"
            :disabled="a.data === 'post' && !h.can_post"
            @click="resolveHeld(h, a.data === 'drop' ? 'drop' : 'post')"
          >
            {{ a.text }}
          </button>
        </div>
      </div>
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

<script setup lang="ts">
import { onMounted, ref } from "vue"
import { useRouter } from "vue-router"
import { api, ApiError, runCommand } from "../api/client"
import type { BriefView, HeldView, JobRow, ReplyView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { confirm, toast } from "../stores/ui"

const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const brief = ref<BriefView | null>(null)
const jobs = ref<JobRow[]>([])
const held = ref<HeldView[]>([])
const formOpen = ref(false)

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
  bad_schedule: "⏸",
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

// Returns one bar class for each of the newest 7 runs. Missing runs are padded at the start.
function bars(j: JobRow): string[] {
  const out = (j.last7 ?? [])
    .slice(-7)
    .map((st) => (st === "ok" ? "" : st === "error" ? "bad" : "none"))
  while (out.length < 7) out.unshift("none")
  return out
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
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function act(text: string): Promise<void> {
  try {
    const r = await runCommand(text)
    if (!("text" in r) || r.text === undefined) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const button = (reply.buttons ?? []).flat().find((b) => b.data.endsWith("confirm"))
      if (button) {
        const [title, ...rest] = reply.text.split("\n")
        const ok = await confirm({
          title,
          body: rest.join("\n").trim(),
          confirmLabel: button.text,
          danger: true,
        })
        if (ok) await act(button.data)
        return
      }
      toast(reply.text, "ok")
    }
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

async function resolveHeld(id: string, action: "post" | "drop"): Promise<void> {
  try {
    const r = await api<ReplyView>("POST", `/api/held/${id}`, { action })
    toast(r.text, "ok")
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

// Returns the link of a brief line: the job, else the goal.
function briefTarget(l: { job_id: string; session_id: string }): Record<string, unknown> {
  if (l.job_id) return { to: { name: "job", params: { id: l.job_id } } }
  if (l.session_id) return { to: { name: "goal", params: { session: l.session_id } } }
  return {}
}

async function saved(): Promise<void> {
  formOpen.value = false
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Jobs</h1>
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
      @retry="load"
    />
    <template v-else>
      <div v-if="brief" class="card">
        <div class="card-h">
          <h3>Brief</h3>
          <span class="c-3 t-12">what needs attention · no model call</span>
        </div>
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
          <span class="c-3 t-12">{{ age(l.age_seconds) }}</span>
        </component>
        <div class="card-f">
          <span>Jobs: {{ brief.runs_ok }} of {{ brief.runs_total }} runs OK in 24 h</span
          ><span class="grow" />
          <span>Spend in 24 h: ${{ brief.spend_usd.toFixed(4) }}</span>
        </div>
      </div>

      <StateBlock
        v-if="jobs.length === 0"
        state="empty"
        text="No jobs yet. Ask in chat, or tap + New job."
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
              ><i v-for="(b, n) in bars(row as unknown as JobRow)" :key="n" :class="b"
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
              @click.stop="act(`/jobs pause ${(row as unknown as JobRow).id}`)"
            >
              ⏸ Pause
            </button>
            <button
              v-else
              class="btn btn-secondary btn-sm"
              type="button"
              @click.stop="act(`/jobs resume ${(row as unknown as JobRow).id}`)"
            >
              ▶ Resume
            </button>
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="!(row as unknown as JobRow).enabled"
              :title="(row as unknown as JobRow).enabled ? undefined : 'Resume the job first'"
              @click.stop="act(`/jobs run ${(row as unknown as JobRow).id}`)"
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
      <StateBlock v-if="held.length === 0" state="empty" text="No held sends" />
      <p v-if="held.length === 0" class="c-3 t-12">
        A delivery that the outbound guard holds shows up here.
      </p>
      <div v-for="h in held" :key="h.id" class="card">
        <div class="card-h">
          <h3>🛡 {{ h.job_name }} → {{ h.target_title }}</h3>
          <span class="c-3 t-12">{{ heldAge(h.created) }}</span>
        </div>
        <div class="card-b">
          <p class="c-2 t-12">{{ h.reason }}</p>
          <div class="cmd">{{ h.text }}</div>
        </div>
        <div class="card-f">
          <span class="grow" />
          <button
            v-for="a in h.actions"
            :key="a.data"
            class="btn btn-sm"
            :class="a.data === 'post' ? 'btn-primary' : 'btn-secondary'"
            type="button"
            @click="resolveHeld(h.id, a.data === 'drop' ? 'drop' : 'post')"
          >
            {{ a.text }}
          </button>
        </div>
      </div>
    </template>

    <SidePanel :open="formOpen" title="New job" @close="formOpen = false">
      <JobForm v-if="formOpen" :job="null" @saved="saved" @cancel="formOpen = false" />
    </SidePanel>
  </div>
</template>

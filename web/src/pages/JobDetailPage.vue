<script setup lang="ts">
import { computed, onMounted, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError, runCommand } from "../api/client"
import type { JobDetail, ReplyView, RunRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import JobForm from "../components/JobForm.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { confirm, toast } from "../stores/ui"

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

const columns = [
  { key: "started", label: "Started" },
  { key: "duration", label: "Duration" },
  { key: "status", label: "Status" },
  { key: "delivery", label: "Delivery" },
  { key: "cost", label: "Cost" },
  { key: "actions", label: "" },
]

const j = computed(() => detail.value?.job)
const done = computed(() => j.value?.icon === "✔")

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string | undefined): string {
  const d = new Date(s ?? "")
  if (!s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—"
  return d.toLocaleString()
}

function bars(d: JobDetail): string[] {
  const out: string[] = d.last7.map((r) => (r.status === "ok" ? "ok" : "bad")).reverse()
  while (out.length < 7) out.push("none")
  return out
}

async function loadRuns(o: number): Promise<void> {
  try {
    runs.value = await api<RunRow[]>("GET", `/api/jobs/${id.value}/runs?offset=${o}&limit=${PAGE}`)
    offset.value = o
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
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
      if (text.startsWith("/jobs delete ")) {
        await router.push({ name: "jobs" })
        return
      }
    }
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

async function saved(): Promise<void> {
  formOpen.value = false
  await load()
}

onMounted(load)
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
      @retry="load"
    />
    <template v-else-if="detail && j">
      <div class="page-h">
        <h1 class="t-20">{{ j.icon }} {{ j.name }}</h1>
        <span v-if="done" class="chip chip-plain">done</span>
        <template v-else>
          <button
            v-if="j.enabled"
            class="btn btn-secondary"
            type="button"
            @click="act(`/jobs pause ${j.id}`)"
          >
            ⏸ Pause
          </button>
          <button
            v-else
            class="btn btn-secondary"
            type="button"
            @click="act(`/jobs resume ${j.id}`)"
          >
            ▶ Resume
          </button>
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="!j.enabled"
            :title="j.enabled ? undefined : 'Resume the job first'"
            @click="act(`/jobs run ${j.id}`)"
          >
            ⚡ Run now
          </button>
        </template>
        <button class="btn btn-secondary" type="button" @click="formOpen = true">✎ Edit</button>
        <button class="btn btn-danger-soft" type="button" @click="act(`/jobs delete ${j.id}`)">
          🗑 Delete…
        </button>
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
            <dt>Prompt</dt>
            <dd>{{ detail.prompt }}</dd>
            <dt>Deliver to</dt>
            <dd>{{ detail.deliver || "—" }}</dd>
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
                ><i v-for="(b, n) in bars(detail)" :key="n" :class="b === 'ok' ? '' : b"
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
            <dd>{{ j.pause_reason || "—" }}</dd>
            <template v-if="detail.watch_output"
              ><dt>Watch</dt>
              <dd>{{ detail.watch_output }}</dd></template
            >
          </dl>
        </div>
      </div>

      <div class="section-h"><h2>📜 Runs</h2></div>
      <DataTable
        :columns="columns"
        :rows="runs as unknown as Record<string, unknown>[]"
        row-key="id"
      >
        <template #cell-started="{ row }">{{
          when((row as unknown as RunRow).started_at)
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
        <template #cell-cost="{ row }">
          {{ (row as unknown as RunRow).cost_known ? usd((row as unknown as RunRow).cost_usd) : "—"
          }}{{ (row as unknown as RunRow).cost_flag ? " 💸" : "" }}
        </template>
        <template #cell-actions="{ row }">
          <button
            class="btn btn-secondary btn-sm"
            type="button"
            @click.stop="output = row as unknown as RunRow"
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
      <div v-if="output" class="card">
        <div class="card-h"><h3>Output</h3></div>
        <div class="card-b">
          <div class="cmd" style="white-space: pre-wrap">{{ output.output }}</div>
        </div>
      </div>
    </template>

    <SidePanel :open="formOpen" :title="`Edit ${j?.name ?? ''}`" @close="formOpen = false">
      <JobForm v-if="formOpen && detail" :job="detail" @saved="saved" @cancel="formOpen = false" />
    </SidePanel>
  </div>
</template>

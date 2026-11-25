<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { api, ApiError } from "../api/client"
import type { SkillDetailView, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import FileViewer from "../components/FileViewer.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { followTask, useLiveData } from "../lib/live"
import { renderDocument } from "../lib/markdown"
import { TONE, chips, type Tone } from "../lib/skills"
import type { Column } from "../lib/table"
import { fullTime, shortTime } from "../lib/time"
import type { ResultNoteData } from "../lib/command"
import { confirm, isSecure, listLink, toast, ui } from "../stores/ui"

type Action = "update" | "uninstall" | "reset"
type Running = { id: string; action: Action; waiting: boolean }

const LOST = "The gateway restarted. The result of the task is lost."

const route = useRoute()
const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const view = ref<SkillDetailView | null>(null)
const note = ref<ResultNoteData | null>(null)
const running = ref<Running | null>(null)
const forceOffer = ref<"update" | null>(null)
const manual = ref<{ findings: string } | null>(null)
const file = ref<{ path: string; line: number | null } | null>(null)
const busy = computed(() => running.value !== null)

const name = computed(() => String(route.params.name))
const html = computed(() => renderDocument(view.value?.body ?? ""))
const hasLock = computed(
  () => !!view.value && (view.value.skill.source !== "" || view.value.installed_at !== ""),
)
const scanned = computed(() => !!view.value && view.value.skill.trust !== "builtin")

const findingColumns: Column[] = [
  { key: "description", label: "Finding", type: "text" },
  { key: "category", label: "Category", type: "text", optional: true },
  { key: "pattern", label: "Pattern", type: "text" },
  { key: "where", label: "File", type: "text" },
  { key: "severity", label: "Severity", type: "badge" },
  { key: "match", label: "Match", type: "text", optional: true },
]
type Finding = SkillDetailView["findings"][number]
const f = (x: unknown) => x as Finding
const findingRows = computed(() =>
  (view.value?.findings ?? []).map((x, i) => ({
    ...x,
    key: String(i),
    where: `${x.file}:${x.line}`,
  })),
)

function verdictTone(v: string): Tone {
  if (v === "safe") return "ok"
  if (v === "dangerous") return "bad"
  return "warn"
}

function severityTone(s: string): Tone {
  return s === "high" || s === "critical" ? "bad" : "warn"
}

function when(s: string): string {
  return shortTime(s) || "—"
}

// Returns true when the route can open the file. Other paths are refused by the server.
function canOpen(path: string): boolean {
  return !!view.value && view.value.files.includes(path)
}

function openFile(path: string, line: number | null): void {
  file.value = { path, line }
}

// The severities from the worst to the least.
const SEVERITIES = ["critical", "high", "medium", "low"]
// The Files card shows this many files before "Show all".
const FILES_SHOWN = 8
const allFiles = ref(false)

function rank(sev: string): number {
  const i = SEVERITIES.indexOf(sev)
  return i < 0 ? SEVERITIES.length : i
}

// Returns the finding count and the worst severity of each file with findings.
const flags = computed<Map<string, { n: number; worst: string }>>(() => {
  const m = new Map<string, { n: number; worst: string }>()
  for (const x of view.value?.findings ?? []) {
    const cur = m.get(x.file)
    if (!cur) m.set(x.file, { n: 1, worst: x.severity })
    else {
      cur.n++
      if (rank(x.severity) < rank(cur.worst)) cur.worst = x.severity
    }
  }
  return m
})

// Puts the files with findings first, so that "Show all" never hides a flagged file.
// Each group is in name order.
const files = computed<string[]>(() => {
  const all = view.value?.files ?? []
  const flagged = all.filter((p) => flags.value.has(p)).sort((a, b) => a.localeCompare(b))
  const rest = all.filter((p) => !flags.value.has(p)).sort((a, b) => a.localeCompare(b))
  return [...flagged, ...rest]
})

// Returns the count of each severity from the worst, for example "1 critical, 2 high".
const severityText = computed<string>(() => {
  const count = new Map<string, number>()
  for (const x of view.value?.findings ?? [])
    count.set(x.severity, (count.get(x.severity) ?? 0) + 1)
  const known = SEVERITIES.filter((s) => count.has(s))
  const other = [...count.keys()].filter((s) => !SEVERITIES.includes(s))
  return [...known, ...other].map((s) => `${count.get(s)} ${s}`).join(", ")
})

// Returns the text after "Used by:". null means that the server could not read the jobs.
const usedBy = computed<string>(() => {
  const jobs = view.value?.cron_jobs
  if (jobs == null) return "unknown"
  return jobs.length > 0 ? jobs.join(", ") : "no cron jobs"
})

function scrollToFindings(): void {
  document.getElementById("skill-findings")?.scrollIntoView({ block: "start" })
}

async function load(): Promise<void> {
  try {
    view.value = await api<SkillDetailView>("GET", `/api/skills/${encodeURIComponent(name.value)}`)
    state.value = "ready"
  } catch (e) {
    // A failed reload keeps the shown skill. A 404 means the skill is gone, so it shows the error.
    if (state.value === "ready" && !(e instanceof ApiError && e.status === 404)) {
      toast(friendlyError(e), "error")
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

// Returns true for the end of a task of the kind skills.
function isEnd(_name: string, data: unknown): boolean {
  const t = data as Partial<TaskView> | null
  return !!t && t.kind === "skills" && (t.state === "done" || t.state === "error")
}

const verbs: Record<Action, [string, string]> = {
  update: ["Updating", "Updated"],
  uninstall: ["Uninstalling", "Uninstalled"],
  reset: ["Resetting", "Reset"],
}

// Shows the end of a task. The rules have a fixed order.
function settle(t: TaskView, action: Action): void {
  running.value = null
  const title = `/${name.value}`
  if (t.state === "lost") {
    note.value = { title, at: new Date(), text: LOST, kind: "error" }
  } else if (t.state === "done") {
    if (action === "uninstall") {
      void router.push(listLink("skills"))
      return
    }
    toast(`${verbs[action][1]} /${name.value}. It loads in new sessions.`, "ok")
    void load()
  } else if (t.code === "need_force") {
    note.value = { title, at: new Date(), text: t.text, kind: "warn" }
    forceOffer.value = "update"
  } else if (t.code === "need_override") {
    manual.value = { findings: t.text }
  } else if (action === "uninstall" && /(^|\n)Cancelled\.(\n|$)/.test(t.text)) {
    toast("Not removed.", "info")
  } else {
    note.value = { title, at: new Date(), text: t.text, kind: "error" }
  }
}

const stops = new Set<() => void>()
// True after the page unmounts. An await can end after that.
let gone = false

// Follows one task. Each end of the task goes to settle.
function track(id: string, action: Action): void {
  running.value = { id, action, waiting: false }
  note.value = null
  forceOffer.value = null
  manual.value = null
  let stop: (() => void) | null = null
  let ended = false
  stop = followTask(id, (t) => {
    if (ended) return
    if (t.state === "running") {
      if (running.value && running.value.id === id) running.value.waiting = t.waiting
      return
    }
    ended = true
    if (stop) {
      stop()
      stops.delete(stop)
    }
    settle(t, action)
  })
  // A follow that starts after the page is gone stops at once.
  if (ended || gone) stop()
  else stops.add(stop)
}

onBeforeUnmount(() => {
  gone = true
  stops.forEach((fn) => fn())
  stops.clear()
})

async function start(action: Action, force?: boolean): Promise<void> {
  let id = ""
  try {
    const path = `/api/skills/${encodeURIComponent(name.value)}/${action}`
    const r = force
      ? await api<{ task_id: string }>("POST", path, { force: true })
      : await api<{ task_id: string }>("POST", path)
    id = r.task_id
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  toast(`${verbs[action][0]} /${name.value}…`, "info")
  track(id, action)
}

// Posts one skill action. Reset and uninstall ask a confirm first.
async function skillAction(action: Action): Promise<void> {
  const n = name.value
  if (action === "reset") {
    const ok = await confirm({
      title: `Reset /${n}?`,
      body: "The bundled version replaces your local changes.",
      confirmLabel: "Reset",
    })
    if (!ok) return
  }
  if (action === "uninstall") {
    const ok = await confirm({
      title: `Remove /${n}?`,
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    if (!ok) return
  }
  await start(action)
}

async function updateAnyway(): Promise<void> {
  const ok = await confirm({
    title: `Update /${name.value} anyway?`,
    body: "The skill guard rated it caution. A confirm card follows in ⏳ Waiting.",
    confirmLabel: "Update anyway",
    danger: true,
  })
  if (!ok) return
  await start("update", true)
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast("Copied.")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

const { stale, updatedAt, refresh } = useLiveData(load, { events: ["task"], filter: isEnd })
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <router-link
      class="text-sm text-muted-foreground hover:text-foreground"
      :to="listLink('skills')"
      >‹ Skills</router-link
    >
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <template v-else-if="view">
      <div class="flex flex-wrap items-center gap-3">
        <h1 class="text-xl font-semibold tracking-tight">/{{ view.skill.name }}</h1>
        <Badge variant="outline" :class="TONE.plain" data-tone="plain">{{
          view.skill.trust
        }}</Badge>
        <Badge v-if="view.override" variant="outline" :class="TONE.warn" data-tone="warn"
          >Owner override</Badge
        >
        <Badge v-if="busy" variant="outline" :class="TONE.info" data-tone="info">Running…</Badge>
        <Badge
          v-for="ch in chips(view.skill)"
          :key="ch.text"
          variant="outline"
          :class="TONE[ch.tone]"
          :data-tone="ch.tone"
          >{{ ch.text }}</Badge
        >
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <Button
          v-if="view.skill.actions.includes('update') && !view.skill.pinned"
          variant="outline"
          :disabled="busy"
          @click="skillAction('update')"
        >
          Update
        </Button>
        <Button
          v-if="view.skill.actions.includes('reset')"
          variant="outline"
          :disabled="busy"
          @click="skillAction('reset')"
        >
          Reset
        </Button>
        <Button
          v-if="view.skill.actions.includes('uninstall')"
          variant="destructive"
          :disabled="busy"
          @click="skillAction('uninstall')"
        >
          Uninstall
        </Button>
      </div>
      <p class="text-muted-foreground">{{ view.skill.description }}</p>
      <ResultNote :note="note" @close="note = null" />
      <div v-if="forceOffer" class="flex items-center gap-2">
        <Button variant="outline" :disabled="busy" @click="updateAnyway">Update anyway…</Button>
      </div>
      <div v-if="running?.waiting" class="flex items-center gap-2">
        <span class="flex-1">Answer the card in ⏳ Waiting.</span>
        <Button variant="outline" size="sm" @click="ui.trayOpen = true">Open Waiting</Button>
      </div>

      <div class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18.75rem] lg:items-start">
        <aside
          aria-label="Skill facts"
          class="flex min-w-0 flex-col gap-4 lg:sticky lg:top-4 lg:col-start-2 lg:row-start-1 lg:max-h-[calc(100dvh-5rem)] lg:overflow-y-auto"
        >
          <Card role="region" aria-label="Use it" class="gap-3 py-4">
            <CardHeader class="px-4"><CardTitle>Use it</CardTitle></CardHeader>
            <CardContent class="flex flex-col gap-1 px-4 text-sm">
              <p>
                Run it in chat: <code class="font-mono break-all">/{{ view.skill.name }}</code>
              </p>
              <p v-if="view.skill.required_env.length" class="break-words">
                Needs: {{ view.skill.required_env.join(", ") }}
              </p>
              <p class="break-words">Used by: {{ usedBy }}</p>
            </CardContent>
          </Card>

          <Card v-if="scanned" role="region" aria-label="Guard scan" class="gap-3 py-4">
            <CardHeader class="px-4"><CardTitle>Guard scan</CardTitle></CardHeader>
            <CardContent class="flex flex-col gap-1 px-4 text-sm">
              <div class="flex flex-wrap items-center gap-2">
                <Badge
                  v-if="view.skill.verdict"
                  variant="outline"
                  :class="TONE[verdictTone(view.skill.verdict)]"
                  :data-tone="verdictTone(view.skill.verdict)"
                  >{{ view.skill.verdict }}</Badge
                >
                <span class="text-xs text-muted-foreground"
                  >{{ view.findings.length }}
                  {{ view.findings.length === 1 ? "finding" : "findings" }} ·
                  {{ view.unscanned.length }} not scanned</span
                >
              </div>
              <p v-if="severityText" class="text-muted-foreground">{{ severityText }}</p>
              <p v-if="view.unscanned.length" class="break-all text-muted-foreground">
                Not scanned: {{ view.unscanned.join(", ") }}
              </p>
              <Button
                v-if="view.findings.length"
                variant="link"
                class="h-auto self-start p-0"
                @click="scrollToFindings"
                >See the findings ↓</Button
              >
              <p v-else class="text-muted-foreground">No findings</p>
            </CardContent>
          </Card>

          <Card v-if="view.files.length" role="region" aria-label="Files" class="gap-3 py-4">
            <CardHeader class="px-4">
              <CardTitle>Files · {{ view.files.length }}</CardTitle>
            </CardHeader>
            <CardContent class="flex flex-col gap-1 px-4 text-sm">
              <div
                v-for="p in files.slice(0, FILES_SHOWN)"
                :key="p"
                class="flex min-w-0 items-center gap-2"
              >
                <Button
                  variant="link"
                  class="h-auto min-w-0 shrink justify-start p-0 font-mono"
                  :title="p"
                  @click="openFile(p, null)"
                  ><span class="truncate">{{ p }}</span></Button
                >
                <Badge
                  v-if="flags.get(p)"
                  variant="outline"
                  :class="TONE[severityTone(flags.get(p)!.worst)]"
                  :data-tone="severityTone(flags.get(p)!.worst)"
                  :title="`${flags.get(p)!.n} ${flags.get(p)!.n === 1 ? 'finding' : 'findings'}`"
                  >{{ flags.get(p)!.n }}</Badge
                >
              </div>
              <Collapsible v-if="files.length > FILES_SHOWN" v-model:open="allFiles">
                <CollapsibleContent class="flex flex-col gap-1">
                  <div
                    v-for="p in files.slice(FILES_SHOWN)"
                    :key="p"
                    class="flex min-w-0 items-center gap-2"
                  >
                    <Button
                      variant="link"
                      class="h-auto min-w-0 shrink justify-start p-0 font-mono"
                      :title="p"
                      @click="openFile(p, null)"
                      ><span class="truncate">{{ p }}</span></Button
                    >
                    <Badge
                      v-if="flags.get(p)"
                      variant="outline"
                      :class="TONE[severityTone(flags.get(p)!.worst)]"
                      :data-tone="severityTone(flags.get(p)!.worst)"
                      :title="`${flags.get(p)!.n} ${flags.get(p)!.n === 1 ? 'finding' : 'findings'}`"
                      >{{ flags.get(p)!.n }}</Badge
                    >
                  </div>
                </CollapsibleContent>
                <CollapsibleTrigger as-child>
                  <Button v-if="!allFiles" variant="link" class="h-auto self-start p-0"
                    >Show all {{ files.length }}</Button
                  >
                </CollapsibleTrigger>
              </Collapsible>
            </CardContent>
          </Card>

          <Card role="region" aria-label="Source" class="gap-3 py-4">
            <CardHeader class="px-4"><CardTitle>Source</CardTitle></CardHeader>
            <CardContent class="px-4">
              <dl
                v-if="hasLock"
                class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm"
              >
                <dt class="text-muted-foreground">Source</dt>
                <dd class="min-w-0 truncate font-mono" :title="view.skill.source">
                  {{ view.skill.source || "—" }}
                </dd>
                <dt class="text-muted-foreground">Ref</dt>
                <dd class="min-w-0 truncate font-mono" :title="view.ref">{{ view.ref || "—" }}</dd>
                <dt class="text-muted-foreground">Commit</dt>
                <dd class="min-w-0 truncate font-mono" :title="view.skill.commit">
                  {{ view.skill.commit || "—" }}
                </dd>
                <dt class="text-muted-foreground">Installed</dt>
                <dd data-test="installed-at">
                  <span class="whitespace-nowrap" :title="fullTime(view.installed_at)">{{
                    when(view.installed_at)
                  }}</span>
                </dd>
                <dt class="text-muted-foreground">Updated</dt>
                <dd data-test="updated-at">
                  <span class="whitespace-nowrap" :title="fullTime(view.updated_at)">{{
                    when(view.updated_at)
                  }}</span>
                </dd>
                <dt class="text-muted-foreground">Content hash</dt>
                <dd class="min-w-0 truncate font-mono" :title="view.content_hash">
                  {{ view.content_hash || "—" }}
                </dd>
              </dl>
              <p v-else class="text-sm text-muted-foreground">Not installed from a source.</p>
            </CardContent>
          </Card>
        </aside>

        <Card role="region" aria-label="SKILL.md" class="min-w-0 lg:col-start-1 lg:row-start-1">
          <CardHeader><CardTitle>SKILL.md</CardTitle></CardHeader>
          <CardContent>
            <div class="typeset max-w-[72ch]" v-html="html" />
            <p v-if="view.body_cut" class="mt-4 text-sm text-muted-foreground">
              The text is cut at 256 KiB. Open SKILL.md in Files to read it all.
            </p>
          </CardContent>
        </Card>
      </div>

      <section
        v-if="scanned && view.findings.length"
        id="skill-findings"
        aria-labelledby="skill-findings-title"
        class="flex scroll-mt-4 flex-col gap-3"
      >
        <h2 id="skill-findings-title" class="text-lg font-semibold">
          Guard findings · {{ view.findings.length }}
        </h2>
        <DataTable
          :columns="findingColumns"
          :rows="findingRows as unknown as Record<string, unknown>[]"
          row-key="key"
          page-key="skill-findings"
          page-label="Guard findings"
        >
          <template #cell-pattern="{ row }"
            ><span class="font-mono text-xs">{{ f(row).pattern }}</span></template
          >
          <template #cell-where="{ row }">
            <Button
              v-if="canOpen(f(row).file)"
              variant="link"
              class="h-auto p-0 font-mono"
              @click="openFile(f(row).file, f(row).line)"
            >
              {{ f(row).file }}:{{ f(row).line }}
            </Button>
            <span v-else class="font-mono text-xs">{{ f(row).file }}:{{ f(row).line }}</span>
          </template>
          <template #cell-severity="{ row }"
            ><Badge
              variant="outline"
              :class="TONE[severityTone(f(row).severity)]"
              :data-tone="severityTone(f(row).severity)"
              >{{ f(row).severity }}</Badge
            ></template
          >
          <template #cell-match="{ row }"
            ><code class="font-mono text-xs">{{ f(row).match }}</code></template
          >
        </DataTable>
      </section>

      <SidePanel :open="file !== null" :title="file?.path ?? ''" @close="file = null">
        <FileViewer v-if="file" :skill="view.skill.name" :path="file.path" :line="file.line" />
      </SidePanel>
      <SidePanel :open="manual !== null" title="Update from the terminal" @close="manual = null">
        <template v-if="manual">
          <pre
            class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
          >
tether skills update {{ view.skill.name }}</pre>
          <p>The terminal asks you to type the name of the skill.</p>
          <Button
            v-if="isSecure()"
            variant="outline"
            @click="copy(`tether skills update ${view.skill.name}`)"
          >
            Copy command
          </Button>
          <p v-else class="text-sm text-muted-foreground">Needs HTTPS or localhost</p>
          <pre
            v-if="manual.findings"
            class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
            >{{ manual.findings }}</pre>
        </template>
      </SidePanel>
    </template>
  </div>
</template>

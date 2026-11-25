<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { useRouter } from "vue-router"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import { api } from "../api/client"
import type { BackupView, BootstrapView, ConfigView, TaskView, UpdateInfo } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import LogViewer from "../components/LogViewer.vue"
import ResultNote from "../components/ResultNote.vue"
import type { Column } from "../lib/table"
import StateBlock from "../components/StateBlock.vue"
import Tabs from "../components/Tabs.vue"
import { friendlyError } from "../lib/errors"
import { type ResultNoteData, useCommand } from "../lib/command"
import { followTask, useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { confirm, isSecure, toast } from "../stores/ui"

const router = useRouter()
const { run } = useCommand()

const LOST_TEXT = "The gateway restarted. The result of the task is lost."

const config = ref<ConfigView | null>(null)
const configState = ref<"loading" | "ready" | "error">("loading")
const configError = ref("")
const boot = ref<BootstrapView | null>(null)
const drain = computed(() => boot.value?.drain_s ?? 180)
const update = ref<UpdateInfo | null>(null)
const checking = ref(false)
const backups = ref<BackupView | null>(null)
const chosen = ref("")
const group = ref("")
const backingUp = ref(false)
const backupNote = ref<ResultNoteData | null>(null)
const updateNote = ref<ResultNoteData | null>(null)
const restartNote = ref<ResultNoteData | null>(null)
const stops = new Set<() => void>()
// True after the page unmounts. An await can end after that.
let gone = false
const updating = ref(false)
const restarting = ref(false)

async function loadConfig(): Promise<void> {
  try {
    config.value = await api<ConfigView>("GET", "/api/config")
    configState.value = "ready"
  } catch (e) {
    configError.value = friendlyError(e)
    configState.value = "error"
  }
}

async function loadBoot(): Promise<void> {
  try {
    boot.value = await api<BootstrapView>("GET", "/api/bootstrap")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

async function loadBackups(): Promise<void> {
  try {
    const v = await api<BackupView>("GET", "/api/backup")
    const files = [...(v.files ?? [])].sort((a, b) => b.modified.localeCompare(a.modified))
    backups.value = { ...v, files }
    if (!chosen.value || !files.some((f) => f.name === chosen.value))
      chosen.value = files[0]?.name ?? ""
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

async function checkUpdate(): Promise<void> {
  checking.value = true
  try {
    update.value = await api<UpdateInfo>("GET", "/api/update")
  } catch (e) {
    toast(friendlyError(e), "error")
  } finally {
    checking.value = false
  }
}

async function load(): Promise<void> {
  await Promise.all([loadConfig(), loadBoot(), loadBackups(), checkUpdate()])
}

// Reloads on the end of a backup or update task, and on each restart event.
function isLive(name: string, data: unknown): boolean {
  if (name === "restart") return true
  if (name !== "task") return false
  const t = data as Partial<TaskView> | null
  return (
    (t?.kind === "backup" || t?.kind === "update") && (t.state === "done" || t.state === "error")
  )
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["task", "restart"],
  filter: isLive,
})

function note(title: string, text: string, kind: ResultNoteData["kind"]): ResultNoteData {
  return { title, at: new Date(), text, kind }
}

async function runUpdate(): Promise<void> {
  const u = update.value
  if (!u || !u.newer || !u.writable || updating.value) return
  const ok = await confirm({
    title: `Update to ${u.latest} and restart?`,
    body: `The gateway downloads the release, checks its SHA-256, runs it once, swaps the binary, and restarts. Running turns finish first (up to ${drain.value} s).`,
    confirmLabel: "Update and restart",
    danger: true,
  })
  if (!ok || updating.value) return
  updating.value = true
  updateNote.value = null
  try {
    const r = await api<{ task_id: string }>("POST", "/api/update", { confirm: true })
    const stop = followTask(r.task_id, (t) => {
      if (t.state === "error") updateNote.value = note("Update", t.text, "error")
      else if (t.state === "lost") updateNote.value = note("Update", LOST_TEXT, "error")
      else if (t.state !== "done") return
      updating.value = false
      stop()
      stops.delete(stop)
    })
    // A follow that starts after the page is gone stops at once.
    if (gone) stop()
    else stops.add(stop)
  } catch (e) {
    toast(friendlyError(e), "error")
    updating.value = false
  }
}

async function restart(): Promise<void> {
  const ok = await confirm({
    title: "Restart the gateway?",
    body: `Running turns finish first (up to ${drain.value} s). This page reconnects by itself.`,
    confirmLabel: "Restart",
    danger: true,
  })
  if (!ok || restarting.value) return
  restarting.value = true
  restartNote.value = null
  try {
    await run("/restart", { result: restartNote, title: "Restart" })
  } finally {
    restarting.value = false
  }
}

const num = (n: number): string => n.toLocaleString("en-US")

// Returns the text of the backup report, one item on each line.
function report(t: TaskView): string {
  const lines = [t.text]
  const b = t.backup
  if (b) {
    lines.push(
      `Sessions: ${num(b.sessions)} · Messages: ${num(b.messages)} · Cron jobs: ${num(b.cron_jobs)}`,
    )
    if (b.replaced?.length) lines.push(`Replaced keys: ${b.replaced.join(", ")}`)
    for (const w of b.warnings ?? []) lines.push(`Warning: ${w}`)
    if (b.skipped?.length) lines.push(`Skipped: ${b.skipped.join(", ")}`)
  }
  return lines.join("\n")
}

async function createBackup(): Promise<void> {
  if (backingUp.value) return
  backingUp.value = true
  backupNote.value = null
  try {
    const r = await api<{ task_id: string }>("POST", "/api/backup")
    const stop = followTask(r.task_id, (t) => {
      if (t.state === "done") {
        const warn = (t.backup?.warnings ?? []).length > 0
        backupNote.value = note("Backup", report(t), warn ? "warn" : "info")
        void loadBackups()
      } else if (t.state === "error") backupNote.value = note("Backup", t.text, "error")
      else if (t.state === "lost") backupNote.value = note("Backup", LOST_TEXT, "error")
      else return
      backingUp.value = false
      stop()
      stops.delete(stop)
    })
    // A follow that starts after the page is gone stops at once.
    if (gone) stop()
    else stops.add(stop)
  } catch (e) {
    toast(friendlyError(e), "error")
    backingUp.value = false
  }
}

function restoreLines(): string[] {
  const dir = backups.value?.dir ?? "~/.tether/backups"
  return [
    "systemctl --user stop tether.service",
    `tether restore ${dir}/${chosen.value || "<file>"}`,
    "systemctl --user start tether.service",
  ]
}
const restoreText = computed(() =>
  restoreLines()
    .map((l, i) => `${i + 1}. ${l}`)
    .join("\n"),
)

async function copyRestore(): Promise<void> {
  try {
    await navigator.clipboard.writeText(restoreLines().join("\n"))
    toast("Copied.", "ok")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

// Splits the config text into groups. A line that starts a top-level key opens a group.
function groups(text: string): { key: string; text: string }[] {
  const out: { key: string; text: string }[] = []
  for (const line of text.split("\n")) {
    const m = /^([A-Za-z_][\w-]*):/.exec(line)
    if (m) out.push({ key: m[1], text: line })
    else if (out.length === 0) out.push({ key: "", text: line })
    else out[out.length - 1].text += "\n" + line
  }
  return out
}
const configGroups = computed(() => groups(config.value?.text ?? ""))
const groupKeys = computed(() => configGroups.value.map((g) => g.key).filter((k) => k !== ""))
const shownConfig = computed(() => {
  const text = config.value?.text ?? ""
  if (!group.value) return text
  return configGroups.value.find((g) => g.key === group.value)?.text ?? text
})

const tab = computed({
  get: () => "ops",
  set: (v: string) => void router.push({ name: v }),
})

const checkedAt = computed(() => {
  const raw = update.value?.checked_at
  if (!raw) return ""
  const d = new Date(raw)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return ""
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
})

// Downloads a file with the session header and saves it from a blob URL.
async function download(path: string, name: string): Promise<void> {
  try {
    const headers: Record<string, string> = {}
    let sid: string | null = null
    try {
      sid = localStorage.getItem("tether.session")
    } catch {
      sid = null
    }
    if (sid) headers["X-Tether-Session"] = sid
    const r = await fetch(path, { headers })
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    const url = URL.createObjectURL(await r.blob())
    const a = document.createElement("a")
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Safari on iOS can start the download after click() returns.
    setTimeout(() => URL.revokeObjectURL(url), 30_000)
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

const mb = (n: number): string => (n / (1024 * 1024)).toFixed(1)
const rows = computed(() =>
  (backups.value?.files ?? []).map((f) => ({
    name: f.name,
    size: `${mb(f.size)} MB`,
    modified: f.modified,
  })),
)
const cols: Column[] = [
  { key: "name", label: "File", type: "text" },
  { key: "size", label: "Size", type: "num" },
  { key: "modified", label: "Date", type: "date" },
  { key: "actions", label: "", type: "act", width: "7rem" },
]

onBeforeUnmount(() => {
  gone = true
  stops.forEach((f) => f())
  stops.clear()
})
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">System</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
    </div>
    <Tabs
      v-model="tab"
      :tabs="[
        { id: 'system', label: 'Health' },
        { id: 'ops', label: 'Operations' },
      ]"
    />

    <Card>
      <CardHeader>
        <CardTitle>config.yaml</CardTitle>
        <CardDescription>read-only · a change needs a restart</CardDescription>
        <CardAction>
          <NativeSelect v-if="configState === 'ready'" v-model="group" aria-label="Config group">
            <NativeSelectOption value="">All keys</NativeSelectOption>
            <NativeSelectOption v-for="k in groupKeys" :key="k" :value="k">{{
              k
            }}</NativeSelectOption>
          </NativeSelect>
        </CardAction>
      </CardHeader>
      <CardContent>
        <StateBlock v-if="configState === 'loading'" state="loading" />
        <StateBlock
          v-else-if="configState === 'error'"
          state="error"
          :text="configError"
          action="Retry"
          @action="loadConfig"
        />
        <pre v-else class="max-h-96 overflow-auto rounded-md bg-muted p-3 font-mono text-xs">{{
          shownConfig
        }}</pre>
      </CardContent>
    </Card>

    <div class="grid grid-cols-1 gap-4 tab:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>Update</CardTitle>
        </CardHeader>
        <CardContent class="flex flex-col gap-2 text-sm">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span>tether {{ boot?.version ?? update?.current ?? "" }}</span>
            <span v-if="checking" class="text-muted-foreground">checking…</span>
            <span v-else-if="!update" class="text-muted-foreground">not checked yet</span>
            <span v-else-if="update.newer">
              {{ update.latest }} is available
              <a
                class="underline"
                :href="update.notes_url"
                target="_blank"
                rel="noopener noreferrer"
                >release notes</a
              >
            </span>
            <span v-else class="text-muted-foreground">up to date</span>
          </div>
          <div v-if="checkedAt" class="text-xs text-muted-foreground">Checked {{ checkedAt }}</div>
          <ResultNote :note="updateNote" @close="updateNote = null" />
          <div v-if="update && !update.newer" class="text-xs text-muted-foreground">
            You have the newest version.
          </div>
          <div
            v-if="update && update.newer && !update.writable"
            class="text-xs text-muted-foreground"
          >
            The binary at {{ update.path }} is not writable by this user. Run `tether update` as a
            user who can write it.
          </div>
        </CardContent>
        <CardFooter class="flex flex-wrap gap-2">
          <Button variant="outline" :disabled="checking" @click="checkUpdate"
            >Check for update</Button
          >
          <Button
            :disabled="updating || !(update && update.newer && update.writable)"
            @click="runUpdate"
            >Update and restart</Button
          >
        </CardFooter>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Restart</CardTitle>
        </CardHeader>
        <CardContent class="flex flex-col gap-2 text-sm">
          <div class="text-muted-foreground">
            Running turns finish first (up to {{ drain }} s). Cut replies and goals resume after the
            start.
          </div>
          <ResultNote :note="restartNote" @close="restartNote = null" />
        </CardContent>
        <CardFooter>
          <Button variant="outline" :disabled="restarting" @click="restart"
            >Restart the gateway</Button
          >
        </CardFooter>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Backup</CardTitle>
        </CardHeader>
        <CardContent class="flex flex-col gap-3 text-sm">
          <div class="text-muted-foreground">
            A .tar.gz of state.db, memory, skills, scripts, and config.yaml. No .env; literal keys
            become ${NAME}.
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <Button variant="outline" :disabled="backingUp" @click="createBackup"
              >Create backup</Button
            >
            <span v-if="backingUp" class="text-muted-foreground">Creating the backup…</span>
          </div>
          <ResultNote :note="backupNote" @close="backupNote = null" />
          <DataTable
            v-if="rows.length"
            :columns="cols"
            :rows="rows"
            row-key="name"
            page-key="backups"
            page-label="Backups"
          >
            <template #cell-name="{ row }"
              ><span :title="String(row.name)">{{ row.name }}</span></template
            >
            <template #cell-modified="{ row }"
              ><span class="whitespace-nowrap" :title="fullTime(String(row.modified))">{{
                shortTime(String(row.modified)) || "—"
              }}</span></template
            >
            <template #cell-actions="{ row }">
              <Button
                variant="outline"
                size="sm"
                @click="download('/api/backup/' + row.name, String(row.name))"
                >Download</Button
              >
            </template>
          </DataTable>
          <StateBlock v-else state="empty" text="No backups yet." />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Restore</CardTitle>
          <CardDescription>on the host</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-col gap-3 text-sm">
          <div class="text-muted-foreground">
            Restore replaces state.db, so it runs only while the gateway is stopped.
          </div>
          <NativeSelect
            v-if="backups?.files.length"
            v-model="chosen"
            aria-label="Backup to restore"
          >
            <NativeSelectOption v-for="f in backups.files" :key="f.name" :value="f.name">{{
              f.name
            }}</NativeSelectOption>
          </NativeSelect>
          <pre class="overflow-auto rounded-md bg-muted p-3 font-mono text-xs">{{
            restoreText
          }}</pre>
          <p class="text-xs text-muted-foreground">The dashboard goes offline until step 3.</p>
          <div>
            <Button v-if="isSecure()" variant="outline" @click="copyRestore">Copy all 3</Button>
            <p v-else class="text-xs text-muted-foreground">Copy needs HTTPS or localhost.</p>
          </div>
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>Logs</CardTitle>
      </CardHeader>
      <CardContent>
        <LogViewer />
      </CardContent>
    </Card>
  </div>
</template>

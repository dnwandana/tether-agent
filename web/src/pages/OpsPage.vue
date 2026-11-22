<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api, ApiError, runCommand } from "../api/client"
import type {
  BackupFile,
  BackupView,
  ConfigView,
  ReplyView,
  TaskView,
  UpdateInfo,
} from "../api/types.gen"
import LogViewer from "../components/LogViewer.vue"
import StateBlock from "../components/StateBlock.vue"
import { status } from "../stores/status"
import { confirm, toast } from "../stores/ui"

const config = ref<ConfigView | null>(null)
const configState = ref<"loading" | "ready" | "error">("loading")
const configError = ref("")

const update = ref<UpdateInfo | null>(null)
const checking = ref(false)
const backup = ref<BackupFile | null>(null)
const backupDir = ref("")
const backingUp = ref(false)
// The restart overlay. It closes when status.stream is "open" again after a break.
const restarting = ref(false)
let broken = false
let gone = false
const backupText = ref("")
let pollTimer: ReturnType<typeof setTimeout> | null = null

function errText(e: unknown): string {
  return e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e)
}

async function loadConfig(): Promise<void> {
  try {
    config.value = await api<ConfigView>("GET", "/api/config")
    configState.value = "ready"
  } catch (e) {
    configError.value = errText(e)
    configState.value = "error"
  }
}

async function act(text: string): Promise<boolean> {
  try {
    const r = await runCommand(text)
    if (!("text" in r) || r.text === undefined)
      toast("The command runs. The reply comes later.", "info")
    else toast((r as ReplyView).text, "ok")
    return true
  } catch (e) {
    toast(errText(e), "error")
    return false
  }
}

async function checkUpdate(): Promise<void> {
  checking.value = true
  try {
    update.value = await api<UpdateInfo>("GET", "/api/update")
  } catch (e) {
    toast(errText(e), "error")
  } finally {
    checking.value = false
  }
}

function showOverlay(): void {
  broken = status.stream !== "open"
  restarting.value = true
}

async function runUpdate(): Promise<void> {
  const u = update.value
  if (!u || !u.newer || !u.writable) return
  const ok = await confirm({
    title: `Update to ${u.latest} and restart?`,
    body: "The gateway downloads the release, checks its SHA-256, runs it once, swaps the binary, and restarts. Running turns finish first.",
    confirmLabel: "Update and restart",
  })
  if (!ok) return
  try {
    const r = await api<{ task_id: string }>("POST", "/api/update", { confirm: true })
    showOverlay()
    void followUpdate(r.task_id)
  } catch (e) {
    toast(errText(e), "error")
  }
}

// Closes the restart overlay and shows the error when the update task fails.
async function followUpdate(taskId: string): Promise<void> {
  const t = await followTask(taskId)
  if (t?.state === "error") {
    restarting.value = false
    toast(t.text, "error")
  }
}

async function restart(): Promise<void> {
  const ok = await confirm({
    title: "Restart the gateway?",
    body: "Running turns finish first (up to 180 s). This page reconnects by itself.",
    confirmLabel: "Restart",
  })
  if (!ok) return
  if (await act("/restart")) showOverlay()
}

function stopPoll(): void {
  if (pollTimer !== null) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

const sleep = (ms: number): Promise<void> =>
  new Promise((r) => {
    pollTimer = setTimeout(r, ms)
  })

// Polls a task every 2 s for 90 s at most. It returns the last view, or null when the time ends.
// A failed read keeps the poll, because a restart can cut the connection.
async function followTask(taskId: string): Promise<TaskView | null> {
  for (let i = 0; i < 45; i++) {
    await sleep(2000)
    if (gone) return null
    try {
      const t = await api<TaskView>("GET", `/api/tasks/${taskId}`)
      if (t.state === "done" || t.state === "error") return t
    } catch {
      // Keep the poll. The next try can work.
    }
  }
  return null
}

async function listBackups(): Promise<BackupView | null> {
  try {
    return await api<BackupView>("GET", "/api/backup")
  } catch {
    return null
  }
}

// Starts the backup task, polls it, then reads the backup list for the new file.
async function createBackup(): Promise<void> {
  if (backingUp.value) return
  backingUp.value = true
  backup.value = null
  backupText.value = ""
  try {
    const before = new Set(((await listBackups())?.files ?? []).map((f) => f.name))
    const r = await api<{ task_id: string }>("POST", "/api/backup")
    const t = await followTask(r.task_id)
    if (!t) {
      if (!gone) toast("The backup did not finish in 90 s. Look again later.", "error")
      return
    }
    if (t.state === "error") {
      toast(t.text, "error")
      return
    }
    const v = await listBackups()
    const files = v?.files ?? []
    backup.value = files.find((f) => !before.has(f.name)) ?? files[0] ?? null
    backupDir.value = v?.dir ?? ""
    backupText.value = t.text
  } catch (e) {
    toast(errText(e), "error")
  } finally {
    backingUp.value = false
  }
}

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
    toast(errText(e), "error")
  }
}

const mb = (n: number): string => (n / (1024 * 1024)).toFixed(1)

watch(
  () => status.stream,
  (s) => {
    if (!restarting.value) return
    if (s !== "open") broken = true
    else if (broken) {
      restarting.value = false
      broken = false
    }
  },
)

onMounted(loadConfig)
onBeforeUnmount(() => {
  gone = true
  stopPoll()
})
</script>

<template>
  <div class="page">
    <div class="page-h"><h1 class="t-20">System</h1></div>
    <div class="tabs">
      <router-link :to="{ name: 'system' }" class="tab">Health</router-link>
      <router-link :to="{ name: 'ops' }" class="tab is-on">Operations</router-link>
    </div>

    <div class="card">
      <div class="card-h">
        <h3>config.yaml</h3>
        <span class="c-3 t-12">read-only · a change needs a restart</span>
      </div>
      <StateBlock v-if="configState === 'loading'" state="loading" />
      <StateBlock
        v-else-if="configState === 'error'"
        state="error"
        :text="configError"
        action="Retry"
        @retry="loadConfig"
      />
      <div v-else class="code-block">{{ config?.text }}</div>
    </div>

    <div class="grid-2">
      <div class="card">
        <div class="card-h"><h3>Update</h3></div>
        <div class="li">
          <span class="grow">tether {{ update?.current ?? "" }}</span>
          <span v-if="checking" class="c-3">checking…</span>
          <span v-else-if="!update" class="c-3">not checked yet</span>
          <span v-else-if="update.newer">
            {{ update.latest }} is available
            <a :href="update.notes_url" target="_blank" rel="noopener noreferrer">release notes</a>
          </span>
          <span v-else class="c-3">up to date</span>
        </div>
        <div v-if="update && update.newer && !update.writable" class="li c-3 t-12">
          The binary at {{ update.path }} is not writable by this user. Run `tether update` as a
          user who can write it.
        </div>
        <div class="li">
          <button class="btn btn-secondary" type="button" :disabled="checking" @click="checkUpdate">
            Check for update
          </button>
          <button
            class="btn btn-primary"
            type="button"
            :disabled="!(update && update.newer && update.writable)"
            @click="runUpdate"
          >
            Update and restart
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-h"><h3>Restart</h3></div>
        <div class="li c-2">
          Running turns finish first (up to 180 s). Cut replies and goals resume after the start.
        </div>
        <div class="li">
          <button class="btn btn-secondary" type="button" @click="restart">
            Restart the gateway
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-h"><h3>Backup</h3></div>
        <div class="li c-2">
          A .tar.gz of state.db, memory, skills, scripts, and config.yaml. No .env; literal keys
          become ${NAME}.
        </div>
        <div class="li">
          <button
            class="btn btn-secondary"
            type="button"
            :disabled="backingUp"
            @click="createBackup"
          >
            Create backup
          </button>
          <span v-if="backingUp" class="c-3">Creating the backup…</span>
        </div>
        <div v-if="backupText" class="li c-2">{{ backupText }}</div>
        <dl v-if="backup" class="kv">
          <dt>File</dt>
          <dd class="mono">{{ backup.name }}</dd>
          <dt>Folder</dt>
          <dd class="mono">{{ backupDir }}</dd>
        </dl>
        <div v-if="backup" class="li">
          <button
            class="btn btn-primary"
            type="button"
            @click="download('/api/backup/' + backup.name, backup.name)"
          >
            Download ({{ mb(backup.size) }} MB)
          </button>
        </div>
      </div>

      <div class="card">
        <div class="card-h">
          <h3>Restore</h3>
          <span class="c-3 t-12">on the host</span>
        </div>
        <div class="li c-2">
          Restore replaces state.db, so it runs only while the gateway is stopped.
        </div>
        <div class="code-block">
          systemctl --user stop tether.service tether restore ~/.tether/backups/&lt;file&gt;
          systemctl --user start tether.service
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-h"><h3>Logs</h3></div>
      <LogViewer />
    </div>

    <div
      v-if="restarting"
      class="state"
      role="status"
      style="position: fixed; inset: 0; z-index: 50; background: var(--bg, #fff)"
    >
      <span class="spin" aria-hidden="true" />
      <h4>Restarting…</h4>
    </div>
  </div>
</template>

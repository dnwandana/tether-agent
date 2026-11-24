<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, postForm, runCommand } from "../../api/client"
import type { ReplyView } from "../../api/types.gen"
import { turn } from "../../stores/turn"
import { isSecure, toast } from "../../stores/ui"
import { friendlyError } from "../../lib/errors"
import HttpsNote from "../HttpsNote.vue"
import AckChip from "./AckChip.vue"
import QueueStrip from "./QueueStrip.vue"

type Row = { name: string; description: string }
type Att = { file: File; label: string; voice: boolean }

const emit = defineEmits<{
  sent: [payload: { text: string; files: string[] }]
  failed: [payload: { text: string; files: string[] }]
  reply: [r: ReplyView | { request_id: string }]
  resize: [height: number]
}>()

const MAX_HEIGHT = 194

const route = useRoute() as ReturnType<typeof useRoute> | undefined
const router = useRouter() as ReturnType<typeof useRouter> | undefined

const draft = ref("")
// A reload of a frozen phone page keeps the draft. main.ts does not reload for a new build
// while a draft waits.
const KEY_DRAFT = "tether.draft"
const sendError = ref("")
// True only when a send failed. A voice error has nothing to send again.
const canRetry = ref(false)
const files = ref<Att[]>([])
const clientId = ref("")
const commands = ref<Row[]>([])
const skills = ref<Row[]>([])
const hi = ref(0)
const menuClosed = ref(false)
const sending = ref(false)
const recording = ref(false)
const recMs = ref(0)
const secure = isSecure()

const dock = ref<HTMLElement | null>(null)
const field = ref<HTMLTextAreaElement | null>(null)
const fileIn = ref<HTMLInputElement | null>(null)
const photoIn = ref<HTMLInputElement | null>(null)

const busy = computed(() => turn.view?.running === true)
const empty = computed(() => draft.value.trim() === "" && files.value.length === 0)
const liveAcks = computed(() => turn.acks.filter((a) => a.request_id === ""))

const matches = computed<Row[]>(() => {
  const d = draft.value
  if (menuClosed.value || !d.startsWith("/") || /\s/.test(d)) return []
  return [...commands.value, ...skills.value].filter((r) => r.name.startsWith(d)).slice(0, 8)
})
const menuOpen = computed(() => matches.value.length > 0)
watch(matches, () => {
  hi.value = 0
})
watch(draft, () => {
  menuClosed.value = false
})
watch(draft, (v) => {
  try {
    if (v === "") sessionStorage.removeItem(KEY_DRAFT)
    else sessionStorage.setItem(KEY_DRAFT, v)
  } catch {
    // Storage can be blocked. The draft then lives only in memory.
  }
})

// Sets the height of the text field from its scroll height, between 1 and 8 lines.
function grow(): void {
  const el = field.value
  if (!el) return
  el.style.height = "auto"
  el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`
}
watch(draft, () => {
  void nextTick(grow)
})

// Returns 32 hex characters from crypto.getRandomValues.
function newClientId(): string {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("")
}

function isBuiltin(text: string): boolean {
  const first = text.split(/\s/, 1)[0]
  return commands.value.some((c) => c.name === first)
}

// Sends the draft and the files: a built-in command to runCommand, all else to postForm.
async function send(): Promise<void> {
  const text = draft.value.trim()
  const atts = files.value
  if ((text === "" && atts.length === 0) || sending.value) return
  sendError.value = ""
  canRetry.value = false
  if (!clientId.value) clientId.value = newClientId()
  const id = clientId.value
  draft.value = ""
  files.value = []
  await nextTick(grow)
  const payload = { text, files: atts.map((a) => a.file.name) }
  emit("sent", payload)
  sending.value = true
  try {
    if (atts.length === 0 && isBuiltin(text)) {
      emit("reply", await runCommand(text))
    } else {
      // The server reads client_id only as the first part, so a retry finds the first upload.
      const form = new FormData()
      form.append("client_id", id)
      form.append("text", text)
      // A voice note goes in the voice part, so the server converts it and writes the text.
      for (const a of atts) form.append(a.voice ? "voice" : "files", a.file, a.file.name)
      await postForm("/api/messages", form)
    }
    clientId.value = ""
  } catch (e) {
    draft.value = text
    files.value = atts
    emit("failed", payload)
    canRetry.value = true
    sendError.value = friendlyError(
      e,
      atts.length === 0 && isBuiltin(text) ? "/api/command" : "/api/messages",
    )
  } finally {
    sending.value = false
  }
}

function pick(name: string): void {
  draft.value = `${name} `
  menuClosed.value = true
  void nextTick(() => field.value?.focus())
}

function onKey(e: KeyboardEvent): void {
  if (menuOpen.value) {
    const n = matches.value.length
    if (e.key === "ArrowDown") {
      e.preventDefault()
      hi.value = (hi.value + 1) % n
      return
    }
    if (e.key === "ArrowUp") {
      e.preventDefault()
      hi.value = (hi.value + n - 1) % n
      return
    }
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault()
      pick(matches.value[hi.value].name)
      return
    }
    if (e.key === "Escape") {
      e.preventDefault()
      menuClosed.value = true
      return
    }
  }
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    void send()
  }
}

function addFiles(e: Event, photo: boolean): void {
  const input = e.target as HTMLInputElement
  for (const f of Array.from(input.files ?? [])) {
    files.value.push({
      file: f,
      label: `${photo || f.type.startsWith("image/") ? "🖼" : "📎"} ${f.name}`,
      voice: false,
    })
  }
  try {
    input.value = ""
  } catch {
    /* jsdom may refuse */
  }
}

function removeFile(i: number): void {
  files.value.splice(i, 1)
}

async function stopTask(): Promise<void> {
  try {
    emit("reply", await runCommand("/stop"))
  } catch (e) {
    toast(friendlyError(e, "/api/command"), "error")
  }
}

async function runAck(data: string): Promise<void> {
  try {
    emit("reply", await runCommand(data))
  } catch (e) {
    toast(friendlyError(e, "/api/command"), "error")
  }
}

// Voice notes.
let recorder: MediaRecorder | null = null
let stream: MediaStream | null = null
let chunks: Blob[] = []
let timer: ReturnType<typeof setInterval> | undefined
let t0 = 0
let released = false

function pickType(): string {
  for (const t of ["audio/webm;codecs=opus", "audio/mp4"]) {
    if (typeof MediaRecorder !== "undefined" && MediaRecorder.isTypeSupported(t)) return t
  }
  return ""
}

// Starts a voice note while the record button is held.
async function startRecord(e: PointerEvent): Promise<void> {
  e.preventDefault()
  if (recording.value) return
  released = false
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    if (released) {
      stream.getTracks().forEach((t) => t.stop())
      stream = null
      return
    }
    const type = pickType()
    recorder = type ? new MediaRecorder(stream, { mimeType: type }) : new MediaRecorder(stream)
    chunks = []
    recorder.ondataavailable = (ev) => {
      if (ev.data.size > 0) chunks.push(ev.data)
    }
    const rec = recorder
    rec.onstop = () => {
      const mime = rec.mimeType || type || "audio/webm"
      const ext = mime.includes("mp4") ? "m4a" : mime.includes("ogg") ? "ogg" : "webm"
      const file = new File(chunks, `voice-${Date.now()}.${ext}`, { type: mime })
      stream?.getTracks().forEach((t) => t.stop())
      stream = null
      recorder = null
      files.value = [...files.value, { file, label: `🎤 ${file.name}`, voice: true }]
      void send()
    }
    rec.start()
    recording.value = true
    t0 = Date.now()
    recMs.value = 0
    timer = setInterval(() => {
      recMs.value = Date.now() - t0
    }, 100)
    window.addEventListener("pointerup", stopRecord)
    window.addEventListener("pointercancel", stopRecord)
  } catch (err) {
    stream?.getTracks().forEach((t) => t.stop())
    stream = null
    recorder = null
    canRetry.value = false
    sendError.value = friendlyError(err)
  }
}

// Ends the voice note and sends it.
function stopRecord(): void {
  released = true
  window.removeEventListener("pointerup", stopRecord)
  window.removeEventListener("pointercancel", stopRecord)
  clearInterval(timer)
  recording.value = false
  if (recorder && recorder.state !== "inactive") recorder.stop()
}

const hint = computed(() => {
  if (!recording.value) return ""
  return recMs.value < 200
    ? "● Recording… release to send"
    : `● ${(recMs.value / 1000).toFixed(1)} s · release to send`
})

function takeDraft(): void {
  const d = route?.query?.draft
  const v = Array.isArray(d) ? d[0] : d
  if (!v) return
  draft.value = v
  void router?.replace({ query: {} })
  void nextTick(() => {
    field.value?.focus()
    grow()
  })
}
watch(() => route?.query?.draft, takeDraft)

let ro: ResizeObserver | undefined
onMounted(async () => {
  if (typeof ResizeObserver !== "undefined" && dock.value) {
    ro = new ResizeObserver((entries) => {
      const en = entries[0]
      if (!en) return
      const h = en.borderBoxSize?.[0]?.blockSize ?? en.contentRect.height
      dock.value?.parentElement?.style.setProperty("--dock-h", `${h}px`)
      emit("resize", h)
    })
    ro.observe(dock.value)
  }
  if (draft.value === "" && !route?.query?.draft) {
    try {
      draft.value = sessionStorage.getItem(KEY_DRAFT) ?? ""
    } catch {
      // Storage can be blocked.
    }
  }
  takeDraft()
  try {
    const b = await api<{ commands: Row[]; skills: Row[] }>("GET", "/api/bootstrap")
    commands.value = b.commands ?? []
    skills.value = b.skills ?? []
  } catch {
    commands.value = []
    skills.value = []
  }
})
onBeforeUnmount(() => {
  ro?.disconnect()
  clearInterval(timer)
  window.removeEventListener("pointerup", stopRecord)
  window.removeEventListener("pointercancel", stopRecord)
  stream?.getTracks().forEach((t) => t.stop())
})
</script>

<template>
  <div ref="dock" class="dock">
    <div class="dock-in">
      <div class="live-acks">
        <AckChip v-for="a in liveAcks" :key="a.id" :ack="a" @action="runAck" />
      </div>
      <QueueStrip :items="turn.queue" @now="runAck('/now')" />
      <div v-if="sendError" class="composer-err c-bad t-13" role="alert">
        <span>{{ sendError }}</span>
        <button v-if="canRetry" type="button" class="btn btn-secondary btn-sm" @click="send">
          Send again
        </button>
      </div>
      <div class="composer2">
        <div v-if="menuOpen" class="menu slash-pop" role="listbox">
          <button
            v-for="(r, i) in matches"
            :key="r.name"
            type="button"
            class="menu-item"
            :class="{ 'is-hi': i === hi }"
            @mousedown.prevent="pick(r.name)"
          >
            <span class="mono">{{ r.name }}</span
            ><span class="r">{{ r.description }}</span>
          </button>
        </div>
        <div v-if="files.length > 0" class="attach-row">
          <span v-for="(a, i) in files" :key="i" class="att">
            {{ a.label }}
            <button
              type="button"
              class="icon-btn"
              :aria-label="`Remove ${a.file.name}`"
              @click="removeFile(i)"
            >
              ✕
            </button>
          </span>
        </div>
        <textarea
          ref="field"
          v-model="draft"
          rows="1"
          placeholder="Message Tether…  / for commands"
          aria-label="Message"
          @keydown="onKey"
          @input="grow"
        ></textarea>
        <div class="cbar">
          <button
            type="button"
            class="icon-btn"
            title="Attach files"
            aria-label="Attach files"
            @click="fileIn?.click()"
          >
            <svg
              viewBox="0 0 24 24"
              width="20"
              height="20"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path
                d="M20 11l-8.5 8.5a5 5 0 0 1-7-7L13 4a3.5 3.5 0 0 1 5 5l-8.5 8.5a2 2 0 0 1-3-3L14 7"
              />
            </svg>
          </button>
          <button
            type="button"
            class="icon-btn"
            title="Take or attach a photo"
            aria-label="Photo"
            @click="photoIn?.click()"
          >
            <svg
              viewBox="0 0 24 24"
              width="20"
              height="20"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M4 8h3l2-3h6l2 3h3v11H4zM12 10a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7z" />
            </svg>
          </button>
          <button
            v-if="secure"
            type="button"
            class="icon-btn"
            title="Hold to record a voice note"
            aria-label="Hold to record"
            @pointerdown="startRecord"
          >
            <svg
              viewBox="0 0 24 24"
              width="20"
              height="20"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path
                d="M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3zM5 11a7 7 0 0 0 14 0M12 18v3"
              />
            </svg>
          </button>
          <span class="rec-hint">{{ hint }}</span>
          <span class="send-slot">
            <button
              v-if="busy && empty"
              type="button"
              class="round-btn is-stop"
              aria-label="Stop the task"
              @click="stopTask"
            >
              <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                <path d="M7 7h10v10H7z" />
              </svg>
            </button>
            <button
              v-else
              type="button"
              class="round-btn"
              aria-label="Send"
              :disabled="empty"
              @click="send"
            >
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <path d="M12 19V5M5.5 11.5L12 5l6.5 6.5" />
              </svg>
            </button>
          </span>
        </div>
      </div>
      <HttpsNote v-if="!secure" />
      <input ref="fileIn" type="file" multiple hidden @change="addFiles($event, false)" />
      <input
        ref="photoIn"
        type="file"
        accept="image/*"
        capture="environment"
        hidden
        @change="addFiles($event, true)"
      />
    </div>
  </div>
</template>

<style scoped>
.composer-err {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 0 4px 8px;
}
</style>

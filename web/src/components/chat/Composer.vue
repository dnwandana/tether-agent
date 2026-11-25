<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type Component } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, postForm, runCommand } from "../../api/client"
import type { ReplyView } from "../../api/types.gen"
import { turn } from "../../stores/turn"
import { isSecure, toast } from "../../stores/ui"
import { friendlyError } from "../../lib/errors"
import {
  ArrowUpIcon,
  CameraIcon,
  ImageIcon,
  MicIcon,
  PaperclipIcon,
  SquareIcon,
  XIcon,
} from "@lucide/vue"
import { Button } from "@/components/ui/button"
import {
  Attachment,
  AttachmentAction,
  AttachmentActions,
  AttachmentContent,
  AttachmentGroup,
  AttachmentTitle,
} from "@/components/ui/attachment"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/components/ui/input-group"
import { cn } from "@/lib/utils"
import HttpsNote from "../HttpsNote.vue"
import AckChip from "./AckChip.vue"
import QueueStrip from "./QueueStrip.vue"

type Row = { name: string; description: string }
type Att = { file: File; kind: "image" | "file" | "voice" }
// Gives the chip icon of each file kind.
const ATT_ICON: Record<Att["kind"], Component> = {
  image: ImageIcon,
  file: PaperclipIcon,
  voice: MicIcon,
}

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
// The files of a running send. The chips stay visible while the request runs.
const inflight = ref<Att[]>([])
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
const field = ref<InstanceType<typeof InputGroupTextarea> | null>(null)
// The root of the copied textarea component is the <textarea> element.
function fieldEl(): HTMLTextAreaElement | null {
  return (field.value?.$el as HTMLTextAreaElement | undefined) ?? null
}
const fileIn = ref<HTMLInputElement | null>(null)
const photoIn = ref<HTMLInputElement | null>(null)

const busy = computed(() => turn.view?.running === true)
const empty = computed(() => draft.value.trim() === "" && files.value.length === 0)
const chips = computed(() => (sending.value ? inflight.value : files.value))
const chipState = computed<"uploading" | "error" | "idle">(() =>
  sending.value ? "uploading" : canRetry.value ? "error" : "idle",
)
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
  const el = fieldEl()
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
  inflight.value = atts
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
      for (const a of atts) form.append(a.kind === "voice" ? "voice" : "files", a.file, a.file.name)
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
    inflight.value = []
    sending.value = false
  }
}

function pick(name: string): void {
  draft.value = `${name} `
  menuClosed.value = true
  void nextTick(() => fieldEl()?.focus())
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
      kind: photo || f.type.startsWith("image/") ? "image" : "file",
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
      files.value = [...files.value, { file, kind: "voice" }]
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
    ? "Recording… release to send"
    : `${(recMs.value / 1000).toFixed(1)} s · release to send`
})

function takeDraft(): void {
  const d = route?.query?.draft
  const v = Array.isArray(d) ? d[0] : d
  if (!v) return
  draft.value = v
  void router?.replace({ query: {} })
  void nextTick(() => {
    fieldEl()?.focus()
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
  <div
    ref="dock"
    class="pointer-events-none absolute inset-x-0 bottom-0 bg-gradient-to-t from-background from-70% to-transparent px-3 pt-6 pb-3"
  >
    <div class="pointer-events-auto mx-auto flex max-w-[760px] flex-col gap-2">
      <div class="flex flex-col gap-1">
        <AckChip v-for="a in liveAcks" :key="a.id" :ack="a" @action="runAck" />
      </div>
      <QueueStrip :items="turn.queue" @now="runAck('/now')" />
      <div
        v-if="sendError"
        role="alert"
        class="flex flex-wrap items-center gap-2 px-1 text-sm text-destructive"
      >
        <span>{{ sendError }}</span>
        <Button v-if="canRetry" type="button" variant="secondary" size="sm" @click="send">
          Send again
        </Button>
      </div>
      <div class="relative">
        <div
          v-if="menuOpen"
          role="listbox"
          aria-label="Commands"
          class="absolute inset-x-0 bottom-full mb-2 rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        >
          <div
            v-for="(r, i) in matches"
            :key="r.name"
            role="option"
            :aria-selected="i === hi"
            :class="
              cn(
                'flex cursor-pointer items-baseline gap-3 rounded-sm px-2 py-1.5 text-sm',
                i === hi && 'bg-accent text-accent-foreground',
              )
            "
            @mousedown.prevent="pick(r.name)"
          >
            <span class="font-mono">{{ r.name }}</span>
            <span class="truncate text-muted-foreground">{{ r.description }}</span>
          </div>
        </div>
        <InputGroup class="bg-background">
          <InputGroupAddon v-if="chips.length > 0" align="block-start">
            <AttachmentGroup>
              <Attachment v-for="(a, i) in chips" :key="i" size="sm" :state="chipState">
                <AttachmentContent>
                  <AttachmentTitle class="flex items-center gap-1"
                    ><component :is="ATT_ICON[a.kind]" class="size-3.5 shrink-0" /><span>{{
                      a.file.name
                    }}</span></AttachmentTitle
                  >
                </AttachmentContent>
                <AttachmentActions>
                  <AttachmentAction
                    type="button"
                    :aria-label="`Remove ${a.file.name}`"
                    :disabled="sending"
                    @click="removeFile(i)"
                  >
                    <XIcon />
                  </AttachmentAction>
                </AttachmentActions>
              </Attachment>
            </AttachmentGroup>
          </InputGroupAddon>
          <InputGroupTextarea
            ref="field"
            v-model="draft"
            rows="1"
            class="min-h-0 field-sizing-fixed max-tab:min-h-11 max-tab:text-base"
            placeholder="Message Tether…  / for commands"
            aria-label="Message"
            @keydown="onKey"
            @input="grow"
          />
          <InputGroupAddon align="block-end">
            <InputGroupButton
              type="button"
              size="icon-sm"
              class="max-tab:size-11"
              title="Attach files"
              aria-label="Attach files"
              @click="fileIn?.click()"
            >
              <PaperclipIcon />
            </InputGroupButton>
            <InputGroupButton
              type="button"
              size="icon-sm"
              class="max-tab:size-11"
              title="Take or attach a photo"
              aria-label="Photo"
              @click="photoIn?.click()"
            >
              <CameraIcon />
            </InputGroupButton>
            <InputGroupButton
              v-if="secure"
              type="button"
              size="icon-sm"
              class="max-tab:size-11"
              title="Hold to record a voice note"
              aria-label="Hold to record"
              @pointerdown="startRecord"
            >
              <MicIcon />
            </InputGroupButton>
            <span v-if="hint" class="flex items-center gap-1 text-xs text-destructive">
              <span
                data-rec-dot
                aria-hidden="true"
                class="size-2 shrink-0 rounded-full bg-destructive"
              />
              <span>{{ hint }}</span>
            </span>
            <InputGroupButton
              v-if="busy && empty"
              type="button"
              size="icon-sm"
              variant="destructive"
              class="ml-auto rounded-full max-tab:size-11"
              aria-label="Stop the task"
              @click="stopTask"
            >
              <SquareIcon />
            </InputGroupButton>
            <InputGroupButton
              v-else
              type="button"
              size="icon-sm"
              variant="default"
              class="ml-auto rounded-full max-tab:size-11"
              aria-label="Send"
              :disabled="empty"
              @click="send"
            >
              <ArrowUpIcon />
            </InputGroupButton>
          </InputGroupAddon>
        </InputGroup>
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

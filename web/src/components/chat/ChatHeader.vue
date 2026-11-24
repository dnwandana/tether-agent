<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue"
import { useRouter } from "vue-router"
import { useCommand } from "../../lib/command"
import { session } from "../../stores/session"
import { toast } from "../../stores/ui"
import { renderMarkdown } from "../../lib/markdown"
import SidePanel from "../SidePanel.vue"

const emit = defineEmits<{ "toggle-details": [] }>()
const router = useRouter()

const editing = ref(false)
const draftTitle = ref("")
const titleInput = ref<HTMLInputElement | null>(null)
const pop = ref<"menu" | null>(null)
const promptOpen = ref(false)
const promptText = ref("")
const chatH = ref<HTMLElement | null>(null)
const popLeft = ref(16)

// An action runs as a typed command. The reply popover and the confirm dialog come from useCommand.
const { run, reply, closeReply } = useCommand()
// The menu closes when a reply popover opens.
watch(reply, (r) => {
  if (r) pop.value = null
})

const modelLabel = computed(() =>
  session.entry ? `${session.model} (${session.entry})` : session.model,
)

function closePop(): void {
  pop.value = null
  closeReply()
}

// Puts the reply popover under the chip that the user clicked. The popover stays inside
// the header: 8 px from each side, and at most 420 px wide.
function chipRun(e: MouseEvent, text: string): void {
  const box = chatH.value?.getBoundingClientRect()
  const chip = (e.currentTarget as HTMLElement | null)?.getBoundingClientRect()
  if (box && chip) {
    const width = Math.min(420, box.width - 16)
    popLeft.value = Math.max(8, Math.min(chip.left - box.left, box.width - width - 8))
  }
  void run(text)
}

function html(src: string): string {
  return renderMarkdown(src)
}

function startRename(): void {
  draftTitle.value = session.title
  editing.value = true
  void nextTick(() => titleInput.value?.focus())
}
function commitRename(): void {
  if (!editing.value) return
  editing.value = false
  const t = draftTitle.value.trim()
  if (t !== "" && t !== session.title) void run("/rename " + t)
}
function cancelRename(): void {
  editing.value = false
}

function togglePop(kind: "menu"): void {
  closeReply()
  pop.value = pop.value === kind ? null : kind
}

function openGoal(): void {
  void router.push({ name: "goal", params: { session: session.id } })
}

function menuAct(text: string): void {
  pop.value = null
  void run(text)
}

function openPrompt(): void {
  pop.value = null
  promptText.value = ""
  promptOpen.value = true
}
function savePrompt(): void {
  const t = promptText.value.trim()
  if (t === "") return
  promptOpen.value = false
  void run("/prompt set " + t)
}
function clearPrompt(): void {
  promptOpen.value = false
  void run("/prompt clear")
}

// Downloads a file from an API route. A plain link cannot send the session header.
async function download(path: string, name: string): Promise<void> {
  try {
    const r = await fetch(path, {
      headers: { "X-Tether-Session": localStorage.getItem("tether.session") ?? "" },
    })
    if (!r.ok) {
      toast(`The download failed. The status is ${r.status}.`, "error")
      return
    }
    const url = URL.createObjectURL(await r.blob())
    const a = document.createElement("a")
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // The browser needs the URL for a while after the click to start the download.
    setTimeout(() => URL.revokeObjectURL(url), 30_000)
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}
function exportSession(): void {
  pop.value = null
  void download(
    "/api/sessions/" + encodeURIComponent(session.id) + "/export",
    `session-${session.id}.jsonl`,
  )
}

function onDocClick(e: MouseEvent): void {
  if (pop.value === null && !reply.value) return
  const t = e.target as Element | null
  if (t?.closest?.("[data-pop]")) return
  closePop()
}
function onKey(e: KeyboardEvent): void {
  if (e.key !== "Escape" || (pop.value === null && !reply.value)) return
  e.preventDefault()
  closePop()
}
onMounted(() => {
  document.addEventListener("click", onDocClick)
  window.addEventListener("keydown", onKey)
})
onUnmounted(() => {
  document.removeEventListener("click", onDocClick)
  window.removeEventListener("keydown", onKey)
})
</script>

<template>
  <div ref="chatH" class="chat-h" style="position: relative">
    <input
      v-if="editing"
      ref="titleInput"
      v-model="draftTitle"
      class="input chat-title-input"
      aria-label="Session title"
      @keydown.enter.prevent="commitRename"
      @keydown.esc.prevent="cancelRename"
      @blur="commitRename"
    />
    <span v-else-if="session.id === ''" class="chat-title truncate c-3">New session</span>
    <button
      v-else
      type="button"
      class="chat-title truncate"
      :class="{ 'c-3': !session.title }"
      title="Rename the session"
      @click="startRename"
    >
      {{ session.title || "New session" }}
    </button>
    <div class="chips">
      <button type="button" class="chip" @click="chipRun($event, '/model')">
        model {{ modelLabel }} ▾
      </button>
      <button type="button" class="chip" @click="chipRun($event, '/effort')">
        effort {{ session.effort || "default" }} ▾
      </button>
      <span v-if="session.context" class="ctx">
        ~{{ (session.context.tokens / 1000).toFixed(1) }}k /
        {{ Math.round(session.context.window / 1000) }}k
        <span
          class="meter"
          :class="{
            'meter-warn':
              session.context.tokens / session.context.window > session.context.threshold,
          }"
          :style="{ '--mark': session.context.threshold * 100 + '%' }"
        >
          <span
            :style="{
              width: Math.min(100, (session.context.tokens / session.context.window) * 100) + '%',
            }"
          />
        </span>
        <button type="button" class="btn btn-ghost btn-sm hide-phone" @click="run('/compact')">
          Compact
        </button>
      </span>
      <button v-if="session.goalLine !== ''" type="button" class="chip chip-acc" @click="openGoal">
        {{ session.goalLine }}
      </button>
    </div>
    <div class="chat-h-end">
      <a
        class="help-link"
        href="https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages"
        target="_blank"
        rel="noreferrer"
        title="docs/wiki/Dashboard.md#pages"
        aria-label="Help for the pages: docs/wiki/Dashboard.md#pages"
        >?</a
      >
      <button
        type="button"
        class="btn btn-ghost btn-icon"
        aria-label="Session menu"
        data-pop
        @click="togglePop('menu')"
      >
        ⋯
      </button>
      <button
        type="button"
        class="btn btn-ghost btn-sm aside-toggle hide-phone"
        @click="emit('toggle-details')"
      >
        Details
      </button>
    </div>

    <div
      v-if="pop === 'menu'"
      class="menu"
      data-pop
      style="position: absolute; right: 16px; top: 100%; z-index: 55"
    >
      <template v-if="session.id !== ''">
        <button type="button" class="menu-item" @click="menuAct('/fork')">Fork</button>
        <button type="button" class="menu-item" @click="menuAct('/archive')">Archive</button>
        <button type="button" class="menu-item" @click="exportSession">Export JSONL</button>
      </template>
      <button type="button" class="menu-item" @click="openPrompt">Custom prompt…</button>
      <button
        v-if="session.context"
        type="button"
        class="menu-item only-phone"
        @click="menuAct('/compact')"
      >
        Compact
      </button>
      <template v-if="session.id !== ''">
        <div class="menu-sep" />
        <button type="button" class="menu-item danger" @click="menuAct('/delete ' + session.id)">
          Delete…
        </button>
      </template>
    </div>
    <div
      v-if="reply"
      class="popover"
      data-pop
      :style="{ left: popLeft + 'px' }"
      style="
        position: absolute;
        top: 100%;
        z-index: 55;
        max-width: min(420px, calc(100% - 16px));
        padding: 12px;
        display: flex;
        flex-direction: column;
        gap: 8px;
      "
    >
      <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown output only -->
      <div class="answer" v-html="html(reply.text)" />
      <div
        v-for="(row, i) in reply.buttons"
        :key="i"
        style="display: flex; flex-wrap: wrap; gap: 6px"
      >
        <button
          v-for="(b, j) in row"
          :key="j"
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="b.data === '·'"
          @click="run(b.data)"
        >
          {{ b.text }}
        </button>
      </div>
    </div>

    <SidePanel :open="promptOpen" title="Custom prompt of this session" @close="promptOpen = false">
      <div style="display: flex; flex-direction: column; gap: 12px">
        <textarea v-model="promptText" class="input" rows="8" aria-label="Custom prompt" />
        <div style="display: flex; gap: 8px; flex-wrap: wrap">
          <button type="button" class="btn btn-secondary btn-sm" @click="clearPrompt">
            🧹 Use the base prompt
          </button>
          <span class="grow" />
          <button type="button" class="btn btn-primary btn-sm" @click="savePrompt">Save</button>
        </div>
      </div>
    </SidePanel>
  </div>
</template>

<style scoped>
.ctx .meter {
  position: relative;
  overflow: visible;
}
.ctx .meter::after {
  content: "";
  position: absolute;
  left: var(--mark, 50%);
  top: -3px;
  bottom: -3px;
  width: 1px;
  background: var(--warning);
}
</style>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue"
import type { ToolView } from "../api/types.gen"
import { session, loadMessages } from "../stores/session"
import { turn } from "../stores/turn"
import { cards } from "../stores/cards"
import { status } from "../stores/status"
import { notices } from "../stores/notices"
import { renderMarkdown } from "../lib/markdown"
import { useCommand } from "../lib/command"
import MessageList from "../components/chat/MessageList.vue"
import ReasoningBlock from "../components/chat/ReasoningBlock.vue"
import CostFooter from "../components/chat/CostFooter.vue"
import PlanCard from "../components/chat/PlanCard.vue"
import Composer from "../components/chat/Composer.vue"
import ChatHeader from "../components/chat/ChatHeader.vue"
import TurnDrawer from "../components/chat/TurnDrawer.vue"
import ApprovalCard from "../components/cards/ApprovalCard.vue"
import ClarifyCard from "../components/cards/ClarifyCard.vue"
import SecretCard from "../components/cards/SecretCard.vue"
import RecordLine from "../components/cards/RecordLine.vue"

const MODES: Record<string, string> = {
  "read-only": "👁 Read-only",
  guarded: "🛡 Guarded",
  full: "⚡ Full",
}
const SUGGESTED = ["/help", "/usage today", "/model", "/jobs"]

// A sent message shows here until the server has saved it. A message that waits in the queue
// shows in the queue strip instead.
const pending = ref<{ key: number; text: string; after: number }[]>([])
let pendingKey = 0
const shownPending = computed(() =>
  pending.value.filter((p) => !session.messages.some((m) => m.role === "user" && m.id > p.after)),
)
function pendingText(p: { text: string; files: string[] }): string {
  return p.text || p.files.map((f) => "📎 " + f).join("\n")
}
function onSent(p: { text: string; files: string[] }): void {
  if (p.text.startsWith("/") || turn.view?.running) return
  const text = pendingText(p)
  if (text === "") return
  pending.value = [...shownPending.value, { key: ++pendingKey, text, after: session.lastId }]
}
// Removes the newest bubble of a send that failed. The retry adds a new bubble.
function onFailed(p: { text: string; files: string[] }): void {
  const text = pendingText(p)
  const i = pending.value.map((x) => x.text).lastIndexOf(text)
  if (i >= 0) pending.value = pending.value.filter((_, j) => j !== i)
}

const selected = ref<ToolView | null>(null)
const drawerOpen = ref(false)

const empty = computed(() => session.messages.length === 0 && !turn.view)
const modeLabel = computed(() => MODES[status.mode] ?? status.mode)
const chatCards = computed(() => cards.open.filter((c) => c.kind !== "signin"))
// The records of the session on the screen, oldest first. A clarify or a secret card has no
// session, and it comes only from a dashboard turn, so it belongs to this chat.
const chatRecords = computed(() =>
  [...cards.answered]
    .reverse()
    .filter((r) =>
      r.session_id !== ""
        ? r.session_id === session.id
        : r.kind === "clarify" || r.kind === "secret",
    ),
)
// The live copy of the selected tool, from the running turn when it is there.
const drawerTool = computed<ToolView | null>(() => {
  const s = selected.value
  if (!s) return null
  return turn.view?.tools.find((t) => t.call === s.call) ?? s
})
// The id of the last answer. Only this answer shows the cost of the finished turn.
const lastAnswerId = computed(() => {
  for (let i = session.messages.length - 1; i >= 0; i--) {
    if (session.messages[i].role === "assistant") return session.messages[i].id
  }
  return -1
})

// Opens the drawer on a tool line. A second tap on the selected line closes it.
function onToolSelect(t: ToolView): void {
  if (drawerOpen.value && selected.value?.call === t.call) {
    drawerOpen.value = false
    return
  }
  selected.value = t
  drawerOpen.value = true
}
function closeDrawer(): void {
  drawerOpen.value = false
  selected.value = null
}
function toggleDetails(): void {
  drawerOpen.value = !drawerOpen.value
}

// Runs a suggested command as a typed command. A reply with buttons shows as a popover.
const { run, reply, closeReply } = useCommand()
function onDocClick(e: MouseEvent): void {
  if (!reply.value) return
  if ((e.target as Element | null)?.closest?.("[data-pop]")) return
  closeReply()
}

onMounted(() => {
  document.addEventListener("click", onDocClick)
  if (session.messages.length === 0)
    loadMessages().catch((e) => console.warn("messages load failed", e))
})
onUnmounted(() => document.removeEventListener("click", onDocClick))
</script>

<template>
  <div class="chat-wrap">
    <div class="chat-col">
      <h1 class="sr">Chat</h1>
      <ChatHeader @toggle-details="toggleDetails" />
      <div class="chat-scroll">
        <div v-if="empty" class="empty-chat">
          <h2>New session</h2>
          <p class="t-13 c-3">
            {{ modeLabel }} · Dashboard · {{ session.model }}. Ask anything, or try one of these.
          </p>
          <div class="sugg">
            <button
              v-for="c in SUGGESTED"
              :key="c"
              type="button"
              class="chip chip-touch"
              data-pop
              @click="run(c)"
            >
              {{ c }}
            </button>
          </div>
          <div v-if="reply" class="popover reply-pop" data-pop>
            <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
            <div class="answer" v-html="renderMarkdown(reply.text)" />
            <div v-for="(row, i) in reply.buttons" :key="i" class="reply-row">
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
        </div>
        <MessageList v-else :selected="selected?.call" @tool="onToolSelect">
          <template #reasoning="{ message }">
            <ReasoningBlock
              v-if="message.reasoning"
              :text="message.reasoning"
              :tokens="message.reasoning_tokens"
            />
          </template>
          <template #footer="{ message }">
            <CostFooter
              v-if="turn.done && message.id === lastAnswerId && !turn.view?.running"
              :cost="turn.done.cost"
            />
          </template>
        </MessageList>
        <div v-if="notices.list.length > 0 || shownPending.length > 0" class="transcript tail">
          <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
          <div v-for="(n, i) in notices.list" :key="'n' + i" class="notice">
            <span class="n md-inline" v-html="renderMarkdown(n.text)" />
          </div>
          <div v-for="p in shownPending" :key="p.key" class="msg msg-user is-pending">
            <div class="bubble-user" style="white-space: pre-wrap">{{ p.text }}</div>
          </div>
        </div>
        <div
          v-if="
            !empty &&
            ((turn.view?.plan.length ?? 0) > 0 || chatCards.length > 0 || chatRecords.length > 0)
          "
          class="transcript tail"
        >
          <PlanCard v-if="turn.view && turn.view.plan.length > 0" :items="turn.view.plan" />
          <RecordLine v-for="r in chatRecords" :key="'r' + r.id" :record="r" />
          <template v-for="c in chatCards" :key="c.id">
            <ApprovalCard v-if="c.kind === 'approval' || c.kind === 'pin'" :card="c" />
            <ClarifyCard v-else-if="c.kind === 'clarify'" :card="c" />
            <SecretCard v-else-if="c.kind === 'secret'" :card="c" />
          </template>
        </div>
      </div>
      <Composer @sent="onSent" @failed="onFailed" />
    </div>
    <TurnDrawer :open="drawerOpen" :tool="drawerTool" @close="closeDrawer" />
  </div>
</template>

<style scoped>
.chat-scroll > .transcript:has(+ .tail) {
  padding-bottom: 0;
}
.chat-scroll > .tail {
  flex: none;
  padding-top: var(--sp-5);
}
.is-pending {
  opacity: 0.6;
}
.reply-pop {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  max-width: min(420px, 100%);
  margin: 12px auto 0;
}
.reply-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.md-inline :deep(p) {
  margin: 0;
}
</style>

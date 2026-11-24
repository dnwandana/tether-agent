<script setup lang="ts">
import { computed, onMounted, ref } from "vue"
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
import {
  MessageScrollerProvider,
  MessageScroller,
  MessageScrollerViewport,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerButton,
} from "@/components/ui/message-scroller"
import { Message } from "@/components/ui/message"
import { Bubble, BubbleContent } from "@/components/ui/bubble"
import { Marker, MarkerContent } from "@/components/ui/marker"
import { Button } from "@/components/ui/button"
import { Popover, PopoverAnchor, PopoverContent } from "@/components/ui/popover"

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
// The popover closes itself on Esc and on an outside click. This clears the reply.
function onReplyOpen(open: boolean): void {
  if (!open) closeReply()
}

onMounted(() => {
  if (session.messages.length === 0)
    loadMessages().catch((e) => console.warn("messages load failed", e))
})
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1">
    <div class="relative flex min-h-0 min-w-0 flex-1 flex-col">
      <h1 class="sr-only">Chat</h1>
      <ChatHeader @toggle-details="toggleDetails" />
      <MessageScrollerProvider auto-scroll>
        <MessageScroller class="min-h-0 flex-1">
          <MessageScrollerViewport>
            <MessageScrollerContent
              class="mx-auto w-full max-w-[760px] px-4 pt-6 pb-[calc(var(--dock-h,96px)+24px)]"
            >
              <MessageScrollerItem v-if="empty" message-id="empty">
                <div class="flex flex-col items-center gap-3 py-10 text-center">
                  <h2 class="text-xl font-semibold tracking-tight">New session</h2>
                  <p class="text-sm text-muted-foreground">
                    {{ modeLabel }} · Dashboard · {{ session.model }}. Ask anything, or try one of
                    these.
                  </p>
                  <Popover :open="reply !== null" @update:open="onReplyOpen">
                    <PopoverAnchor as-child>
                      <div
                        class="flex scroll-fade-x scrollbar-none max-w-full gap-2 overflow-x-auto overscroll-x-contain"
                      >
                        <Button
                          v-for="c in SUGGESTED"
                          :key="c"
                          type="button"
                          variant="outline"
                          size="sm"
                          class="shrink-0"
                          @click="run(c)"
                        >
                          {{ c }}
                        </Button>
                      </div>
                    </PopoverAnchor>
                    <PopoverContent class="flex w-[min(420px,calc(100vw-32px))] flex-col gap-2">
                      <template v-if="reply">
                        <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
                        <div class="typeset typeset-chat" v-html="renderMarkdown(reply.text)" />
                        <div
                          v-for="(row, i) in reply.buttons"
                          :key="i"
                          class="flex flex-wrap gap-1.5"
                        >
                          <Button
                            v-for="(b, j) in row"
                            :key="j"
                            type="button"
                            variant="secondary"
                            size="sm"
                            :disabled="b.data === '·'"
                            @click="run(b.data)"
                          >
                            {{ b.text }}
                          </Button>
                        </div>
                      </template>
                    </PopoverContent>
                  </Popover>
                </div>
              </MessageScrollerItem>
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
              <MessageScrollerItem
                v-for="(n, i) in notices.list"
                :key="'notice-' + i"
                :message-id="'notice-' + i"
              >
                <Marker>
                  <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
                  <MarkerContent class="[&_p]:m-0" v-html="renderMarkdown(n.text)" />
                </Marker>
              </MessageScrollerItem>
              <MessageScrollerItem
                v-for="p in shownPending"
                :key="'pending-' + p.key"
                :message-id="'pending-' + p.key"
                scroll-anchor
              >
                <Message align="end" class="opacity-60">
                  <Bubble variant="muted" align="end">
                    <BubbleContent class="whitespace-pre-wrap">{{ p.text }}</BubbleContent>
                  </Bubble>
                </Message>
              </MessageScrollerItem>
              <MessageScrollerItem
                v-if="!empty && turn.view && turn.view.plan.length > 0"
                message-id="plan"
              >
                <PlanCard :items="turn.view.plan" />
              </MessageScrollerItem>
              <template v-if="!empty">
                <MessageScrollerItem
                  v-for="r in chatRecords"
                  :key="'rec-' + r.id"
                  :message-id="'rec-' + r.id"
                >
                  <RecordLine :record="r" />
                </MessageScrollerItem>
                <MessageScrollerItem
                  v-for="c in chatCards"
                  :key="'card-' + c.id"
                  :message-id="'card-' + c.id"
                >
                  <ApprovalCard v-if="c.kind === 'approval' || c.kind === 'pin'" :card="c" />
                  <ClarifyCard v-else-if="c.kind === 'clarify'" :card="c" />
                  <SecretCard v-else-if="c.kind === 'secret'" :card="c" />
                </MessageScrollerItem>
              </template>
            </MessageScrollerContent>
          </MessageScrollerViewport>
          <MessageScrollerButton class="bottom-[calc(var(--dock-h,96px)+16px)]" />
        </MessageScroller>
      </MessageScrollerProvider>
      <Composer @sent="onSent" @failed="onFailed" />
    </div>
    <TurnDrawer :open="drawerOpen" :tool="drawerTool" @close="closeDrawer" />
  </div>
</template>

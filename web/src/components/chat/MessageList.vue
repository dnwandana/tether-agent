<script setup lang="ts">
import { computed, ref, watch } from "vue"
import type { FileRef, MessageView, ToolView } from "../../api/types.gen"
import { session, loadMessages } from "../../stores/session"
import { turn } from "../../stores/turn"
import { renderMarkdown } from "../../lib/markdown"
import { ChevronDownIcon, ChevronRightIcon, FoldVerticalIcon, PaperclipIcon } from "@lucide/vue"
import { cn } from "@/lib/utils"
import { Attachment, AttachmentMedia, AttachmentTitle } from "@/components/ui/attachment"
import { Badge } from "@/components/ui/badge"
import { Bubble, BubbleContent } from "@/components/ui/bubble"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Marker, MarkerIcon } from "@/components/ui/marker"
import { Message, MessageContent, MessageHeader } from "@/components/ui/message"
import { MessageScrollerItem } from "@/components/ui/message-scroller"
import { Spinner } from "@/components/ui/spinner"
import { Trace } from "@/components/ui/trace"
import StreamText from "./StreamText.vue"
import ToolLine from "./ToolLine.vue"

defineProps<{ selected?: string }>()
const emit = defineEmits<{ tool: [view: ToolView] }>()

// The number of saved messages to draw, counted from the newest.
const drawn = ref(150)
// False when the last older load added no message.
const more = ref(true)

function shown(m: MessageView): boolean {
  return m.role === "summary" || m.role === "user" || m.role === "assistant"
}

const list = computed(() => session.messages.filter(shown).slice(-drawn.value))

// The server flag tells if older rows exist. A page with tool calls merges to fewer rows, so
// the count of loaded rows cannot tell it.
const canOlder = computed(
  () => more.value && (session.hasOlder || session.messages.filter(shown).length > drawn.value),
)

// Draws 100 more messages, and loads an older page when all loaded messages are drawn.
async function older(): Promise<void> {
  const reached = drawn.value >= session.messages.length
  drawn.value += 100
  if (!reached || session.messages.length === 0) return
  const before = session.messages.length
  // A summary row has a negative id. The server accepts only a positive id for before.
  const first = session.messages.find((m) => m.id > 0)
  if (!first) return
  try {
    await loadMessages({ before: first.id })
  } catch (e) {
    console.warn("older messages load failed", e)
    return
  }
  if (session.messages.length === before) more.value = false
}

function fileUrl(f: FileRef): string {
  return "/api/files/" + encodeURIComponent(f.id)
}

function html(src: string): string {
  return renderMarkdown(src)
}

const live = computed(() => turn.view)

// The ids of the user messages that steer a running turn. The server does not mark a steer, so
// the list marks it. The page loads the first user message of a turn at the first live event, so
// that message arrives while the turn runs too. The first new user message of a running turn
// starts the turn and stays an anchor. Each later new user message of the same turn is a steer.
// A steered message is never a scroll anchor, also after the turn ends.
// The set is not reactive: the watch below fills it before the next render reads it.
const steered = new Set<number>()
const seen = new Set<number>(session.messages.filter((m) => m.role === "user").map((m) => m.id))
let newest = Math.max(0, ...seen)
// True when the running turn already has its first user message. When the list mounts during a
// turn, the loaded messages hold that first message. A new session has no message yet: the list
// mounts at the start of the turn, before its first message loads.
let opened = Boolean(turn.view?.running) && seen.size > 0
watch(
  () => Boolean(turn.view?.running),
  (running) => {
    if (running) opened = false
  },
)
watch(
  () => session.messages.filter((m) => m.role === "user").map((m) => m.id),
  (ids) => {
    for (const id of ids) {
      if (seen.has(id)) continue
      seen.add(id)
      // An older page has smaller ids. It is not a steer.
      if (id <= newest || !turn.view?.running) continue
      if (opened) steered.add(id)
      else opened = true
    }
    newest = Math.max(newest, ...ids)
  },
)
</script>

<template>
  <MessageScrollerItem v-if="canOlder" message-id="older">
    <Button variant="ghost" size="sm" data-act="older" @click="older">Load older messages</Button>
  </MessageScrollerItem>
  <MessageScrollerItem
    v-for="m in list"
    :key="m.id"
    :message-id="String(m.id)"
    :scroll-anchor="m.role === 'user' && !steered.has(m.id)"
  >
    <Collapsible v-if="m.role === 'summary'" v-slot="{ open }">
      <Marker variant="separator">
        <MarkerIcon><FoldVerticalIcon /></MarkerIcon>
        <CollapsibleTrigger as-child>
          <Button variant="ghost" size="sm"
            >Compacted summary<ChevronDownIcon v-if="open" /><ChevronRightIcon v-else
          /></Button>
        </CollapsibleTrigger>
      </Marker>
      <CollapsibleContent class="px-3 pt-2 text-sm whitespace-pre-wrap text-muted-foreground">{{
        m.text
      }}</CollapsibleContent>
    </Collapsible>
    <Message v-else-if="m.role === 'user'" align="end" :class="cn(m.compacted && 'opacity-60')">
      <Bubble variant="muted" align="end">
        <BubbleContent class="whitespace-pre-wrap">{{ m.text }}</BubbleContent>
      </Bubble>
    </Message>
    <Message v-else :class="cn(m.compacted && 'opacity-60')">
      <MessageContent>
        <MessageHeader class="gap-2">
          <span>Tether</span>
          <span v-if="m.model">{{ m.model }}</span>
          <Badge v-if="m.answered_by" variant="outline" class="text-warning"
            >answered by {{ m.answered_by }}</Badge
          >
        </MessageHeader>
        <slot name="reasoning" :message="m" />
        <Trace v-if="m.tools.length > 0">
          <ToolLine
            v-for="t in m.tools"
            :key="t.call"
            :tool="t"
            :selected="selected === t.call"
            @select="emit('tool', t)"
          />
        </Trace>
        <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown output only -->
        <div v-if="m.text !== ''" class="typeset typeset-chat" v-html="html(m.text)" />
        <template v-for="f in m.files" :key="f.id">
          <Attachment v-if="f.kind === 'image'" orientation="vertical">
            <AttachmentMedia variant="image">
              <img :src="fileUrl(f)" :alt="f.name" />
            </AttachmentMedia>
          </Attachment>
          <audio v-else-if="f.kind === 'audio'" controls :src="fileUrl(f)" />
          <video v-else-if="f.kind === 'video'" controls :src="fileUrl(f)" />
          <Attachment v-else-if="f.kind === 'blocked'" state="error">
            <AttachmentTitle>[blocked file]</AttachmentTitle>
          </Attachment>
          <Attachment v-else-if="f.kind === 'missing'" state="error">
            <AttachmentTitle>[missing file]</AttachmentTitle>
          </Attachment>
          <Attachment v-else>
            <AttachmentTitle>
              <a :href="fileUrl(f)" download class="inline-flex items-center gap-1"
                ><PaperclipIcon class="size-4 shrink-0" />{{ f.name }}</a
              >
            </AttachmentTitle>
          </Attachment>
        </template>
        <slot name="footer" :message="m" />
      </MessageContent>
    </Message>
  </MessageScrollerItem>
  <MessageScrollerItem v-if="live || $slots.live" message-id="live">
    <Message v-if="live">
      <MessageContent>
        <MessageHeader class="gap-2"><span>Tether</span></MessageHeader>
        <Trace v-if="live.tools.length > 0">
          <ToolLine
            v-for="t in live.tools"
            :key="t.call"
            :tool="t"
            :selected="selected === t.call"
            @select="emit('tool', t)"
          />
        </Trace>
        <StreamText v-if="live.text !== ''" :text="live.text" :running="live.running" />
        <div
          v-else-if="live.running && live.tools.length === 0"
          class="flex items-center gap-2 text-muted-foreground"
        >
          <Spinner aria-hidden="true" />
          working
        </div>
        <slot name="live" />
      </MessageContent>
    </Message>
    <slot v-else name="live" />
  </MessageScrollerItem>
</template>

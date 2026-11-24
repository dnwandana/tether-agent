<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { CostView, MessagePage, MessageView, SessionRow, ToolView } from "../api/types.gen"
import CostFooter from "../components/chat/CostFooter.vue"
import ReasoningBlock from "../components/chat/ReasoningBlock.vue"
import ScrollToMessage from "../components/chat/ScrollToMessage.vue"
import ToolLine from "../components/chat/ToolLine.vue"
import ToolResult from "../components/chat/ToolResult.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useCommand } from "../lib/command"
import { useLiveData } from "../lib/live"
import { renderMarkdown } from "../lib/markdown"
import { fullTime, shortTime } from "../lib/time"
import { session } from "../stores/session"
import { toast } from "../stores/ui"
import { cn } from "@/lib/utils"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Bubble, BubbleContent } from "@/components/ui/bubble"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Marker } from "@/components/ui/marker"
import { Message, MessageContent, MessageHeader } from "@/components/ui/message"
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@/components/ui/message-scroller"
import { Trace, TraceCollapsible, TraceCollapsibleContent } from "@/components/ui/trace"

const route = useRoute()
const router = useRouter()
const { run } = useCommand()
const id = computed(() => String(route.params.id))
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const info = ref<SessionRow | null>(null)
const parentTitle = ref("")
const messages = ref<MessageView[]>([])
// The smallest saved message id of the loaded pages. It is the cursor of the older page.
const firstId = ref(0)
// The largest saved message id of the loaded pages. It is the cursor of the newer page.
const lastId = ref(0)
const more = ref(false)
const newer = ref(false)
const openTool = ref<ToolView | null>(null)
// The id of the message to scroll to. It is empty when the route has no search hit.
const jumpTo = ref("")

function usd(n: number, unpriced: boolean): string {
  return "$" + n.toFixed(4) + (unpriced ? "+" : "")
}

function when(s: string): string {
  return shortTime(s) || "—"
}

function costOf(m: MessageView): CostView {
  return {
    model: m.model,
    provider: m.provider,
    calls: 1,
    in: m.in,
    cached: m.cached,
    out: m.out,
    usd: m.cost_usd,
    unpriced: m.unpriced,
    seconds: m.latency_ms / 1000,
    answered_by: m.answered_by,
  }
}

// Returns the arguments with 2 spaces of indent, or the raw text when it is not JSON.
function argsText(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

const shown = computed(() => messages.value.filter((m) => m.role !== "tool"))
const hitId = computed(() => {
  const m = Number(route.query.message)
  return Number.isFinite(m) && m > 0 ? m : 0
})

// Counts the loads. A reply of an older load or of another session must not write to the page.
let gen = 0

async function load(): Promise<void> {
  const my = ++gen
  jumpTo.value = ""
  state.value = "loading"
  openTool.value = null
  try {
    const base = `/api/sessions/${id.value}`
    const row = await api<SessionRow>("GET", base)
    if (my !== gen) return
    info.value = row
    parentTitle.value = ""
    if (row.parent_id) {
      try {
        const parent = await api<SessionRow>("GET", `/api/sessions/${row.parent_id}`)
        if (my !== gen) return
        parentTitle.value = parent.title
      } catch {
        if (my !== gen) return
        parentTitle.value = ""
      }
    }
    // A tool result merges into the view of its call, so the number of views does not tell if
    // older messages exist. The page tells it with has_older.
    const page = await api<MessagePage>(
      "GET",
      hitId.value > 0
        ? `${base}/messages?around=${hitId.value}&limit=25`
        : `${base}/messages?limit=50`,
    )
    if (my !== gen) return
    messages.value = page.rows ?? []
    firstId.value = page.first_id
    lastId.value = page.last_id
    more.value = page.has_older
    newer.value = page.has_newer
    state.value = "ready"
    jumpTo.value = hitId.value > 0 ? String(hitId.value) : ""
  } catch (e) {
    if (my !== gen) return
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

// Loads the 50 saved messages before the smallest saved id of the loaded pages. A summary row has
// a negative id and stays where the server puts it. A row that shows already is not added again.
async function loadOlder(): Promise<void> {
  if (firstId.value <= 0) return
  const my = gen
  try {
    const page = await api<MessagePage>(
      "GET",
      `/api/sessions/${id.value}/messages?before=${firstId.value}&limit=50`,
    )
    if (my !== gen) return
    const have = new Set(messages.value.map((m) => m.id))
    messages.value = [...(page.rows ?? []).filter((m) => !have.has(m.id)), ...messages.value]
    if (page.first_id > 0) firstId.value = Math.min(firstId.value, page.first_id)
    more.value = page.has_older
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

// Loads the 50 saved messages after the largest saved id of the loaded pages.
async function loadNewer(): Promise<void> {
  const my = gen
  try {
    const page = await api<MessagePage>(
      "GET",
      `/api/sessions/${id.value}/messages?after=${lastId.value}&limit=50`,
    )
    if (my !== gen) return
    const have = new Set(messages.value.map((m) => m.id))
    messages.value = [...messages.value, ...(page.rows ?? []).filter((m) => !have.has(m.id))]
    lastId.value = Math.max(lastId.value, page.last_id)
    newer.value = page.has_newer
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

// A live reload keeps the older pages. It reads the session row and the newer messages.
async function live(): Promise<void> {
  if (state.value !== "ready") {
    await load()
    return
  }
  const my = gen
  try {
    const row = await api<SessionRow>("GET", `/api/sessions/${id.value}`)
    if (my !== gen) return
    info.value = row
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  await loadNewer()
}

const { stale, updatedAt, refresh } = useLiveData(live)

function jumpLatest(): void {
  void router.push({ name: "transcript", params: { id: id.value } })
}

async function selectTool(t: ToolView): Promise<void> {
  if (openTool.value?.call === t.call) {
    openTool.value = null
    return
  }
  try {
    openTool.value = await api<ToolView>(
      "GET",
      `/api/sessions/${id.value}/tools/${encodeURIComponent(t.call)}`,
    )
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

// Downloads a file with the session header. A plain link cannot send it.
async function download(path: string, name: string): Promise<void> {
  try {
    let h: string | null = null
    try {
      h = localStorage.getItem("tether.session")
    } catch {
      h = null
    }
    const r = await fetch(path, {
      headers: h ? { "X-Tether-Session": h } : {},
      credentials: "same-origin",
    })
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
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
    toast(friendlyError(e), "error")
  }
}

async function fork(): Promise<void> {
  if ((await run("/fork from " + id.value)) === "ok") void router.push({ name: "chat" })
}

async function resume(): Promise<void> {
  if ((await run("/resume " + id.value)) === "ok") void router.push({ name: "chat" })
}

watch(
  () => [route.params.id, route.query.message],
  () => {
    if (route.name === "transcript") void load()
  },
)
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div class="flex shrink-0 flex-col gap-3 px-4 pt-4 tab:px-6">
      <router-link
        class="text-sm text-muted-foreground underline-offset-4 hover:underline"
        :to="{ name: 'sessions' }"
        >‹ Sessions</router-link
      >

      <StateBlock v-if="state === 'loading'" state="loading" />
      <StateBlock
        v-else-if="state === 'error'"
        state="error"
        :text="errorText"
        action="Retry"
        @action="load"
      />
      <template v-else-if="info">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="text-xl font-semibold tracking-tight">{{ info.title }}</h1>
          <Badge variant="secondary">{{ info.source_label }}</Badge>
          <Badge variant="secondary">{{ info.kind }}</Badge>
          <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        </div>
        <router-link
          v-if="parentTitle"
          class="text-sm text-muted-foreground underline underline-offset-4"
          :to="{ name: 'transcript', params: { id: info.parent_id } }"
          >↳ child of {{ parentTitle }}</router-link
        >
        <div
          v-if="(info.children ?? []).length > 0"
          class="flex flex-wrap items-center gap-2 text-sm text-muted-foreground"
        >
          Children:
          <router-link
            v-for="c in info.children"
            :key="c.id"
            class="underline underline-offset-4"
            :to="{ name: 'transcript', params: { id: c.id } }"
            >{{ c.title }}</router-link
          >
        </div>
        <div class="grid grid-cols-2 gap-3 tab:grid-cols-4">
          <Card>
            <CardHeader>
              <CardDescription>Messages</CardDescription>
              <CardTitle>{{ info.messages }}</CardTitle>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Cost</CardDescription>
              <CardTitle>{{ usd(info.cost_usd, false) }}</CardTitle>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Started</CardDescription>
              <CardTitle>
                <span class="whitespace-nowrap" :title="fullTime(info.started)">{{
                  when(info.started)
                }}</span>
              </CardTitle>
            </CardHeader>
          </Card>
          <Card>
            <CardHeader>
              <CardDescription>Children</CardDescription>
              <CardTitle>{{ (info.children ?? []).length }}</CardTitle>
            </CardHeader>
          </Card>
        </div>

        <Alert v-if="info.read_only">
          <AlertDescription class="flex flex-wrap items-center gap-2">
            <span class="flex-1"
              >👁 Read-only. This session belongs to {{ info.source_label }}.</span
            >
            <Button v-if="info.kind === 'chat'" variant="secondary" @click="fork">
              Fork into dashboard
            </Button>
            <Button
              variant="ghost"
              @click="download(`/api/sessions/${info.id}/export`, `session-${info.id}.jsonl`)"
            >
              Export JSONL
            </Button>
          </AlertDescription>
        </Alert>
        <div v-else class="flex justify-end gap-2">
          <Button
            v-if="info.id === session.id"
            variant="secondary"
            @click="router.push({ name: 'chat' })"
          >
            Open in Chat
          </Button>
          <Button v-else variant="secondary" @click="resume">Resume</Button>
          <Button
            variant="ghost"
            @click="download(`/api/sessions/${info.id}/export`, `session-${info.id}.jsonl`)"
          >
            Export JSONL
          </Button>
        </div>

        <Button v-if="hitId > 0" class="self-start" variant="ghost" size="sm" @click="jumpLatest">
          Jump to latest
        </Button>
      </template>
    </div>

    <template v-if="state === 'ready' && info">
      <div v-if="shown.length === 0" class="px-4 tab:px-6">
        <StateBlock state="empty" text="No messages yet." />
      </div>
      <MessageScrollerProvider v-else default-scroll-position="end">
        <MessageScroller class="min-h-0 flex-1">
          <MessageScrollerViewport :preserve-scroll-on-prepend="true">
            <MessageScrollerContent class="mx-auto w-full max-w-[760px] px-4 py-6">
              <MessageScrollerItem v-if="more" message-id="older">
                <Button variant="ghost" size="sm" @click="loadOlder">Load older</Button>
              </MessageScrollerItem>
              <MessageScrollerItem
                v-for="m in shown"
                :key="m.id"
                :message-id="String(m.id)"
                :data-hit="m.id === hitId ? 'true' : undefined"
                :class="
                  cn(
                    m.compacted && 'opacity-70',
                    m.id === hitId && 'rounded-lg ring-2 ring-ring ring-offset-4',
                  )
                "
              >
                <Collapsible v-if="m.role === 'summary'">
                  <Marker variant="separator">
                    <CollapsibleTrigger as-child>
                      <Button variant="ghost" size="sm">🗜 Compacted · summary</Button>
                    </CollapsibleTrigger>
                  </Marker>
                  <CollapsibleContent
                    class="px-3 pt-2 text-sm whitespace-pre-wrap text-muted-foreground"
                    >{{ m.text }}</CollapsibleContent
                  >
                </Collapsible>
                <Message v-else-if="m.role === 'user'" align="end">
                  <MessageHeader class="gap-2">
                    <span>You</span>
                    <time
                      class="whitespace-nowrap"
                      :datetime="m.created"
                      :title="fullTime(m.created)"
                      >{{ when(m.created) }}</time
                    >
                    <Badge v-if="m.id === hitId" variant="secondary">Search hit</Badge>
                  </MessageHeader>
                  <Bubble variant="muted" align="end">
                    <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown output only -->
                    <BubbleContent class="typeset typeset-chat" v-html="renderMarkdown(m.text)" />
                  </Bubble>
                  <Button
                    v-for="f in m.files"
                    :key="f.id"
                    variant="outline"
                    size="sm"
                    @click="download(`/api/files/${f.id}`, f.name)"
                  >
                    {{ f.name }}
                  </Button>
                </Message>
                <Message v-else-if="m.role === 'assistant'">
                  <MessageHeader class="gap-2">
                    <span>Tether</span>
                    <time
                      class="whitespace-nowrap"
                      :datetime="m.created"
                      :title="fullTime(m.created)"
                      >{{ when(m.created) }}</time
                    >
                    <Badge v-if="m.id === hitId" variant="secondary">Search hit</Badge>
                  </MessageHeader>
                  <ReasoningBlock :text="m.reasoning" :tokens="m.reasoning_tokens" />
                  <Trace v-if="m.tools.length > 0">
                    <TraceCollapsible
                      v-for="t in m.tools"
                      :key="t.call"
                      :open="openTool?.call === t.call"
                    >
                      <ToolLine
                        :tool="t"
                        :selected="openTool?.call === t.call"
                        @select="selectTool(t)"
                      />
                      <TraceCollapsibleContent>
                        <template v-if="openTool?.call === t.call">
                          <pre
                            class="overflow-x-auto rounded-md border bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
                            >{{ argsText(openTool.args) }}</pre>
                          <ToolResult :tool="openTool" />
                        </template>
                      </TraceCollapsibleContent>
                    </TraceCollapsible>
                  </Trace>
                  <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown output only -->
                  <MessageContent class="typeset typeset-chat" v-html="renderMarkdown(m.text)" />
                  <Button
                    v-for="f in m.files"
                    :key="f.id"
                    variant="outline"
                    size="sm"
                    @click="download(`/api/files/${f.id}`, f.name)"
                  >
                    {{ f.name }}
                  </Button>
                  <CostFooter :cost="costOf(m)" />
                </Message>
              </MessageScrollerItem>
              <MessageScrollerItem v-if="newer" message-id="newer">
                <Button variant="ghost" size="sm" @click="loadNewer">Load newer</Button>
              </MessageScrollerItem>
            </MessageScrollerContent>
          </MessageScrollerViewport>
          <MessageScrollerButton />
          <ScrollToMessage :message-id="jumpTo" />
        </MessageScroller>
      </MessageScrollerProvider>
    </template>
  </div>
</template>

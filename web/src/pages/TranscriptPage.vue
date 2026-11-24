<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { CostView, MessagePage, MessageView, SessionRow, ToolView } from "../api/types.gen"
import CostFooter from "../components/chat/CostFooter.vue"
import ReasoningBlock from "../components/chat/ReasoningBlock.vue"
import ToolLine from "../components/chat/ToolLine.vue"
import ToolResult from "../components/chat/ToolResult.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useCommand } from "../lib/command"
import { useLiveData } from "../lib/live"
import { renderMarkdown } from "../lib/markdown"
import { session } from "../stores/session"
import { toast } from "../stores/ui"

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

function usd(n: number, unpriced: boolean): string {
  return "$" + n.toFixed(4) + (unpriced ? "+" : "")
}

function when(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? "—" : d.toLocaleString()
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
    if (hitId.value > 0) {
      await nextTick()
      document.getElementById("msg-" + hitId.value)?.scrollIntoView?.({ block: "center" })
    }
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
  <div class="page">
    <router-link class="back" :to="{ name: 'sessions' }">‹ Sessions</router-link>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <template v-else-if="info">
      <div class="page-h">
        <h1 class="t-20">{{ info.title }}</h1>
        <span class="chip">{{ info.source_label }}</span>
        <span class="chip">{{ info.kind }}</span>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      </div>
      <router-link
        v-if="parentTitle"
        class="link"
        :to="{ name: 'transcript', params: { id: info.parent_id } }"
        >↳ child of {{ parentTitle }}</router-link
      >
      <div v-if="(info.children ?? []).length > 0" class="t-12 c-3">
        Children:
        <router-link
          v-for="c in info.children"
          :key="c.id"
          class="link"
          :to="{ name: 'transcript', params: { id: c.id } }"
          >{{ c.title }}</router-link
        >
      </div>
      <div class="grid-4">
        <div class="card">
          <div class="c-3 t-12">Messages</div>
          <b>{{ info.messages }}</b>
        </div>
        <div class="card">
          <div class="c-3 t-12">Cost</div>
          <b>{{ usd(info.cost_usd, false) }}</b>
        </div>
        <div class="card">
          <div class="c-3 t-12">Started</div>
          <b>{{ when(info.started) }}</b>
        </div>
        <div class="card">
          <div class="c-3 t-12">Children</div>
          <b>{{ (info.children ?? []).length }}</b>
        </div>
      </div>

      <div v-if="info.read_only" class="ro-banner">
        <span class="grow">👁 Read-only. This session belongs to {{ info.source_label }}.</span>
        <button v-if="info.kind === 'chat'" class="btn btn-secondary" type="button" @click="fork">
          Fork into dashboard
        </button>
        <button
          class="btn btn-ghost"
          type="button"
          @click="download(`/api/sessions/${info.id}/export`, `session-${info.id}.jsonl`)"
        >
          Export JSONL
        </button>
      </div>
      <div v-else class="ro-banner">
        <span class="grow" />
        <button
          v-if="info.id === session.id"
          class="btn btn-secondary"
          type="button"
          @click="router.push({ name: 'chat' })"
        >
          Open in Chat
        </button>
        <button v-else class="btn btn-secondary" type="button" @click="resume">Resume</button>
        <button
          class="btn btn-ghost"
          type="button"
          @click="download(`/api/sessions/${info.id}/export`, `session-${info.id}.jsonl`)"
        >
          Export JSONL
        </button>
      </div>

      <button v-if="more" class="btn btn-ghost" type="button" @click="loadOlder">Load older</button>
      <button v-if="hitId > 0" class="btn btn-ghost" type="button" @click="jumpLatest">
        Jump to latest
      </button>
      <StateBlock v-if="shown.length === 0" state="empty" text="No messages yet." />

      <template v-for="m in shown" :key="m.id">
        <details
          v-if="m.role === 'summary'"
          :id="'msg-' + m.id"
          class="card"
          :class="{ 'is-hit': m.id === hitId }"
        >
          <summary>🗜 Compacted · summary</summary>
          <div>{{ m.text }}</div>
        </details>
        <div
          v-else-if="m.role === 'user'"
          :id="'msg-' + m.id"
          class="msg msg-user"
          :class="{ 'is-hit': m.id === hitId }"
          :style="m.compacted ? 'opacity: 0.7' : undefined"
        >
          <div class="msg-meta">
            <b>You</b>
            <time :datetime="m.created">{{ when(m.created) }}</time>
            <span v-if="m.id === hitId" class="chip chip-acc">Search hit</span>
          </div>
          <!-- eslint-disable-next-line vue/no-v-html -->
          <div class="bubble-user" v-html="renderMarkdown(m.text)" />
          <button
            v-for="f in m.files"
            :key="f.id"
            class="att"
            type="button"
            @click="download(`/api/files/${f.id}`, f.name)"
          >
            {{ f.name }}
          </button>
        </div>
        <div
          v-else-if="m.role === 'assistant'"
          :id="'msg-' + m.id"
          class="msg"
          :class="{ 'is-hit': m.id === hitId }"
          :style="m.compacted ? 'opacity: 0.7' : undefined"
        >
          <div class="msg-meta">
            <b>Tether</b>
            <time :datetime="m.created">{{ when(m.created) }}</time>
            <span v-if="m.id === hitId" class="chip chip-acc">Search hit</span>
          </div>
          <ReasoningBlock :text="m.reasoning" :tokens="m.reasoning_tokens" />
          <template v-for="t in m.tools" :key="t.call">
            <ToolLine :tool="t" :selected="openTool?.call === t.call" @select="selectTool(t)" />
            <template v-if="openTool?.call === t.call">
              <pre class="code-block tool-args">{{ argsText(openTool.args) }}</pre>
              <ToolResult :tool="openTool" />
            </template>
          </template>
          <!-- eslint-disable-next-line vue/no-v-html -->
          <div class="answer" v-html="renderMarkdown(m.text)" />
          <button
            v-for="f in m.files"
            :key="f.id"
            class="att"
            type="button"
            @click="download(`/api/files/${f.id}`, f.name)"
          >
            {{ f.name }}
          </button>
          <CostFooter :cost="costOf(m)" />
        </div>
      </template>
      <button v-if="newer" class="btn btn-ghost" type="button" @click="loadNewer">
        Load newer
      </button>
    </template>
  </div>
</template>

<style scoped>
.is-hit {
  outline: 2px solid var(--accent-tint);
  outline-offset: 4px;
  border-radius: 8px;
}
</style>

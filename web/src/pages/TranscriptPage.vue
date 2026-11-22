<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError, runCommand } from "../api/client"
import type { MessagePage, MessageView, ReplyView, SessionRow } from "../api/types.gen"
import StateBlock from "../components/StateBlock.vue"
import { renderMarkdown } from "../lib/markdown"
import { session } from "../stores/session"
import { confirm, toast } from "../stores/ui"

const route = useRoute()
const router = useRouter()
const id = computed(() => String(route.params.id))
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const info = ref<SessionRow | null>(null)
const messages = ref<MessageView[]>([])
// The smallest saved message id of the loaded pages. It is the cursor of the older page.
const firstId = ref(0)
const more = ref(false)

// Formats a token count: 655 -> "655", 40300 -> "40.3k".
function tokens(n: number): string {
  return n > 999 ? (n / 1000).toFixed(1) + "k" : String(n)
}

function secs(ms: number): string {
  return (ms / 1000).toFixed(1) + " s"
}

function usd(n: number, unpriced: boolean): string {
  return "$" + n.toFixed(4) + (unpriced ? "+" : "")
}

function when(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? "—" : d.toLocaleString()
}

const shown = computed(() => messages.value.filter((m) => m.role !== "tool"))
const hitId = computed(() => {
  const m = Number(route.query.message)
  return Number.isFinite(m) && m > 0 ? m : 0
})

async function load(): Promise<void> {
  state.value = "loading"
  try {
    const base = `/api/sessions/${id.value}`
    info.value = await api<SessionRow>("GET", base)
    // A tool result merges into the view of its call, so the number of views does not tell if
    // older messages exist. The page tells it with has_older.
    const page = await api<MessagePage>(
      "GET",
      hitId.value > 0
        ? `${base}/messages?around=${hitId.value}&limit=25`
        : `${base}/messages?limit=50`,
    )
    messages.value = page.rows ?? []
    firstId.value = page.first_id
    more.value = page.has_older
    state.value = "ready"
    if (hitId.value > 0) {
      await nextTick()
      document.getElementById("msg-" + hitId.value)?.scrollIntoView?.({ block: "center" })
    }
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

// Loads the 50 saved messages before the smallest saved id of the loaded pages. A summary row has
// a negative id and stays where the server puts it. A row that shows already is not added again.
async function loadOlder(): Promise<void> {
  if (firstId.value <= 0) return
  try {
    const page = await api<MessagePage>(
      "GET",
      `/api/sessions/${id.value}/messages?before=${firstId.value}&limit=50`,
    )
    const have = new Set(messages.value.map((m) => m.id))
    messages.value = [...(page.rows ?? []).filter((m) => !have.has(m.id)), ...messages.value]
    if (page.first_id > 0) firstId.value = Math.min(firstId.value, page.first_id)
    more.value = page.has_older
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

async function act(text: string, navigates = false): Promise<boolean> {
  try {
    const r = await runCommand(text)
    if (!("text" in r) || r.text === undefined) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const button = (reply.buttons ?? []).flat().find((b) => b.data.endsWith("confirm"))
      if (button) {
        const [title, ...rest] = reply.text.split("\n")
        const yes = await confirm({
          title,
          body: rest.join("\n").trim(),
          confirmLabel: button.text,
          danger: true,
        })
        if (yes) await act(button.data, navigates)
        return yes
      }
      toast(reply.text, "ok")
    }
    if (!navigates) await load()
    return true
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return false
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
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

async function fork(): Promise<void> {
  if (await act("/fork from " + id.value, true)) router.push({ name: "chat" })
}

async function resume(): Promise<void> {
  if (await act("/resume " + id.value, true)) router.push({ name: "chat" })
}

watch(
  () => [route.params.id, route.query.message],
  () => {
    if (route.name === "transcript") void load()
  },
)
onMounted(load)
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
      @retry="load"
    />
    <template v-else-if="info">
      <div class="page-h">
        <h1 class="t-20">{{ info.title }}</h1>
        <span class="chip">{{ info.source_label }}</span>
        <span class="chip">{{ info.kind }}</span>
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
          <div class="msg-meta"><b>You</b></div>
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
            <span v-if="m.answered_by" class="chip">answered by {{ m.answered_by }}</span>
          </div>
          <details v-if="m.reasoning" class="reasoning">
            <summary>▸ Reasoning · {{ m.reasoning_tokens }} tokens</summary>
            <div class="rs-b">{{ m.reasoning }}</div>
          </details>
          <div v-for="t in m.tools" :key="t.call" class="toolline is-done">
            <span class="em">{{ t.emoji || "🛠" }}</span>
            <span class="name">{{ t.name }}</span>
            <span v-if="t.preview" class="args">"{{ t.preview }}"</span>
          </div>
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
          <div class="cost-foot">
            <span>{{ m.model }} · {{ m.provider }}</span>
            <span>{{ tokens(m.in) }} in ({{ tokens(m.cached) }} cached)</span>
            <span>{{ tokens(m.out) }} out</span>
            <span>TTFT {{ secs(m.first_ms) }}</span>
            <span>{{ secs(m.latency_ms) }}</span>
            <span>{{ usd(m.cost_usd, m.unpriced) }}</span>
          </div>
        </div>
      </template>
    </template>
  </div>
</template>

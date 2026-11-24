<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRoute } from "vue-router"
import { api } from "../../api/client"
import type { LogEntry, LogPage } from "../../api/types.gen"
import { friendlyError } from "../../lib/errors"
import { useLiveData } from "../../lib/live"
import { fullTime, shortTime } from "../../lib/time"
import DataTable from "../DataTable.vue"
import SidePanel from "../SidePanel.vue"
import StateBlock from "../StateBlock.vue"

const props = defineProps<{ scopes: string[]; scopeLabels: Record<string, string> }>()

const route = useRoute()
const PAGE = 20
const DECISION: Record<string, string> = {
  allowed: "chip-ok",
  denied: "chip-bad",
  blocked: "chip-warn",
}

const firstScope = route.query.scope
const scope = ref(typeof firstScope === "string" ? firstScope : "")
// The route can name a scope that the list does not have. The select then needs an option for it.
const scopeOptions = computed(() =>
  scope.value && !props.scopes.includes(scope.value)
    ? [scope.value, ...props.scopes]
    : props.scopes,
)
// Returns the label of a scope key. A key with no label shows the key.
function scopeName(key: string): string {
  return props.scopeLabels[key] || key
}
const decision = ref("")
const noAnswer = ref(false)
const q = ref("")
const offset = ref(0)
const page = ref<LogPage | null>(null)
const state = ref<"loading" | "ready" | "error">("loading")
const cause = ref("")
const busy = ref(false)
const detail = ref<LogEntry | null>(null)

let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

// Returns the request path for one page of the log.
function path(off: number): string {
  return (
    "/api/security/log?" +
    new URLSearchParams({
      scope: scope.value,
      decision: decision.value,
      source: noAnswer.value ? "timeout" : "",
      q: q.value.trim(),
      offset: String(off),
      limit: String(PAGE),
    }).toString()
  )
}

// Reads one page. A reply of an old request is dropped.
async function read(off: number): Promise<void> {
  const my = ++seq
  busy.value = true
  try {
    const r = await api<LogPage>("GET", path(off))
    if (my !== seq) return
    page.value = r
    offset.value = off
    state.value = "ready"
  } catch (e) {
    if (my !== seq) return
    cause.value = friendlyError(e)
    state.value = "error"
  } finally {
    if (my === seq) busy.value = false
  }
}

useLiveData(() => read(offset.value), {
  events: ["card.done"],
  filter: (_n, d) => (d as { kind?: string }).kind === "approval",
})

watch([scope, decision, noAnswer], () => void read(0))
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void read(0), 250)
})

// Clears the scope filter.
function showAll(): void {
  scope.value = ""
}

const phone = ref(
  (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia?.("(max-width: 599px)")
    ?.matches ?? false,
)
let mql: MediaQueryList | null = null
function onChange(e: MediaQueryListEvent): void {
  phone.value = e.matches
}
onMounted(() => {
  mql = globalThis.matchMedia?.("(max-width: 599px)") ?? null
  mql?.addEventListener?.("change", onChange)
})
onBeforeUnmount(() => {
  clearTimeout(timer)
  mql?.removeEventListener?.("change", onChange)
  mql = null
})

const allCols = [
  { key: "at", label: "Time" },
  { key: "scope", label: "Scope" },
  { key: "tool", label: "Tool" },
  { key: "rule", label: "Rule" },
  { key: "decision", label: "Decision" },
  { key: "answer", label: "Answer" },
  { key: "preview", label: "Preview" },
]
const phoneCols = [
  { key: "at", label: "Time" },
  { key: "decision", label: "Decision" },
  { key: "tool", label: "Tool" },
  { key: "preview", label: "Preview" },
]
const cols = computed(() => (phone.value ? phoneCols : allCols))
const rows = computed(
  () => (page.value?.rows ?? []).map((r, i) => ({ ...r, idx: i })) as Record<string, unknown>[],
)
const total = computed(() => page.value?.total ?? 0)
const to = computed(() => (page.value?.offset ?? 0) + (page.value?.rows.length ?? 0))

function open(r: Record<string, unknown>): void {
  detail.value = page.value?.rows[r.idx as number] ?? null
}

const fields = computed(() => {
  const d = detail.value
  if (!d) return []
  return [
    ["Time", fullTime(d.at)],
    ["Scope", d.scope_label || d.scope],
    ["Kind", d.kind],
    ["Tool", d.tool],
    ["MCP server", d.server],
    ["MCP tool", d.tool_name],
    ["Rule", d.rule_description || d.rule],
    ["Rule ID", d.rule],
    ["Class", d.class],
    ["Decision", d.decision],
    ["Source", d.source],
    ["Answer", d.answer],
  ] as [string, string][]
})
</script>

<template>
  <div>
    <div class="toolbar">
      <select v-model="scope" class="input" aria-label="Scope">
        <option value="">All scopes</option>
        <option v-for="s in scopeOptions" :key="s" :value="s">{{ scopeName(s) }}</option>
      </select>
      <select v-model="decision" class="input" aria-label="Decision">
        <option value="">All decisions</option>
        <option value="allowed">allowed</option>
        <option value="denied">denied</option>
        <option value="blocked">blocked</option>
      </select>
      <label class="check"><input v-model="noAnswer" type="checkbox" /> No answer</label>
      <input
        v-model="q"
        class="input grow-search"
        type="search"
        aria-label="Search the previews"
        placeholder="Search the previews"
      />
      <span class="c-3 t-12">Kept 180 days.</span>
    </div>
    <StateBlock v-if="state === 'loading' && !page" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="read(offset)" />
    <StateBlock
      v-else-if="page && page.rows.length === 0"
      state="empty"
      text="No approvals in this scope."
      :action="scope ? 'Show all scopes' : undefined"
      @action="showAll"
    />
    <template v-else>
      <DataTable :columns="cols" :rows="rows" row-key="idx" class="table-dense" @row-click="open">
        <template #cell-at="{ row }"
          ><span style="white-space: nowrap" :title="fullTime(row.at as string)">{{
            shortTime(row.at as string) || "—"
          }}</span></template
        >
        <template #cell-scope="{ row }"
          ><bdi class="cell-title" :title="String(row.scope_label || row.scope)">{{
            row.scope_label || row.scope
          }}</bdi></template
        >
        <template #cell-rule="{ row }"
          ><span :title="row.rule as string">{{ row.rule_description || row.rule }}</span></template
        >
        <template #cell-decision="{ row }">
          <span class="chip" :class="DECISION[row.decision as string] ?? 'chip-plain'">{{
            row.decision
          }}</span>
        </template>
        <template #cell-preview="{ row }"
          ><code>{{ row.preview }}</code></template
        >
      </DataTable>
      <div class="pager">
        <span class="grow"
          >{{ total === 0 ? "0" : (page?.offset ?? 0) + 1 }}–{{ to }} of {{ total }}</span
        >
        <button
          class="btn btn-secondary btn-sm"
          type="button"
          :disabled="busy || offset === 0"
          @click="read(Math.max(0, offset - PAGE))"
        >
          ◀ Prev
        </button>
        <button
          class="btn btn-secondary btn-sm"
          type="button"
          :disabled="busy || to >= total"
          @click="read(offset + PAGE)"
        >
          Next ▶
        </button>
      </div>
    </template>
    <SidePanel :open="detail !== null" title="Approval detail" @close="detail = null">
      <dl v-if="detail" class="kv log-detail">
        <template v-for="[k, v] in fields" :key="k">
          <dt>{{ k }}</dt>
          <dd>{{ v || "—" }}</dd>
        </template>
        <dt>Session</dt>
        <dd>
          <router-link
            v-if="detail.session_id"
            :to="{ name: 'transcript', params: { id: detail.session_id } }"
            >{{ detail.session_id }}</router-link
          >
          <template v-else>—</template>
        </dd>
        <dt>Preview</dt>
        <dd>
          <pre class="code-block">{{ detail.preview }}</pre>
        </dd>
      </dl>
    </SidePanel>
  </div>
</template>

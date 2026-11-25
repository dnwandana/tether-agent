<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRoute } from "vue-router"
import { api } from "../../api/client"
import type { LogEntry, LogPage } from "../../api/types.gen"
import { friendlyError } from "../../lib/errors"
import { useLiveData } from "../../lib/live"
import { lastPage, readSize, writeSize, type PageSize } from "../../lib/pager"
import type { Column } from "../../lib/table"
import { fullTime, shortTime } from "../../lib/time"
import { Badge } from "@/components/ui/badge"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { NativeSelect } from "@/components/ui/native-select"
import DataTable from "../DataTable.vue"
import SidePanel from "../SidePanel.vue"
import StateBlock from "../StateBlock.vue"

const props = defineProps<{ scopes: string[]; scopeLabels: Record<string, string> }>()

const route = useRoute()
const DECISION: Record<string, "secondary" | "destructive" | "outline"> = {
  allowed: "secondary",
  denied: "destructive",
  blocked: "outline",
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
const log = ref<LogPage | null>(null)
const state = ref<"loading" | "ready" | "error">("loading")
const cause = ref("")
const busy = ref(false)
const page = ref(1)
const size = ref<PageSize>(readSize("approval-log"))
const detail = ref<LogEntry | null>(null)

let seq = 0
let timer: ReturnType<typeof setTimeout> | undefined

// Returns the request path for one page of the log.
function path(p: number): string {
  return (
    "/api/security/log?" +
    new URLSearchParams({
      scope: scope.value,
      decision: decision.value,
      source: noAnswer.value ? "timeout" : "",
      q: q.value.trim(),
      offset: String((p - 1) * size.value),
      limit: String(size.value),
    }).toString()
  )
}

// Reads one page. A reply of an old request is dropped. A page beyond the last page reads
// the last page.
async function read(p: number): Promise<void> {
  const my = ++seq
  page.value = p
  busy.value = true
  try {
    const r = await api<LogPage>("GET", path(p))
    if (my !== seq) return
    const last = lastPage(r.total, size.value)
    if (r.total > 0 && p > last) return read(last)
    log.value = r
    state.value = "ready"
  } catch (e) {
    if (my !== seq) return
    cause.value = friendlyError(e)
    state.value = "error"
  } finally {
    if (my === seq) busy.value = false
  }
}

// Reads the page that the pager asks for.
function goPage(p: number): void {
  if (p !== page.value) void read(p)
}

// Keeps the new size and reads page 1.
function setSize(n: PageSize): void {
  size.value = n
  writeSize("approval-log", n)
  void read(1)
}

useLiveData(() => read(page.value), {
  events: ["card.done"],
  filter: (_n, d) => (d as { kind?: string }).kind === "approval",
})

watch([scope, decision, noAnswer], () => void read(1))
watch(q, () => {
  clearTimeout(timer)
  timer = setTimeout(() => void read(1), 250)
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

const allCols: Column[] = [
  { key: "at", label: "Time", type: "date" },
  { key: "scope", label: "Scope", type: "text", optional: true },
  { key: "tool", label: "Tool", type: "text" },
  { key: "rule", label: "Rule", type: "text" },
  { key: "decision", label: "Decision", type: "badge" },
  { key: "answer", label: "Answer", type: "badge", optional: true },
  { key: "preview", label: "Preview", type: "block" },
]
const phoneCols: Column[] = [
  { key: "at", label: "Time", type: "date" },
  { key: "decision", label: "Decision", type: "badge" },
  { key: "tool", label: "Tool", type: "text" },
  { key: "preview", label: "Preview", type: "block" },
]
const cols = computed(() => (phone.value ? phoneCols : allCols))
const rows = computed(
  () => (log.value?.rows ?? []).map((r, i) => ({ ...r, idx: i })) as Record<string, unknown>[],
)
const total = computed(() => log.value?.total ?? 0)

function open(r: Record<string, unknown>): void {
  detail.value = log.value?.rows[r.idx as number] ?? null
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
    <div class="flex flex-wrap items-center gap-2">
      <NativeSelect v-model="scope" aria-label="Scope">
        <option value="">All scopes</option>
        <option v-for="s in scopeOptions" :key="s" :value="s">{{ scopeName(s) }}</option>
      </NativeSelect>
      <NativeSelect v-model="decision" aria-label="Decision">
        <option value="">All decisions</option>
        <option value="allowed">allowed</option>
        <option value="denied">denied</option>
        <option value="blocked">blocked</option>
      </NativeSelect>
      <div class="flex items-center gap-2">
        <Checkbox id="log-no-answer" v-model="noAnswer" />
        <Label for="log-no-answer">No answer</Label>
      </div>
      <Input
        v-model="q"
        type="search"
        aria-label="Search the previews"
        placeholder="Search the previews"
        class="w-auto min-w-40 flex-1"
      />
      <span class="text-xs text-muted-foreground">Kept 180 days.</span>
    </div>
    <StateBlock v-if="state === 'loading' && !log" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="read(page)" />
    <StateBlock
      v-else-if="log && log.total === 0"
      state="empty"
      text="No approvals in this scope."
      :action="scope ? 'Show all scopes' : undefined"
      @action="showAll"
    />
    <template v-else>
      <DataTable
        :columns="cols"
        :rows="rows"
        row-key="idx"
        page-key="approval-log"
        page-label="Approval log"
        :total="total"
        :page="page"
        :size="size"
        :loading="busy"
        @update:page="goPage"
        @update:size="setSize"
        @row-click="open"
      >
        <template #cell-at="{ row }"
          ><span class="whitespace-nowrap" :title="fullTime(row.at as string)">{{
            shortTime(row.at as string) || "—"
          }}</span></template
        >
        <template #cell-scope="{ row }"
          ><bdi class="block max-w-48 truncate" :title="String(row.scope_label || row.scope)">{{
            row.scope_label || row.scope
          }}</bdi></template
        >
        <template #cell-rule="{ row }"
          ><span class="block truncate" :title="row.rule as string">{{
            row.rule_description || row.rule
          }}</span></template
        >
        <template #cell-decision="{ row }">
          <Badge :variant="DECISION[row.decision as string] ?? 'outline'">{{ row.decision }}</Badge>
        </template>
        <template #cell-preview="{ row }"
          ><code class="font-mono text-xs break-all">{{ row.preview }}</code></template
        >
      </DataTable>
    </template>
    <SidePanel :open="detail !== null" title="Approval detail" @close="detail = null">
      <dl v-if="detail" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
        <template v-for="[k, v] in fields" :key="k">
          <dt class="text-muted-foreground">{{ k }}</dt>
          <dd class="min-w-0 break-words">{{ v || "—" }}</dd>
        </template>
        <dt class="text-muted-foreground">Session</dt>
        <dd class="min-w-0 break-words">
          <router-link
            v-if="detail.session_id"
            :to="{ name: 'transcript', params: { id: detail.session_id } }"
            >{{ detail.session_id }}</router-link
          >
          <template v-else>—</template>
        </dd>
        <dt class="text-muted-foreground">Preview</dt>
        <dd class="min-w-0 break-words">
          <pre class="rounded-md bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap">{{
            detail.preview
          }}</pre>
        </dd>
      </dl>
    </SidePanel>
  </div>
</template>

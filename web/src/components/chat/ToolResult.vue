<script setup lang="ts">
import { computed } from "vue"
import type { ToolView } from "../../api/types.gen"
import { renderMarkdown } from "../../lib/markdown"
import DiffView from "./DiffView.vue"
import CodeView from "./CodeView.vue"
import DelegateCards from "./DelegateCards.vue"
import SearchList from "./SearchList.vue"
import TerminalView from "./TerminalView.vue"
import {
  fileLink,
  parseDelegate,
  parseNavigate,
  parseReadFile,
  parseSessionSearch,
  parseWebSearch,
  unwrapUntrusted,
} from "./parse"

const props = defineProps<{ tool: ToolView }>()
const emit = defineEmits<{ "show-all": [tool: ToolView] }>()

// Reads the arguments of the call. Gives {} when they do not parse.
function args(t: ToolView): Record<string, unknown> {
  try {
    const v = JSON.parse(t.args)
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}

function str(v: unknown): string {
  return typeof v === "string" ? v : ""
}

const oneLine = ["write_file", "browser_click", "browser_fill"]
const untrustedTools = ["web_fetch", "browser_read"]
const imageTools = ["browser_take_screenshot", "generate_image"]

const a = computed(() => args(props.tool))
const name = computed(() => props.tool.name)
const text = computed(() => props.tool.text)
const firstLine = computed(() => text.value.split("\n")[0])
const unwrapped = computed(() => unwrapUntrusted(text.value))
const html = computed(() => renderMarkdown(unwrapped.value.body))
const nav = computed(() => parseNavigate(text.value))
const link = computed(() => fileLink(text.value))
// Each new view applies only when its parser gives a result.
const readOk = computed(() => {
  if (name.value !== "read_file") return false
  const r = parseReadFile(text.value)
  return r.lines.length > 0 || r.note !== ""
})
const webOk = computed(() => name.value === "web_search" && parseWebSearch(text.value).length > 0)
const sessionOk = computed(
  () => name.value === "session_search" && parseSessionSearch(text.value).length > 0,
)
const delegateOk = computed(
  () => name.value === "delegate_task" && parseDelegate(text.value) !== null,
)
const known = computed(
  () =>
    readOk.value ||
    webOk.value ||
    sessionOk.value ||
    delegateOk.value ||
    ["terminal", "execute_code", "patch", "browser_navigate", "text_to_speech"].includes(
      name.value,
    ) ||
    oneLine.includes(name.value) ||
    untrustedTools.includes(name.value) ||
    imageTools.includes(name.value),
)
</script>

<template>
  <div v-if="name !== 'todo_list'" class="tool-result">
    <TerminalView v-if="name === 'terminal'" :command="str(a.command)" :text="text" />
    <template v-else-if="name === 'execute_code'">
      <pre class="code-block">{{ str(a.code) }}</pre>
      <TerminalView command="python" :text="text" />
    </template>
    <DiffView v-else-if="name === 'patch'" :text="text" />
    <div v-else-if="oneLine.includes(name)" class="result">
      <div class="result-h">
        <span class="path">{{ firstLine }}</span>
      </div>
    </div>
    <details v-else-if="untrustedTools.includes(name)" class="result untrusted">
      <summary class="result-h">
        <span>⚠ Untrusted content</span>
        <span v-if="str(a.url)" class="path">{{ str(a.url) }}</span>
      </summary>
      <div class="result-b md" v-html="html"></div>
    </details>
    <div v-else-if="name === 'browser_navigate'" class="result">
      <div class="result-h">
        <span class="path">{{ nav.title }}</span>
        <span class="grow"></span>
        <span v-if="nav.status" class="c-3">{{ nav.status }}</span>
      </div>
      <div class="result-b">
        <p class="t-12 c-3">{{ nav.url }}</p>
        <pre class="code-block">{{ nav.excerpt }}</pre>
      </div>
    </div>
    <CodeView v-else-if="readOk" :path="str(a.path)" :text="text" />
    <SearchList v-else-if="webOk" kind="web" :text="text" :query="str(a.query)" />
    <SearchList v-else-if="sessionOk" kind="session" :text="text" :query="str(a.query)" />
    <DelegateCards v-else-if="delegateOk" :text="text" :args="props.tool.args" />
    <template v-else-if="imageTools.includes(name)">
      <img v-if="link" class="shot" :src="link" alt="" />
      <pre v-else class="code-block">{{ firstLine }}</pre>
    </template>
    <template v-else-if="name === 'text_to_speech'">
      <audio v-if="link" class="audio" controls :src="link"></audio>
      <pre v-else class="code-block">{{ firstLine }}</pre>
    </template>
    <pre v-if="!known" class="code-block">{{ text }}</pre>
    <p v-if="props.tool.cut" class="t-12 c-3">
      The text is cut at 16 KB.
      <button type="button" class="link" data-act="show-all" @click="emit('show-all', props.tool)">
        Show all
      </button>
    </p>
  </div>
</template>

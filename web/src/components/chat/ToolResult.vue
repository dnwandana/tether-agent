<script setup lang="ts">
import { computed } from "vue"
import { TriangleAlertIcon } from "@lucide/vue"
import type { ToolView } from "../../api/types.gen"
import { Button } from "@/components/ui/button"
import {
  CodeBlock,
  CodeBlockContent,
  CodeBlockFooter,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
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
  <div v-if="name !== 'todo_list'" class="flex min-w-0 flex-col gap-2">
    <TerminalView v-if="name === 'terminal'" :command="str(a.command)" :text="text" />
    <template v-else-if="name === 'execute_code'">
      <CodeBlock size="sm">
        <CodeBlockContent wrap>{{ str(a.code) }}</CodeBlockContent>
      </CodeBlock>
      <TerminalView command="python" :text="text" />
    </template>
    <DiffView v-else-if="name === 'patch'" :text="text" />
    <CodeBlock v-else-if="oneLine.includes(name)" size="sm">
      <CodeBlockContent wrap>{{ firstLine }}</CodeBlockContent>
    </CodeBlock>
    <div
      v-else-if="untrustedTools.includes(name)"
      data-untrusted
      class="rounded-md border border-dashed border-warning"
    >
      <Collapsible>
        <CollapsibleTrigger as-child>
          <Button variant="ghost" size="sm" class="w-full min-w-0 justify-start">
            <TriangleAlertIcon class="text-warning" /><span>Untrusted content</span>
            <span v-if="str(a.url)" class="truncate font-mono text-xs">{{ str(a.url) }}</span>
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
          <div class="typeset typeset-chat p-3" v-html="html"></div>
        </CollapsibleContent>
      </Collapsible>
    </div>
    <CodeBlock v-else-if="name === 'browser_navigate'" size="sm">
      <CodeBlockHeader>
        <CodeBlockTitle>{{ nav.title }}</CodeBlockTitle>
        <span v-if="nav.status" class="ms-auto text-xs">{{ nav.status }}</span>
      </CodeBlockHeader>
      <CodeBlockContent wrap>{{ nav.excerpt }}</CodeBlockContent>
      <CodeBlockFooter>{{ nav.url }}</CodeBlockFooter>
    </CodeBlock>
    <CodeView v-else-if="readOk" :path="str(a.path)" :text="text" />
    <SearchList v-else-if="webOk" kind="web" :text="text" :query="str(a.query)" />
    <SearchList v-else-if="sessionOk" kind="session" :text="text" :query="str(a.query)" />
    <DelegateCards v-else-if="delegateOk" :text="text" :args="props.tool.args" />
    <template v-else-if="imageTools.includes(name)">
      <img v-if="link" class="w-full rounded-md border" :src="link" alt="" />
      <CodeBlock v-else size="sm">
        <CodeBlockContent wrap>{{ firstLine }}</CodeBlockContent>
      </CodeBlock>
    </template>
    <template v-else-if="name === 'text_to_speech'">
      <audio v-if="link" controls :src="link"></audio>
      <CodeBlock v-else size="sm">
        <CodeBlockContent wrap>{{ firstLine }}</CodeBlockContent>
      </CodeBlock>
    </template>
    <CodeBlock v-if="!known" size="sm">
      <CodeBlockContent wrap>{{ text }}</CodeBlockContent>
    </CodeBlock>
    <p v-if="props.tool.cut" class="text-xs text-muted-foreground">
      The text is cut at 16 KB.
      <Button type="button" variant="link" size="xs" @click="emit('show-all', props.tool)">
        Show all
      </Button>
    </p>
  </div>
</template>

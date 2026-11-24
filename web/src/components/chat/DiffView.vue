<script setup lang="ts">
import { computed } from "vue"
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockHeader,
  CodeBlockLine,
  CodeBlockTitle,
} from "@/components/ui/code-block"
import { isSecure } from "../../stores/ui"
import { parseDiff } from "./parse"

const props = defineProps<{ text: string }>()

// The copy button needs a secure context (HTTPS) to be reliable.
const secure = isSecure()
const res = computed(() => parseDiff(props.text))
const col = "inline-block w-8 select-none text-right text-muted-foreground"
</script>

<template>
  <CodeBlock>
    <CodeBlockHeader>
      <span aria-hidden="true">🔧</span>
      <CodeBlockTitle>{{ res.summary || res.path }}</CodeBlockTitle>
      <span class="ms-auto text-success">+{{ res.added }}</span>
      <span class="text-destructive">−{{ res.removed }}</span>
      <CodeBlockActions v-if="secure">
        <CodeBlockCopyButton :value="props.text" />
      </CodeBlockActions>
    </CodeBlockHeader>
    <CodeBlockContent class="max-h-96">
      <template v-for="(l, i) in res.lines" :key="i">
        <CodeBlockLine v-if="l.kind === 'hunk'" variant="highlighted" data-hunk>{{
          l.text
        }}</CodeBlockLine>
        <CodeBlockLine
          v-else
          :variant="l.kind === 'add' ? 'added' : l.kind === 'del' ? 'removed' : 'default'"
        >
          <span data-col="old" :class="col">{{ l.a ?? "" }}</span>
          <span data-col="new" :class="col">{{ l.b ?? "" }}</span>
          {{ l.text }}
        </CodeBlockLine>
      </template>
    </CodeBlockContent>
  </CodeBlock>
</template>

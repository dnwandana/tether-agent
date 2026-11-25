<script setup lang="ts">
import { computed } from "vue"
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  CodeBlockFooter,
  CodeBlockHeader,
  CodeBlockTitle,
} from "@/components/ui/code-block"
import { toolIcon } from "../../lib/glyphs"
import { isSecure } from "../../stores/ui"
import { parseTerminal } from "./parse"

const props = defineProps<{ command: string; text: string }>()

// The copy button needs a secure context (HTTPS) to be reliable.
const secure = isSecure()
const res = computed(() => parseTerminal(props.text))
const failed = computed(() => res.value.exit !== null && res.value.exit !== 0)
const copyText = computed(() => res.value.background?.logPath ?? res.value.output)
</script>

<template>
  <CodeBlock size="sm" :variant="failed ? 'destructive' : 'default'">
    <CodeBlockHeader>
      <component :is="toolIcon('terminal')" class="size-4 shrink-0" />
      <CodeBlockTitle>$ {{ props.command }}</CodeBlockTitle>
      <CodeBlockActions v-if="secure">
        <CodeBlockCopyButton :value="copyText" />
      </CodeBlockActions>
    </CodeBlockHeader>
    <CodeBlockContent wrap class="max-h-96">{{ copyText }}</CodeBlockContent>
    <CodeBlockFooter>
      <span v-for="(n, i) in res.notes" :key="i" class="text-muted-foreground">{{ n }}</span>
      <span v-if="res.background">background · PID {{ res.background.pid }}</span>
      <span v-else-if="res.exit === 0">exit 0</span>
      <span v-else-if="res.exit !== null">exit {{ res.exit }}</span>
    </CodeBlockFooter>
  </CodeBlock>
</template>

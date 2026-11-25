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
import { isSecure } from "../../stores/ui"
import { parseReadFile, unwrapUntrusted } from "./parse"
import { toolIcon } from "../../lib/glyphs"

const props = defineProps<{ path: string; text: string }>()

// The copy button needs a secure context (HTTPS) to be reliable.
const secure = isSecure()
const res = computed(() => parseReadFile(props.text))
const untrusted = computed(() => unwrapUntrusted(props.text).untrusted)
const source = computed(() => res.value.lines.join("\n"))
const last = computed(() => res.value.start + res.value.lines.length - 1)
</script>

<template>
  <CodeBlock
    :data-untrusted="untrusted || undefined"
    :class="untrusted ? 'border-dashed border-warning' : undefined"
  >
    <CodeBlockHeader>
      <component :is="toolIcon('read_file')" class="size-4 shrink-0" />
      <CodeBlockTitle>{{ path }}</CodeBlockTitle>
      <span v-if="res.lines.length" class="ms-auto shrink-0 text-xs"
        >lines {{ res.start }}–{{ last }}</span
      >
      <CodeBlockActions v-if="secure">
        <CodeBlockCopyButton :value="source" />
      </CodeBlockActions>
    </CodeBlockHeader>
    <CodeBlockContent
      v-if="res.lines.length"
      line-numbers
      :start-line="res.start"
      class="max-h-96"
      >{{ source }}</CodeBlockContent
    >
    <CodeBlockFooter v-if="res.next !== null || res.note">
      <p v-if="res.next !== null">More lines follow from line {{ res.next }}.</p>
      <p v-if="res.note">{{ res.note }}</p>
    </CodeBlockFooter>
  </CodeBlock>
</template>

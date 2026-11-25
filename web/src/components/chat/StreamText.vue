<script setup lang="ts">
import { computed } from "vue"
import { MessageContent } from "@/components/ui/message"
import { renderMarkdown } from "../../lib/markdown"

const props = defineProps<{ text: string; running: boolean }>()

// The output of renderMarkdown escapes raw HTML, so v-html is safe here.
const html = computed(() => renderMarkdown(props.text))
</script>

<template>
  <MessageContent>
    <!-- eslint-disable-next-line vue/no-v-html -- renderMarkdown escapes raw HTML -->
    <div class="typeset typeset-chat" v-html="html" />
    <span
      v-if="props.running"
      data-cursor
      aria-hidden="true"
      class="inline-block h-[1em] w-[0.5em] translate-y-[0.15em] animate-pulse bg-foreground motion-reduce:animate-none"
    />
  </MessageContent>
</template>

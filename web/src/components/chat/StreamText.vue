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
    <span v-if="props.running" aria-hidden="true" class="animate-pulse motion-reduce:animate-none"
      >▉</span
    >
  </MessageContent>
</template>

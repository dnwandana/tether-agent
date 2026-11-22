<script setup lang="ts">
import { computed } from "vue"
import { renderMarkdown } from "../../lib/markdown"

const props = defineProps<{ text: string; running: boolean }>()

// The output of renderMarkdown escapes raw HTML, so v-html is safe here.
const html = computed(() => renderMarkdown(props.text))
</script>

<template>
  <div class="answer">
    <div v-html="html" />
    <span v-if="props.running" class="cursor" aria-hidden="true">▉</span>
  </div>
</template>

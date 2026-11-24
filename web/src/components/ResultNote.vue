<script setup lang="ts">
import { computed } from "vue"
import type { ResultNoteData } from "../lib/command"
import { renderMarkdown } from "../lib/markdown"

const props = defineProps<{ note: ResultNoteData | null }>()
const emit = defineEmits<{ close: [] }>()

const time = computed(() => {
  const at = props.note?.at
  if (!at) return ""
  return `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`
})
</script>

<template>
  <div
    v-if="note"
    class="rnote"
    :class="`rnote-${note.kind}`"
    :role="note.kind === 'error' ? 'alert' : 'status'"
  >
    <div class="rnote-h">
      <span class="rnote-t">{{ note.title }}</span>
      <span class="rnote-at">{{ time }}</span>
      <button type="button" class="btn btn-ghost btn-sm" aria-label="Close" @click="emit('close')">
        ✕
      </button>
    </div>
    <div class="rnote-b md" v-html="renderMarkdown(note.text)"></div>
  </div>
</template>

<style scoped>
.rnote {
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-left-width: 3px;
  border-radius: var(--r-md);
  background: var(--surface-2);
}
.rnote-info {
  border-left-color: var(--info);
}
.rnote-warn {
  border-left-color: var(--warning);
}
.rnote-error {
  border-left-color: var(--danger);
}
.rnote-h {
  display: flex;
  align-items: center;
  gap: 8px;
}
.rnote-t {
  font-weight: 600;
  overflow-wrap: anywhere;
}
.rnote-at {
  margin-left: auto;
  color: var(--text-3);
  font-size: var(--fs-12);
}
.rnote-b {
  margin-top: 6px;
  overflow-wrap: anywhere;
}
</style>

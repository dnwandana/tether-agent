<script setup lang="ts">
import { ref, watch } from "vue"
import { useCommand } from "../lib/command"
import { renderMarkdown } from "../lib/markdown"
import { closeToast, holdToast, releaseToast, toasts } from "../stores/ui"
import type { ToastAction } from "../stores/ui"

const expanded = ref(false)

watch(
  () => toasts.length,
  (n) => {
    if (n <= 3) expanded.value = false
  },
)

const { run } = useCommand()

function runAction(id: number, a: ToastAction): void {
  closeToast(id)
  void run(a.data)
}
</script>

<template>
  <div class="toasts">
    <div
      v-for="t in expanded || toasts.length <= 3 ? toasts : toasts.slice(0, 2)"
      :key="t.id"
      class="toast"
      :class="`toast-${t.kind}`"
      :role="t.kind === 'error' ? 'alert' : 'status'"
      @mouseenter="holdToast(t.id)"
      @mouseleave="releaseToast(t.id)"
      @focusin="holdToast(t.id)"
      @focusout="releaseToast(t.id)"
    >
      <span v-if="t.kind === 'ok'" class="t-ok" aria-hidden="true">✓</span>
      <span v-else-if="t.kind === 'warn'" class="t-warn" aria-hidden="true">⚠</span>
      <span v-else-if="t.kind === 'error'" class="t-err" aria-hidden="true">⚠</span>
      <!-- The output of renderMarkdown escapes raw HTML, so v-html is safe here. -->
      <div v-if="t.markdown" class="toast-text md" v-html="renderMarkdown(t.text)" />
      <span v-else class="toast-text">{{ t.text }}</span>
      <button
        v-for="(a, i) in t.actions"
        :key="i"
        type="button"
        class="btn btn-ghost btn-sm"
        @click="runAction(t.id, a)"
      >
        {{ a.label }}
      </button>
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        aria-label="Close"
        @click="closeToast(t.id)"
      >
        ✕
      </button>
    </div>
    <button
      v-if="!expanded && toasts.length > 3"
      type="button"
      class="toast toast-more"
      @click="expanded = true"
    >
      +{{ toasts.length - 2 }} more
    </button>
  </div>
</template>

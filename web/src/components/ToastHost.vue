<script setup lang="ts">
import { toasts } from "../stores/ui"
</script>

<template>
  <div class="toasts" aria-live="polite">
    <div v-for="t in toasts" :key="t.id" class="toast" role="status">
      <span v-if="t.kind === 'ok'" class="t-ok">✓</span>
      <span v-else-if="t.kind === 'error'">⚠</span>
      <span>{{ t.text }}</span>
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        aria-label="Close"
        @click="toasts.splice(0)"
      >
        ✕
      </button>
    </div>
  </div>
</template>

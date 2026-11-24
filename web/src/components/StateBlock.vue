<script setup lang="ts">
defineProps<{
  state: "loading" | "empty" | "error"
  title?: string
  text?: string
  hint?: string
  action?: string
}>()
const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div
    v-if="state === 'loading'"
    class="table-wrap"
    role="status"
    aria-busy="true"
    aria-label="Loading"
  >
    <div v-for="n in 5" :key="n" class="skel-row">
      <span class="skel" style="width: 24px" />
      <span class="skel" style="flex: 1" />
      <span class="skel" style="width: 15%" />
      <span class="skel" style="width: 10%" />
    </div>
  </div>
  <div v-else-if="state === 'empty'" class="state">
    <span class="ico" aria-hidden="true">
      <svg
        viewBox="0 0 24 24"
        width="20"
        height="20"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
      >
        <path d="M4 7h16M4 12h16M4 17h10" />
      </svg>
    </span>
    <p class="state-t">{{ title ?? text }}</p>
    <p v-if="title && text">{{ text }}</p>
    <p v-if="hint" class="state-hint">{{ hint }}</p>
    <button v-if="action" class="btn btn-secondary" type="button" @click="emit('action')">
      {{ action }}
    </button>
  </div>
  <div v-else class="state state-error" role="alert">
    <span class="ico">!</span>
    <p class="state-t">{{ title ?? "Could not load" }}</p>
    <span class="cause">{{ text }}</span>
    <p v-if="hint" class="state-hint">{{ hint }}</p>
    <button class="btn btn-secondary" type="button" @click="emit('action')">
      {{ action ?? "Retry" }}
    </button>
  </div>
</template>

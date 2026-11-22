<script setup lang="ts">
import { computed } from "vue"
import { useRoute } from "vue-router"
import { ICON } from "../lib/icons"
import { cards } from "../stores/cards"

// The nav section of a route: a child page marks its parent.
const PARENT: Record<string, string> = {
  transcript: "sessions",
  job: "jobs",
  goal: "goals",
  skill: "skills",
  ops: "system",
}

const PAGES: { name: string; label: string }[] = [
  { name: "chat", label: "Chat" },
  { name: "sessions", label: "Sessions" },
  { name: "jobs", label: "Jobs" },
  { name: "goals", label: "Goals" },
  { name: "security", label: "Approvals and security" },
  { name: "skills", label: "Skills" },
  { name: "memory", label: "Memory" },
  { name: "usage", label: "Usage" },
  { name: "models", label: "Models" },
  { name: "system", label: "System" },
  { name: "access", label: "Access" },
]

const route = useRoute()
const section = computed(() => {
  const n = String(route.name ?? "")
  return PARENT[n] ?? n
})

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
}
</script>

<template>
  <nav class="rail" aria-label="Pages">
    <div class="rail-brand">
      <span class="rail-mark">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.75"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path :d="ICON.mark" />
        </svg>
      </span>
      <span class="bt">Tether</span>
    </div>
    <button type="button" class="rail-search" @click="openPalette">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path :d="ICON.search" />
      </svg>
      <span class="grow">Search</span>
      <span class="kbd">Ctrl K</span>
    </button>
    <RouterLink
      v-for="p in PAGES"
      :key="p.name"
      :to="{ name: p.name }"
      class="rail-item"
      :class="{ 'is-on': section === p.name }"
      :aria-current="section === p.name ? 'page' : undefined"
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path :d="ICON[p.name]" />
      </svg>
      <span class="truncate">{{ p.label }}</span>
      <span v-if="p.name === 'security' && cards.open.length" class="badge badge-bad">{{
        cards.open.length
      }}</span>
    </RouterLink>
    <div class="rail-foot">
      <RouterLink
        :to="{ name: 'start' }"
        class="rail-item"
        :class="{ 'is-on': section === 'start' }"
        :aria-current="section === 'start' ? 'page' : undefined"
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="1.75"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path :d="ICON.start" />
        </svg>
        <span class="truncate">Getting started</span>
      </RouterLink>
    </div>
  </nav>
</template>

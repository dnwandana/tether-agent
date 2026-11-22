<script setup lang="ts">
import { computed, ref } from "vue"
import { useRoute } from "vue-router"
import { ICON } from "../lib/icons"
import { cards } from "../stores/cards"
import { setTheme } from "../stores/ui"
import WaitingTray from "./WaitingTray.vue"

// The nav section of a route: a child page marks its parent.
const PARENT: Record<string, string> = {
  transcript: "sessions",
  job: "jobs",
  goal: "goals",
  skill: "skills",
  ops: "system",
}

// The pages that the tab bar does not show.
const MORE: { name: string; label: string }[] = [
  { name: "sessions", label: "Sessions" },
  { name: "goals", label: "Goals" },
  { name: "security", label: "Approvals and security" },
  { name: "skills", label: "Skills" },
  { name: "memory", label: "Memory" },
  { name: "usage", label: "Usage" },
  { name: "models", label: "Models" },
  { name: "system", label: "System" },
  { name: "access", label: "Access" },
  { name: "start", label: "Getting started" },
]

const route = useRoute()
const section = computed(() => {
  const n = String(route.name ?? "")
  return PARENT[n] ?? n
})
const trayOpen = ref(false)
const sheetOpen = ref(false)

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  sheetOpen.value = false
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
}

// Switches between the light and the dark theme.
function toggleTheme(): void {
  setTheme(document.documentElement.classList.contains("theme-light") ? "dark" : "light")
  sheetOpen.value = false
}
</script>

<template>
  <nav class="tabbar" aria-label="Main">
    <RouterLink :to="{ name: 'chat' }" class="tab" :class="{ 'is-on': section === 'chat' }">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path :d="ICON.chat" />
      </svg>
      <span>Chat</span>
    </RouterLink>
    <RouterLink :to="{ name: 'jobs' }" class="tab" :class="{ 'is-on': section === 'jobs' }">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path :d="ICON.jobs" />
      </svg>
      <span>Jobs</span>
    </RouterLink>
    <button type="button" class="tab" @click="trayOpen = true">
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path :d="ICON.security" />
      </svg>
      <span>Approvals</span>
      <span v-if="cards.open.length" class="badge badge-bad">{{ cards.open.length }}</span>
    </button>
    <button
      type="button"
      class="tab"
      :class="{ 'is-on': section !== 'chat' && section !== 'jobs' }"
      @click="sheetOpen = true"
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
        <path :d="ICON.more" />
      </svg>
      <span>More</span>
    </button>
    <div v-if="sheetOpen" class="ov scrim is-open" @click.self="sheetOpen = false">
      <div class="dialog" role="dialog" aria-label="More">
        <div class="grabber"></div>
        <div class="list">
          <RouterLink
            v-for="p in MORE"
            :key="p.name"
            :to="{ name: p.name }"
            class="li"
            @click="sheetOpen = false"
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
            {{ p.label }}
          </RouterLink>
          <button type="button" class="li" @click="openPalette">
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
            Commands and skills
          </button>
          <button type="button" class="li" @click="toggleTheme">
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="1.75"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path :d="ICON.moon" />
            </svg>
            Switch the theme
          </button>
        </div>
      </div>
    </div>
    <WaitingTray :open="trayOpen" @close="trayOpen = false" />
  </nav>
</template>

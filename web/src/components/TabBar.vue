<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute } from "vue-router"
import { ICON } from "../lib/icons"
import { useOverlay } from "../lib/overlay"
import { cards } from "../stores/cards"
import { prefs, setTheme, ui, type Theme } from "../stores/ui"

// The nav section of a route: a child page marks its parent.
const PARENT: Record<string, string> = {
  transcript: "sessions",
  job: "jobs",
  goal: "goals",
  skill: "skills",
  ops: "system",
}

// The pages that the tab bar does not show, in groups with an eyebrow label.
const GROUPS: { label: string; pages: { name: string; label: string }[] }[] = [
  {
    label: "Work",
    pages: [
      { name: "sessions", label: "Sessions" },
      { name: "goals", label: "Goals" },
    ],
  },
  {
    label: "Agent",
    pages: [
      { name: "security", label: "Approvals and security" },
      { name: "skills", label: "Skills" },
      { name: "memory", label: "Memory" },
      { name: "models", label: "Models" },
    ],
  },
  {
    label: "Host",
    pages: [
      { name: "usage", label: "Usage" },
      { name: "system", label: "System" },
      { name: "ops", label: "System operations" },
      { name: "access", label: "Access" },
      { name: "start", label: "Getting started" },
    ],
  },
]

const THEMES: { value: Theme; label: string; icon: string }[] = [
  { value: "system", label: "System", icon: "monitor" },
  { value: "dark", label: "Dark", icon: "moon" },
  { value: "light", label: "Light", icon: "sun" },
]

const route = useRoute()
const section = computed(() => {
  const n = String(route.name ?? "")
  return PARENT[n] ?? n
})
// The current page for the sheet: the route name for ops, else the section.
const current = computed(() => (route.name === "ops" ? "ops" : section.value))
const sheetOpen = ref(false)
const themeOpen = ref(false)
const sheetEl = ref<HTMLElement | null>(null)

useOverlay(sheetOpen, sheetEl, { onClose: () => (sheetOpen.value = false), modal: true })
watch(sheetOpen, (on) => {
  if (!on) themeOpen.value = false
})

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  sheetOpen.value = false
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
}

// Sets the theme and closes the sheet.
function chooseTheme(t: Theme): void {
  setTheme(t)
  sheetOpen.value = false
}

function themeLabel(): string {
  return THEMES.find((t) => t.value === prefs.theme)?.label ?? "System"
}
</script>

<template>
  <nav class="tabbar" aria-label="Main">
    <RouterLink
      :to="{ name: 'chat' }"
      class="tab"
      :class="{ 'is-on': section === 'chat' && !ui.trayOpen }"
      :aria-current="section === 'chat' ? 'page' : undefined"
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
        <path :d="ICON.chat" />
      </svg>
      <span>Chat</span>
    </RouterLink>
    <RouterLink
      :to="{ name: 'jobs' }"
      class="tab"
      :class="{ 'is-on': section === 'jobs' && !ui.trayOpen }"
      :aria-current="section === 'jobs' ? 'page' : undefined"
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
        <path :d="ICON.jobs" />
      </svg>
      <span>Jobs</span>
    </RouterLink>
    <button
      type="button"
      class="tab"
      :class="{ 'is-on': ui.trayOpen }"
      :aria-expanded="ui.trayOpen"
      @click="ui.trayOpen = true"
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
        <path :d="ICON.waiting" />
      </svg>
      <span>Waiting</span>
      <span v-if="cards.open.length" class="badge badge-bad">{{ cards.open.length }}</span>
    </button>
    <button
      type="button"
      class="tab"
      :class="{ 'is-on': section !== 'chat' && section !== 'jobs' && !ui.trayOpen }"
      :aria-current="section !== 'chat' && section !== 'jobs' ? 'page' : undefined"
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
      <div ref="sheetEl" class="dialog" role="dialog" aria-label="More">
        <div class="grabber"></div>
        <div class="list">
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
          <template v-for="g in GROUPS" :key="g.label">
            <div class="eyebrow">{{ g.label }}</div>
            <RouterLink
              v-for="p in g.pages"
              :key="p.name"
              :to="{ name: p.name }"
              class="li"
              :class="{ 'is-on': current === p.name }"
              :aria-current="current === p.name ? 'page' : undefined"
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
          </template>
          <button type="button" class="li" @click="themeOpen = !themeOpen">
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="1.75"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path :d="ICON.theme" />
            </svg>
            Theme: {{ themeLabel() }} ▸
          </button>
          <template v-if="themeOpen">
            <button
              v-for="t in THEMES"
              :key="t.value"
              type="button"
              class="li theme-choice"
              :aria-pressed="prefs.theme === t.value"
              @click="chooseTheme(t.value)"
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
                <path :d="ICON[t.icon]" />
              </svg>
              <span class="grow">{{ t.label }}</span>
              <span v-if="prefs.theme === t.value" aria-hidden="true">✓</span>
            </button>
          </template>
        </div>
      </div>
    </div>
  </nav>
</template>

<style scoped>
.tt .li.is-on {
  background: var(--nav-on, var(--border));
  color: var(--text);
  font-weight: 600;
}
</style>

<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useRoute } from "vue-router"
import type { Component } from "vue"
import { CheckIcon, ChevronDownIcon, ChevronRightIcon } from "@lucide/vue"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { ICONS, type IconName } from "@/lib/icons"
import { guardOutside } from "@/lib/layer"
import { cn } from "@/lib/utils"
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

const THEMES: { value: Theme; label: string; icon: Component }[] = [
  { value: "system", label: "System", icon: ICONS.monitor },
  { value: "dark", label: "Dark", icon: ICONS.moon },
  { value: "light", label: "Light", icon: ICONS.sun },
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

// The classes of a tab of the bar. The data-active attribute marks the tab that is on.
const TAB =
  "text-muted-foreground data-[active]:text-foreground relative flex min-h-14 flex-col items-center justify-center gap-0.5 rounded-md text-xs font-medium outline-none focus-visible:ring-3 focus-visible:ring-ring/50 data-[active]:font-semibold [&_svg:not([class*='size-'])]:size-5"
// The classes of a row of the More sheet.
const ROW =
  "min-h-11 w-full justify-start gap-3 px-4 text-base data-[active]:bg-accent data-[active]:text-accent-foreground data-[active]:font-semibold"

function themeLabel(): string {
  return THEMES.find((t) => t.value === prefs.theme)?.label ?? "System"
}
</script>

<template>
  <nav
    class="bg-background grid shrink-0 grid-cols-4 gap-1 border-t px-2 pt-1 pb-[env(safe-area-inset-bottom)] tab:hidden"
    aria-label="Main"
  >
    <RouterLink
      :to="{ name: 'chat' }"
      :class="TAB"
      :data-active="section === 'chat' && !ui.trayOpen ? '' : undefined"
      :aria-current="section === 'chat' ? 'page' : undefined"
    >
      <component :is="ICONS.chat" aria-hidden="true" />
      <span>Chat</span>
    </RouterLink>
    <RouterLink
      :to="{ name: 'jobs' }"
      :class="TAB"
      :data-active="section === 'jobs' && !ui.trayOpen ? '' : undefined"
      :aria-current="section === 'jobs' ? 'page' : undefined"
    >
      <component :is="ICONS.jobs" aria-hidden="true" />
      <span>Jobs</span>
    </RouterLink>
    <button
      type="button"
      :class="TAB"
      :data-active="ui.trayOpen ? '' : undefined"
      :aria-expanded="ui.trayOpen"
      @click="ui.trayOpen = true"
    >
      <component :is="ICONS.waiting" aria-hidden="true" />
      <span>Waiting</span>
      <Badge v-if="cards.open.length" variant="destructive" class="absolute top-1 right-1/4">
        {{ cards.open.length }}
      </Badge>
    </button>
    <button
      type="button"
      :class="TAB"
      :data-active="section !== 'chat' && section !== 'jobs' && !ui.trayOpen ? '' : undefined"
      :aria-current="section !== 'chat' && section !== 'jobs' ? 'page' : undefined"
      @click="sheetOpen = true"
    >
      <component :is="ICONS.more" aria-hidden="true" />
      <span>More</span>
    </button>
    <Sheet v-model:open="sheetOpen">
      <SheetContent
        side="bottom"
        :aria-describedby="undefined"
        class="max-h-[85dvh] pb-[env(safe-area-inset-bottom)]"
        @pointer-down-outside="guardOutside"
        @interact-outside="guardOutside"
      >
        <SheetHeader>
          <SheetTitle class="sr-only">More</SheetTitle>
        </SheetHeader>
        <div
          data-slot="more-body"
          class="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-2 pb-2"
        >
          <Button variant="ghost" data-slot="more-item" :class="ROW" @click="openPalette">
            <component :is="ICONS.search" aria-hidden="true" />
            Commands and skills
          </Button>
          <template v-for="g in GROUPS" :key="g.label">
            <div class="text-muted-foreground px-4 pt-2 text-xs font-medium uppercase">
              {{ g.label }}
            </div>
            <RouterLink
              v-for="p in g.pages"
              :key="p.name"
              :to="{ name: p.name }"
              data-slot="more-item"
              :class="cn(buttonVariants({ variant: 'ghost' }), ROW)"
              :data-active="current === p.name ? '' : undefined"
              :aria-current="current === p.name ? 'page' : undefined"
              @click="sheetOpen = false"
            >
              <component :is="ICONS[p.name as IconName]" aria-hidden="true" />
              {{ p.label }}
            </RouterLink>
          </template>
          <Button
            variant="ghost"
            data-slot="more-item"
            :class="ROW"
            :aria-expanded="themeOpen"
            @click="themeOpen = !themeOpen"
          >
            <component :is="ICONS.theme" aria-hidden="true" />
            Theme: {{ themeLabel() }}
            <component :is="themeOpen ? ChevronDownIcon : ChevronRightIcon" aria-hidden="true" />
          </Button>
          <template v-if="themeOpen">
            <Button
              v-for="t in THEMES"
              :key="t.value"
              variant="ghost"
              data-slot="more-item"
              :class="ROW"
              :aria-pressed="prefs.theme === t.value"
              @click="chooseTheme(t.value)"
            >
              <component :is="t.icon" aria-hidden="true" />
              <span class="flex-1 text-left">{{ t.label }}</span>
              <span v-if="prefs.theme === t.value" class="flex items-center"
                ><CheckIcon aria-hidden="true" /><span class="sr-only">current</span></span
              >
            </Button>
          </template>
        </div>
      </SheetContent>
    </Sheet>
  </nav>
</template>

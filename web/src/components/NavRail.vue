<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import { useRoute } from "vue-router"
import { ICONS, type IconName } from "@/lib/icons"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Kbd } from "@/components/ui/kbd"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cards } from "../stores/cards"
import BrandMark from "./BrandMark.vue"

// The nav section of a route: a child page marks its parent.
const PARENT: Record<string, string> = {
  transcript: "sessions",
  job: "jobs",
  goal: "goals",
  skill: "skills",
  ops: "system",
}

const PAGES: { name: IconName; label: string }[] = [
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

// On a tablet the name is hidden text, so the name uses sr-only there.
const media = globalThis.matchMedia?.("(max-width: 1023px)")
const tablet = ref(media?.matches ?? false)
function onMedia(e: MediaQueryListEvent): void {
  tablet.value = e.matches
}
onMounted(() => media?.addEventListener("change", onMedia))
onBeforeUnmount(() => media?.removeEventListener("change", onMedia))

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
}
</script>

<template>
  <TooltipProvider :delay-duration="0">
    <nav
      class="bg-card text-card-foreground flex h-full w-full flex-col gap-1 p-2"
      aria-label="Pages"
    >
      <div class="flex items-center gap-2 px-2 py-2">
        <BrandMark />
        <span class="font-semibold tracking-tight max-lg:hidden">Tether</span>
      </div>
      <Tooltip :disabled="!tablet">
        <TooltipTrigger as-child>
          <Button variant="outline" class="justify-start gap-2" @click="openPalette">
            <component :is="ICONS.search" />
            <span :class="tablet ? 'sr-only' : 'grow text-left'">Search</span>
            <Kbd class="max-lg:hidden">Ctrl K</Kbd>
          </Button>
        </TooltipTrigger>
        <TooltipContent side="right">Search (Ctrl K)</TooltipContent>
      </Tooltip>
      <Tooltip v-for="p in PAGES" :key="p.name" :disabled="!tablet">
        <TooltipTrigger as-child>
          <RouterLink
            :to="{ name: p.name }"
            :class="
              cn(
                'hover:bg-accent hover:text-accent-foreground flex items-center gap-2 rounded-md px-3 py-2 text-sm',
                section === p.name && 'bg-accent text-accent-foreground',
              )
            "
            :aria-current="section === p.name ? 'page' : undefined"
          >
            <component :is="ICONS[p.name]" />
            <span :class="tablet ? 'sr-only' : 'truncate'">{{ p.label }}</span>
            <Badge v-if="p.name === 'security' && cards.open.length" variant="destructive">{{
              cards.open.length
            }}</Badge>
          </RouterLink>
        </TooltipTrigger>
        <TooltipContent side="right">{{ p.label }}</TooltipContent>
      </Tooltip>
      <div class="mt-auto"></div>
      <Tooltip :disabled="!tablet">
        <TooltipTrigger as-child>
          <RouterLink
            :to="{ name: 'start' }"
            :class="
              cn(
                'hover:bg-accent hover:text-accent-foreground flex items-center gap-2 rounded-md px-3 py-2 text-sm',
                section === 'start' && 'bg-accent text-accent-foreground',
              )
            "
            :aria-current="section === 'start' ? 'page' : undefined"
          >
            <component :is="ICONS.start" />
            <span :class="tablet ? 'sr-only' : 'truncate'">Getting started</span>
          </RouterLink>
        </TooltipTrigger>
        <TooltipContent side="right">Getting started</TooltipContent>
      </Tooltip>
    </nav>
  </TooltipProvider>
</template>

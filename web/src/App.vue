<template>
  <StatusBar v-if="shell" />
  <RestartBanner v-if="shell" />
  <div class="shell">
    <div v-if="shell" class="rail-slot"><NavRail /></div>
    <main class="view">
      <RouterView v-if="ownScroll" />
      <div v-else class="scroll"><RouterView /></div>
    </main>
    <div class="aside-slot" />
  </div>
  <TabBar v-if="shell" />
  <WaitingTray v-if="shell" />
  <CommandPalette :open="paletteOpen" @close="paletteOpen = false" />
  <ConfirmDialog />
  <ToastHost />
  <div class="announce sr" aria-live="polite" role="status"></div>
</template>

<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, provide, ref, watch } from "vue"
import RestartBanner from "./components/RestartBanner.vue"
import WaitingTray from "./components/WaitingTray.vue"
import { cards } from "./stores/cards"
import { announce } from "./lib/announce"
import type { Card } from "./api/types.gen"

// Read the global $route through the instance, so a mocked route works in tests.
// The instance proxy is read at use time, because a test can replace it after setup.
const instance = getCurrentInstance()!
const shell = computed(
  () => (instance.proxy as unknown as { $route?: { name?: unknown } }).$route?.name !== "signin",
)
const ownScroll = computed(
  () =>
    (instance.proxy as unknown as { $route?: { meta?: { ownScroll?: boolean } } }).$route?.meta
      ?.ownScroll === true,
)
// SidePanel teleports into the .aside-slot of the shell when this is provided.
provide("asideSlot", true)
const paletteOpen = ref(false)

// Returns the announce text of a card, or "" for a kind with no text (sign-in).
function cardText(c: Card): string {
  switch (c.kind) {
    case "approval":
    case "pin":
      return `Approval needed: ${c.command || c.summary}`
    case "clarify":
      return `Question: ${c.summary}`
    case "secret":
      return `Secret needed: ${c.secret_name}`
    default:
      return ""
  }
}

// One live region tells a screen reader about each card once, when the card id is new.
const announced = new Set<string>()
watch(
  () => cards.open.map((c) => c.id),
  () => {
    const texts: string[] = []
    for (const c of cards.open) {
      if (announced.has(c.id)) continue
      announced.add(c.id)
      const t = cardText(c)
      if (t) texts.push(t)
    }
    if (texts.length) announce(texts.join(". "))
  },
)

const textField = "input, textarea, select, [contenteditable]"

function onKey(e: KeyboardEvent): void {
  const ctrlK = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k"
  const target = e.target instanceof Element ? e.target : null
  const slash =
    e.key === "/" && !e.ctrlKey && !e.metaKey && !e.altKey && !target?.closest(textField)
  if (ctrlK || slash) {
    paletteOpen.value = true
    e.preventDefault()
  }
}

onMounted(() => document.addEventListener("keydown", onKey))
onBeforeUnmount(() => document.removeEventListener("keydown", onKey))
</script>

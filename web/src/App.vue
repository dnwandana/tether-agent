<template>
  <StatusBar v-if="shell" />
  <div class="shell">
    <div v-if="shell" class="rail-slot"><NavRail /></div>
    <main class="view"><RouterView /></main>
  </div>
  <TabBar v-if="shell" />
  <CommandPalette :open="paletteOpen" @close="paletteOpen = false" />
  <ConfirmDialog />
  <ToastHost />
</template>

<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref } from "vue"

// Read the global $route through the instance, so a mocked route works in tests.
// The instance proxy is read at use time, because a test can replace it after setup.
const instance = getCurrentInstance()!
const shell = computed(
  () => (instance.proxy as unknown as { $route?: { name?: unknown } }).$route?.name !== "signin",
)
const paletteOpen = ref(false)

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

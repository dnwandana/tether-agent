<script lang="ts">
// Ids of the cards that already raised an alert. One alert for each card, even with two bars.
const alerted = new Set<string>()
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, watchEffect } from "vue"
import { useRouter } from "vue-router"
import type { Card } from "../api/types.gen"
import { ICON } from "../lib/icons"
import { cards } from "../stores/cards"
import { status } from "../stores/status"
import { isSecure, prefs, setTheme } from "../stores/ui"
import WaitingTray from "./WaitingTray.vue"

const router = useRouter()
const trayOpen = ref(false)

const MODES: Record<string, string> = {
  "read-only": "👁 Read-only",
  guarded: "🛡 Guarded",
  full: "⚡ Full",
}
const modeText = computed(() => MODES[status.mode] ?? status.mode)
const waiting = computed(() => cards.open.length)
const spend = computed(() => "$" + status.spend24h.toFixed(4) + (status.unpriced ? "+" : ""))
// The resolved system theme. A change listener keeps it current, because matchMedia is not reactive.
const lightMq = globalThis.matchMedia?.("(prefers-color-scheme: light)")
const systemLight = ref(lightMq?.matches === true)
function onSystemTheme(): void {
  systemLight.value = lightMq?.matches === true
}
const isDark = computed(
  () => !prefs.theme || prefs.theme === "dark" || (prefs.theme === "system" && !systemLight.value),
)

// Goes to the page with the route name.
function go(name: string): void {
  void router.push({ name })
}

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
}

// Switches between the light and the dark theme.
function toggleTheme(): void {
  setTheme(document.documentElement.classList.contains("theme-light") ? "dark" : "light")
}

// Plays a short tone and shows a notification for a new card in a hidden tab.
function alertCard(card: Card): void {
  if (prefs.sound) {
    try {
      const ctx = new globalThis.AudioContext()
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.frequency.value = 880
      gain.gain.value = 0.04
      osc.connect(gain)
      gain.connect(ctx.destination)
      // Close the context when the tone ends. A browser limits the number of open contexts.
      osc.addEventListener("ended", () => {
        void ctx.close().catch(() => {})
      })
      osc.start()
      osc.stop(ctx.currentTime + 0.12)
    } catch {
      // A browser can block the tone.
    }
  }
  if (isSecure() && globalThis.Notification?.permission === "granted") {
    try {
      // oxlint-disable-next-line no-new -- the notification shows itself, and the page keeps no handle.
      new globalThis.Notification("Tether: " + card.header, {
        body: card.command || card.summary || "A card waits for you.",
      })
    } catch {
      // A browser can block the notification.
    }
  }
}

watchEffect(() => {
  document.title = (waiting.value ? `(${waiting.value}) ` : "") + "Tether Agent Dashboard"
})

watch(
  () => cards.open.map((c) => c.id),
  (_ids, old) => {
    if (!document.hidden) return
    const seen = new Set(old ?? [])
    for (const c of cards.open) {
      if (seen.has(c.id) || alerted.has(c.id)) continue
      alerted.add(c.id)
      alertCard(c)
    }
  },
)

function askPermission(): void {
  if (isSecure() && globalThis.Notification?.permission === "default") {
    try {
      void globalThis.Notification.requestPermission()
    } catch {
      // A browser can block the request.
    }
  }
}
onMounted(() => {
  document.addEventListener("pointerdown", askPermission, { once: true })
  lightMq?.addEventListener?.("change", onSystemTheme)
})
onBeforeUnmount(() => {
  document.removeEventListener("pointerdown", askPermission)
  lightMq?.removeEventListener?.("change", onSystemTheme)
})
</script>

<template>
  <header class="statusbar">
    <button
      v-if="status.fallback"
      type="button"
      class="sb-item sb-warn"
      data-act="models"
      title="A fallback entry answers"
      @click="go('models')"
    >
      <span>⚠</span><b>{{ status.model }}</b
      ><span class="sb-hide">fallback answers · {{ status.fallback_cause }}</span>
    </button>
    <button
      v-else
      type="button"
      class="sb-item"
      data-act="models"
      title="Model of this session"
      @click="go('models')"
    >
      <span class="dot dot-ok"></span><b>{{ status.model }}</b
      ><span class="c-3">{{ status.entry }}</span>
    </button>
    <span class="sb-sep"></span>
    <button
      type="button"
      class="sb-item"
      :class="{ 'sb-full': status.mode === 'full' }"
      data-act="security"
      title="Mode of the dashboard scope"
      @click="go('security')"
    >
      {{ modeText }}<span class="c-3 sb-hide">· Dashboard</span>
    </button>
    <span class="sb-sep hide-phone"></span>
    <button
      type="button"
      class="sb-item hide-phone"
      data-act="tray"
      title="Cards that wait for you"
      @click="trayOpen = true"
    >
      Waiting<span class="badge" :class="{ 'badge-bad': waiting }">{{ waiting }}</span>
    </button>
    <span class="sb-sep"></span>
    <button
      type="button"
      class="sb-item"
      data-act="system"
      title="Background work"
      @click="go('system')"
    >
      <span v-if="status.running > 0" class="spin"></span><b>{{ status.running }} running</b>
    </button>
    <span class="sb-sep"></span>
    <button type="button" class="sb-item" data-act="usage" @click="go('usage')">
      Spend 24 h <b class="mono">{{ spend }}</b>
    </button>
    <button
      v-if="status.stream === 'reconnecting'"
      type="button"
      class="sb-item sb-warn"
      title="The page lost the event stream"
    >
      Reconnecting…
    </button>
    <span class="sb-spacer"></span>
    <button type="button" class="sb-item hide-phone" data-act="palette" @click="openPalette">
      Commands <span class="kbd">Ctrl</span><span class="kbd">K</span>
    </button>
    <button
      type="button"
      class="sb-item"
      title="Theme"
      aria-label="Switch the theme"
      @click="toggleTheme"
    >
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.75"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
        style="width: 16px; height: 16px"
      >
        <path :d="isDark ? ICON.sun : ICON.moon" />
      </svg>
    </button>
    <WaitingTray :open="trayOpen" @close="trayOpen = false" />
  </header>
</template>

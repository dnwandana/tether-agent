<script lang="ts">
// Ids of the cards that already raised an alert. One alert for each card, even with two bars.
const alerted = new Set<string>()
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, watchEffect } from "vue"
import { useRouter } from "vue-router"
import type { Card } from "../api/types.gen"
import { retryNow } from "../api/stream"
import { useOverlay } from "../lib/overlay"
import { cards } from "../stores/cards"
import { status } from "../stores/status"
import { isSecure, prefs, setTheme, ui, type Theme } from "../stores/ui"

const router = useRouter()

const MODES: Record<string, string> = {
  "read-only": "👁 Read-only",
  guarded: "🛡 Guarded",
  full: "⚡ Full",
}
const modeText = computed(() => MODES[status.mode] ?? status.mode)
const waiting = computed(() => cards.open.length)
const spend = computed(() => "$" + status.spend24h.toFixed(2) + (status.unpriced ? "+" : ""))
// The resolved system theme. A change listener keeps it current, because matchMedia is not reactive.
const lightMq = globalThis.matchMedia?.("(prefers-color-scheme: light)")
const systemLight = ref(lightMq?.matches === true)
function onSystemTheme(): void {
  systemLight.value = lightMq?.matches === true
}

const runningList = computed(() => {
  const parts: string[] = []
  if (status.running_batches > 0) parts.push(`🔀 ${status.running_batches}`)
  if (status.running_acp > 0) parts.push(`🤖 ${status.running_acp}`)
  if (status.running_shell > 0) parts.push(`💻 ${status.running_shell}`)
  if (status.browser_on) parts.push("🌐")
  return parts.join(" · ")
})

// The offline time as m:ss. The timer runs only while the stream is "reconnecting".
const now = ref(Date.now())
let tick: ReturnType<typeof setInterval> | null = null
function stopTick(): void {
  if (tick) clearInterval(tick)
  tick = null
}
watch(
  () => status.stream,
  (s) => {
    stopTick()
    if (s === "reconnecting") {
      now.value = Date.now()
      tick = setInterval(() => (now.value = Date.now()), 1000)
    }
  },
  { immediate: true },
)
const offlineFor = computed(() => {
  const sec = Math.max(0, Math.floor((now.value - (status.offlineSince ?? now.value)) / 1000))
  return `${Math.floor(sec / 60)}:${String(sec % 60).padStart(2, "0")}`
})

const menuOpen = ref(false)
const menuEl = ref<HTMLElement | null>(null)
useOverlay(menuOpen, menuEl, { onClose: () => (menuOpen.value = false) })
const canAskNotify = ref(isSecure() && globalThis.Notification?.permission === "default")

// Sets the theme and closes the menu.
function pickTheme(t: Theme): void {
  setTheme(t)
  menuOpen.value = false
}

// Asks for the notification permission. Only the menu row calls it.
function askPermission(): void {
  menuOpen.value = false
  canAskNotify.value = false
  try {
    void globalThis.Notification.requestPermission()
  } catch {
    // A browser can block the request.
  }
}

// A click outside the data-pop elements closes the menu.
function onDocClick(e: Event): void {
  if (!menuOpen.value) return
  const t = e.target as Element | null
  if (t?.closest?.("[data-pop]")) return
  menuOpen.value = false
}

// Goes to the page with the route name.
function go(name: string): void {
  void router.push({ name })
}

// Sends Ctrl+K to document, so App.vue opens the palette.
function openPalette(): void {
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }))
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

onMounted(() => {
  document.addEventListener("click", onDocClick)
  lightMq?.addEventListener?.("change", onSystemTheme)
})
onBeforeUnmount(() => {
  document.removeEventListener("click", onDocClick)
  stopTick()
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
      @click="ui.trayOpen = true"
    >
      Waiting<span v-if="waiting > 0" class="badge badge-bad">{{ waiting }}</span>
    </button>
    <span class="sb-sep"></span>
    <button
      type="button"
      class="sb-item"
      data-act="system"
      title="Background work"
      @click="go('system')"
    >
      <span v-if="status.running > 0" class="spin"></span><b>{{ status.running }} running</b
      ><span v-if="runningList" class="sb-hide c-3">· {{ runningList }}</span>
    </button>
    <span class="sb-sep"></span>
    <button type="button" class="sb-item" data-act="usage" @click="go('usage')">
      Spend 24 h <b class="mono">{{ spend }}</b>
    </button>
    <button
      v-if="status.stream === 'reconnecting'"
      type="button"
      class="sb-item sb-warn"
      data-act="offline"
      title="The page lost the event stream"
      @click="retryNow"
    >
      Offline {{ offlineFor }} · Try now
    </button>
    <span class="sb-spacer"></span>
    <button type="button" class="sb-item hide-phone" data-act="palette" @click="openPalette">
      Commands <span class="kbd">Ctrl</span><span class="kbd">K</span>
    </button>
    <span class="sb-menu-wrap">
      <button
        type="button"
        class="sb-item"
        aria-label="Theme and alerts"
        title="Theme and alerts"
        :aria-expanded="menuOpen"
        data-pop
        @click="menuOpen = !menuOpen"
      >
        🎨
      </button>
      <div v-if="menuOpen" ref="menuEl" class="menu sb-menu" role="menu" data-pop>
        <button
          type="button"
          class="menu-item"
          role="menuitemradio"
          :aria-checked="prefs.theme === 'system'"
          @click="pickTheme('system')"
        >
          {{ prefs.theme === "system" ? "✓ " : "" }}System (now
          {{ systemLight ? "light" : "dark" }})
        </button>
        <button
          type="button"
          class="menu-item"
          role="menuitemradio"
          :aria-checked="prefs.theme === 'dark'"
          @click="pickTheme('dark')"
        >
          {{ prefs.theme === "dark" ? "✓ " : "" }}🌙 Dark
        </button>
        <button
          type="button"
          class="menu-item"
          role="menuitemradio"
          :aria-checked="prefs.theme === 'light'"
          @click="pickTheme('light')"
        >
          {{ prefs.theme === "light" ? "✓ " : "" }}☀️ Light
        </button>
        <button type="button" class="menu-item" role="menuitem" @click="prefs.sound = !prefs.sound">
          🔔 Sound on a new card: {{ prefs.sound ? "On" : "Off" }}
        </button>
        <button
          v-if="canAskNotify"
          type="button"
          class="menu-item"
          role="menuitem"
          @click="askPermission"
        >
          Turn on notifications…
        </button>
      </div>
    </span>
  </header>
</template>

<style scoped>
.sb-menu-wrap {
  position: relative;
  display: inline-flex;
}
.sb-menu {
  position: absolute;
  right: 0;
  top: 100%;
  z-index: 50;
}
</style>

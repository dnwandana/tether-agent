<script lang="ts">
// Ids of the cards that already raised an alert. One alert for each card, even with two bars.
const alerted = new Set<string>()
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, watchEffect, type Component } from "vue"
import { useRouter } from "vue-router"
import type { Card } from "../api/types.gen"
import { retryNow } from "../api/stream"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Kbd } from "@/components/ui/kbd"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import {
  BotIcon,
  GitBranchIcon,
  GlobeIcon,
  SquareTerminalIcon,
  TriangleAlertIcon,
} from "@lucide/vue"
import { cn } from "@/lib/utils"
import { GLYPH_TONE_CLASS, MODES, splitGlyph } from "../lib/glyphs"
import { ICONS } from "../lib/icons"
import { cards } from "../stores/cards"
import { status } from "../stores/status"
import { isSecure, prefs, setTheme, ui, type Theme } from "../stores/ui"

const router = useRouter()

// The rows of the theme menu, with the icon component of each row.
const THEME_ROWS: { value: Theme; label: string; icon: Component }[] = [
  { value: "system", label: "System", icon: ICONS.monitor },
  { value: "dark", label: "Dark", icon: ICONS.moon },
  { value: "light", label: "Light", icon: ICONS.sun },
]
const mode = computed<{ label: string; icon: Component | null }>(
  () => MODES[status.mode] ?? { label: status.mode, icon: null },
)
const waiting = computed(() => cards.open.length)
const spend = computed(() => "$" + status.spend24h.toFixed(2) + (status.unpriced ? "+" : ""))
// The full spend text. A phone shows only the amount, so the label keeps the full text.
const spendLabel = computed(() => "Spend 24 h " + spend.value)
// The resolved system theme. A change listener keeps it current, because matchMedia is not reactive.
const lightMq = globalThis.matchMedia?.("(prefers-color-scheme: light)")
const systemLight = ref(lightMq?.matches === true)
function onSystemTheme(): void {
  systemLight.value = lightMq?.matches === true
}

const runningList = computed(() => {
  const parts: { icon: Component; n: string; word: string }[] = []
  if (status.running_batches > 0)
    parts.push({
      icon: GitBranchIcon,
      n: String(status.running_batches),
      word: "delegation batches",
    })
  if (status.running_acp > 0)
    parts.push({ icon: BotIcon, n: String(status.running_acp), word: "ACP sessions" })
  if (status.running_shell > 0)
    parts.push({
      icon: SquareTerminalIcon,
      n: String(status.running_shell),
      word: "background processes",
    })
  if (status.browser_on) parts.push({ icon: GlobeIcon, n: "", word: "browser" })
  return parts
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

const canAskNotify = ref(isSecure() && globalThis.Notification?.permission === "default")

// Asks for the notification permission. Only the menu row calls it.
function askPermission(): void {
  canAskNotify.value = false
  try {
    void globalThis.Notification.requestPermission()
  } catch {
    // A browser can block the request.
  }
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
      new globalThis.Notification("Tether: " + splitGlyph(card.header).rest, {
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
  lightMq?.addEventListener?.("change", onSystemTheme)
})
onBeforeUnmount(() => {
  stopTick()
  lightMq?.removeEventListener?.("change", onSystemTheme)
})
</script>

<template>
  <header class="bg-background flex h-10 w-full items-center gap-1 border-b px-2 text-sm">
    <div data-part="scroll" class="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto">
      <Button
        v-if="status.fallback"
        variant="ghost"
        size="sm"
        class="text-warning"
        data-act="models"
        data-tone="warn"
        title="A fallback entry answers"
        @click="go('models')"
      >
        <TriangleAlertIcon :class="GLYPH_TONE_CLASS.warn" /><span class="sr-only">fallback</span
        ><b>{{ status.model }}</b
        ><span class="max-lg:hidden">fallback answers · {{ status.fallback_cause }}</span>
      </Button>
      <Button
        v-else
        variant="ghost"
        size="sm"
        data-act="models"
        title="Model of this session"
        @click="go('models')"
      >
        <span class="bg-success size-2 rounded-full"></span><b>{{ status.model }}</b
        ><span class="text-muted-foreground max-tab:hidden">{{ status.entry }}</span>
      </Button>
      <Separator orientation="vertical" class="h-4" />
      <Button
        variant="ghost"
        size="sm"
        :class="cn(status.mode === 'full' && 'text-warning')"
        :data-tone="status.mode === 'full' ? 'warn' : undefined"
        data-act="security"
        title="Mode of the dashboard scope"
        @click="go('security')"
      >
        <component :is="mode.icon" v-if="mode.icon" />{{ mode.label
        }}<span class="text-muted-foreground max-lg:hidden">· Dashboard</span>
      </Button>
      <Separator orientation="vertical" class="h-4 max-tab:hidden" />
      <Button
        variant="ghost"
        size="sm"
        class="max-tab:hidden"
        data-act="tray"
        data-waiting-anchor
        title="Cards that wait for you"
        @click="ui.trayOpen = true"
      >
        Waiting<Badge v-if="waiting > 0" variant="destructive">{{ waiting }}</Badge>
      </Button>
      <Separator orientation="vertical" class="h-4" />
      <Button
        variant="ghost"
        size="sm"
        data-act="system"
        title="Background work"
        @click="go('system')"
      >
        <Spinner v-if="status.running > 0" /><b>{{ status.running }} running</b
        ><span
          v-if="runningList.length"
          class="text-muted-foreground inline-flex items-center gap-1 max-lg:hidden"
          >·
          <template v-for="(p, i) in runningList" :key="p.word">
            <span v-if="i > 0">·</span>
            <span class="inline-flex items-center gap-0.5"
              ><component :is="p.icon" />{{ p.n }}<span class="sr-only">{{ p.word }}</span></span
            >
          </template>
        </span>
      </Button>
      <Separator orientation="vertical" class="h-4" />
      <Button
        variant="ghost"
        size="sm"
        data-act="usage"
        :title="spendLabel"
        :aria-label="spendLabel"
        @click="go('usage')"
      >
        <span class="max-tab:hidden">Spend 24 h</span> <b class="font-mono">{{ spend }}</b>
      </Button>
    </div>
    <div data-part="fixed" class="flex shrink-0 items-center gap-1">
      <Button
        v-if="status.stream === 'reconnecting'"
        variant="ghost"
        size="sm"
        class="text-warning"
        data-act="offline"
        data-tone="warn"
        title="The page lost the event stream"
        @click="retryNow"
      >
        Offline {{ offlineFor }} · Try now
      </Button>
      <Button
        variant="ghost"
        size="sm"
        class="max-tab:hidden"
        data-act="palette"
        @click="openPalette"
      >
        Commands <Kbd>Ctrl</Kbd><Kbd>K</Kbd>
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Theme and alerts"
            title="Theme and alerts"
          >
            <component :is="ICONS.theme" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuRadioGroup
            :model-value="prefs.theme"
            @update:model-value="(v) => setTheme(v as Theme)"
          >
            <DropdownMenuRadioItem v-for="row in THEME_ROWS" :key="row.value" :value="row.value">
              <component :is="row.icon" />
              {{
                row.value === "system"
                  ? `System (now ${systemLight ? "light" : "dark"})`
                  : row.label
              }}
            </DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem @select="prefs.sound = !prefs.sound">
              <component :is="ICONS.bell" />
              Sound on a new card: {{ prefs.sound ? "On" : "Off" }}
            </DropdownMenuItem>
            <DropdownMenuItem v-if="canAskNotify" @select="askPermission">
              Turn on notifications…
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  </header>
</template>

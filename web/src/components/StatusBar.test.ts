import { flushPromises, mount } from "@vue/test-utils"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { createMemoryHistory, createRouter } from "vue-router"

const names = [
  "chat",
  "sessions",
  "transcript",
  "jobs",
  "job",
  "goals",
  "goal",
  "security",
  "skills",
  "skill",
  "memory",
  "usage",
  "models",
  "system",
  "ops",
  "access",
  "start",
  "signin",
]
const paths: Record<string, string> = {
  transcript: "/sessions/:id",
  job: "/jobs/:id",
  goal: "/goals/:session",
  skill: "/skills/:name",
  ops: "/system/ops",
}
function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: names.map((name) => ({
      name,
      path: paths[name] ?? "/" + name,
      component: { render: () => null },
    })),
  })
}

vi.mock("../api/client", () => ({ api: vi.fn(), ApiError: class extends Error {} }))
vi.mock("../api/stream", () => ({ retryNow: vi.fn() }))
import { retryNow } from "../api/stream"
import StatusBar from "./StatusBar.vue"
import { status } from "../stores/status"
import { cards } from "../stores/cards"
import { prefs, setTheme, ui } from "../stores/ui"

const card = (id: string) => ({
  id,
  kind: "approval",
  header: "🛡 Approval needed",
  command: "rm -rf build/",
  summary: "",
  human_only: false,
  answers: ["deny", "once"],
})

async function mountBar() {
  const router = makeRouter()
  router.push("/chat")
  await router.isReady()
  return {
    w: mount(StatusBar, { global: { plugins: [router], stubs: { WaitingTray: true } } }),
    router,
  }
}

beforeEach(() => {
  ui.trayOpen = false
  Object.assign(status, {
    model: "glm-4.6",
    entry: "primary",
    fallback: false,
    fallback_cause: "",
    mode: "guarded",
    waiting: 0,
    running: 2,
    running_batches: 0,
    running_acp: 0,
    running_shell: 0,
    browser_on: false,
    offlineSince: null,
    spend24h: 1.8421,
    unpriced: false,
    tls: true,
    stream: "open",
  })
  cards.open = [card("a1"), card("a2"), card("a3")] as never
  prefs.sound = true
})
afterEach(() => {
  vi.unstubAllGlobals()
  Object.defineProperty(document, "hidden", { configurable: true, get: () => false })
})

describe("StatusBar", () => {
  it("shows the model, the mode, the waiting count, the running count, and the spend", async () => {
    const { w } = await mountBar()
    const t = w.text()
    expect(t).toContain("glm-4.6")
    expect(t).toContain("primary")
    expect(t).toContain("🛡 Guarded")
    expect(t).toContain("· Dashboard")
    expect(t).toContain("2 running")
    expect(t).toContain("$1.84")
    expect(w.find(".badge").text()).toBe("3")
    expect(document.title).toBe("(3) Tether Agent Dashboard")
  })

  it("warns on a fallback, on Full mode, on an unknown price, and on a lost stream", async () => {
    Object.assign(status, {
      fallback: true,
      fallback_cause: "HTTP 503",
      model: "deepseek-chat",
      mode: "full",
      unpriced: true,
      stream: "reconnecting",
    })
    cards.open = []
    const { w } = await mountBar()
    expect(w.find(".sb-warn").text()).toContain("deepseek-chat")
    expect(w.find(".sb-warn").text()).toContain("fallback answers · HTTP 503")
    expect(w.find(".sb-full").text()).toContain("⚡ Full")
    expect(w.text()).toContain("$1.84+")
    expect(w.find('[data-act="offline"]').exists()).toBe(true)
    expect(document.title).toBe("Tether Agent Dashboard")
  })

  it("opens the tray and goes to the pages", async () => {
    const { w, router } = await mountBar()
    await w.find('[data-act="tray"]').trigger("click")
    expect(ui.trayOpen).toBe(true)
    expect(w.findComponent({ name: "WaitingTray" }).exists()).toBe(false)
    await w.find('[data-act="usage"]').trigger("click")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("usage")
  })

  it("opens the palette with Ctrl+K", async () => {
    const seen: KeyboardEvent[] = []
    const f = (e: Event) => seen.push(e as KeyboardEvent)
    document.addEventListener("keydown", f)
    const { w } = await mountBar()
    await w.find('[data-act="palette"]').trigger("click")
    document.removeEventListener("keydown", f)
    expect(seen.some((e) => e.key === "k" && e.ctrlKey)).toBe(true)
  })

  it("hides the waiting badge at 0 and opens the tray", async () => {
    cards.open = []
    const { w } = await mountBar()
    expect(w.find('[data-act="tray"] .badge').exists()).toBe(false)
    await w.find('[data-act="tray"]').trigger("click")
    expect(ui.trayOpen).toBe(true)
  })

  it("lists the running work by kind", async () => {
    Object.assign(status, {
      running_batches: 1,
      running_acp: 1,
      running_shell: 2,
      browser_on: true,
    })
    const { w } = await mountBar()
    expect(w.find('[data-act="system"]').text()).toContain("🔀 1 · 🤖 1 · 💻 2 · 🌐")
    expect(w.find('[data-act="system"] .sb-hide').exists()).toBe(true)
    Object.assign(status, {
      running_batches: 0,
      running_acp: 0,
      running_shell: 0,
      browser_on: false,
    })
    await flushPromises()
    expect(w.find('[data-act="system"] .sb-hide').exists()).toBe(false)
  })

  it("sets the theme from the 🎨 menu", async () => {
    setTheme("dark")
    const { w } = await mountBar()
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    const items = () => w.findAll(".menu .menu-item").map((b) => b.text())
    expect(items()).toEqual([
      "System (now dark)",
      "✓ 🌙 Dark",
      "☀️ Light",
      "🔔 Sound on a new card: On",
    ])
    await w.findAll(".menu .menu-item")[2].trigger("click")
    expect(prefs.theme).toBe("light")
    expect(document.documentElement.classList.contains("theme-light")).toBe(true)
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    await w.findAll(".menu .menu-item")[0].trigger("click")
    expect(prefs.theme).toBe("system")
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    expect(items()[0]).toBe("✓ System (now dark)")
    await w.findAll(".menu .menu-item")[3].trigger("click")
    expect(prefs.sound).toBe(false)
  })

  it("asks for notifications only from the menu", async () => {
    const ask = vi.fn(async () => "granted")
    vi.stubGlobal(
      "Notification",
      Object.assign(function () {}, { permission: "default", requestPermission: ask }),
    )
    vi.stubGlobal("isSecureContext", true)
    const { w } = await mountBar()
    document.dispatchEvent(new Event("pointerdown"))
    expect(ask).not.toHaveBeenCalled()
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    const turnOn = w.findAll(".menu .menu-item").find((b) => b.text() === "Turn on notifications…")!
    await turnOn.trigger("click")
    expect(ask).toHaveBeenCalledTimes(1)
  })

  it("shows no notification item on plain HTTP", async () => {
    vi.stubGlobal(
      "Notification",
      Object.assign(function () {}, { permission: "default", requestPermission: vi.fn() }),
    )
    vi.stubGlobal("isSecureContext", false)
    const { w } = await mountBar()
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    expect(w.text()).not.toContain("Turn on notifications…")
  })

  it("shows the offline time and retries now", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 10, 23, 9, 1, 5))
    Object.assign(status, {
      stream: "reconnecting",
      offlineSince: new Date(2026, 10, 23, 9, 0, 0).getTime(),
    })
    const { w } = await mountBar()
    const chip = () => w.find('[data-act="offline"]')
    expect(chip().text()).toBe("Offline 1:05 · Try now")
    await vi.advanceTimersByTimeAsync(1000)
    expect(chip().text()).toBe("Offline 1:06 · Try now")
    await chip().trigger("click")
    expect(retryNow).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it("shows no offline chip while the first connect runs", async () => {
    Object.assign(status, { stream: "connecting", offlineSince: null })
    const { w } = await mountBar()
    expect(w.find('[data-act="offline"]').exists()).toBe(false)
  })

  it("alerts on a new card while the tab is hidden", async () => {
    const tone = vi.fn()
    vi.stubGlobal(
      "AudioContext",
      class {
        currentTime = 0
        destination = {}
        constructor() {
          tone()
        }
        createOscillator() {
          return {
            frequency: { value: 0 },
            connect: () => {},
            start: () => {},
            stop: () => {},
            addEventListener: () => {},
          }
        }
        createGain() {
          return { gain: { value: 0 }, connect: () => {} }
        }
      },
    )
    const shown = vi.fn()
    vi.stubGlobal(
      "Notification",
      Object.assign(
        function (this: unknown, title: string, o: unknown) {
          shown(title, o)
        },
        { permission: "granted", requestPermission: vi.fn() },
      ),
    )
    vi.stubGlobal("isSecureContext", true)
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true })
    await mountBar()
    cards.open = [...cards.open, card("a4")] as never
    await flushPromises()
    expect(tone).toHaveBeenCalledTimes(1)
    expect(shown).toHaveBeenCalledWith("Tether: 🛡 Approval needed", { body: "rm -rf build/" })
  })
  it("closes the audio context after the tone", async () => {
    const closed = vi.fn()
    let onended: (() => void) | null = null
    vi.stubGlobal(
      "AudioContext",
      class {
        currentTime = 0
        destination = {}
        close() {
          closed()
          return Promise.resolve()
        }
        createOscillator() {
          const o = {
            frequency: { value: 0 },
            connect: () => {},
            start: () => {},
            stop: () => {},
            addEventListener: (_: string, f: () => void) => {
              onended = f
            },
          }
          return o
        }
        createGain() {
          return { gain: { value: 0 }, connect: () => {} }
        }
      },
    )
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true })
    await mountBar()
    cards.open = [...cards.open, card("a5")] as never
    await flushPromises()
    expect(closed).not.toHaveBeenCalled()
    onended!()
    expect(closed).toHaveBeenCalledTimes(1)
  })

  it("follows the system theme in the menu and removes the listener", async () => {
    prefs.theme = "system"
    let cb: (() => void) | null = null
    const mql = {
      matches: false,
      addEventListener: vi.fn((_: string, f: () => void) => (cb = f)),
      removeEventListener: vi.fn(),
    }
    vi.stubGlobal("matchMedia", () => mql)
    const { w } = await mountBar()
    await w.find('[aria-label="Theme and alerts"]').trigger("click")
    expect(w.findAll(".menu .menu-item")[0].text()).toBe("✓ System (now dark)")
    mql.matches = true
    cb!()
    await flushPromises()
    expect(w.findAll(".menu .menu-item")[0].text()).toBe("✓ System (now light)")
    w.unmount()
    expect(mql.removeEventListener).toHaveBeenCalled()
  })
})

import { flushPromises } from "@vue/test-utils"
import { render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import type { Component } from "vue"
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
import { ICONS } from "../lib/icons"

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
  const r = render(StatusBar, { global: { plugins: [router], stubs: { WaitingTray: true } } })
  return { ...r, router }
}
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const act = (name: string) => document.querySelector<HTMLElement>(`[data-act="${name}"]`)
const themeButton = () => screen.getByRole("button", { name: "Theme and alerts" })
// Returns the class of the SVG that an icon component draws.
const iconClass = (c: Component) =>
  render(c).container.querySelector("svg")?.getAttribute("class") ?? ""
const radios = () => screen.getAllByRole("menuitemradio")
const system = () => radios()[0]
const svgClass = (el: Element) => el.querySelector("svg")?.getAttribute("class") ?? ""

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
    await mountBar()
    const t = screen.getByRole("banner").textContent ?? ""
    expect(t).toContain("glm-4.6")
    expect(t).toContain("primary")
    expect(t).toContain("🛡 Guarded")
    expect(t).toContain("· Dashboard")
    expect(t).toContain("2 running")
    expect(t).toContain("$1.84")
    expect(within(act("tray")!).getByText("3")).toBeTruthy()
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
    await mountBar()
    expect(act("models")!.dataset.tone).toBe("warn")
    expect(act("models")!.textContent).toContain("deepseek-chat")
    expect(act("models")!.textContent).toContain("fallback answers · HTTP 503")
    expect(act("security")!.dataset.tone).toBe("warn")
    expect(act("security")!.textContent).toContain("⚡ Full")
    expect(screen.getByRole("banner").textContent).toContain("$1.84+")
    expect(act("offline")).not.toBeNull()
    expect(document.title).toBe("Tether Agent Dashboard")
  })

  it("keeps the offline item, Commands, and the theme menu out of the scroll box", async () => {
    Object.assign(status, { stream: "reconnecting", offlineSince: Date.now() })
    await mountBar()
    const bar = screen.getByRole("banner")
    expect([...bar.children].map((e) => e.getAttribute("data-part"))).toEqual(["scroll", "fixed"])
    const scroll = bar.querySelector<HTMLElement>('[data-part="scroll"]')!
    expect(
      [...scroll.querySelectorAll("[data-act]")].map((b) => b.getAttribute("data-act")),
    ).toEqual(["models", "security", "tray", "system", "usage"])
    const fixed = bar.querySelector<HTMLElement>('[data-part="fixed"]')!
    expect(
      [...fixed.children].map((e) => e.getAttribute("data-act") ?? e.getAttribute("aria-label")),
    ).toEqual(["offline", "palette", "Theme and alerts"])
    expect(within(scroll).queryByRole("button", { name: "Theme and alerts" })).toBeNull()
  })

  it("has the short spend form and hides the entry on a phone only", async () => {
    Object.assign(status, { unpriced: true })
    await mountBar()
    const spend = act("usage")!
    expect(spend.getAttribute("aria-label")).toBe("Spend 24 h $1.84+")
    expect(spend.getAttribute("title")).toBe("Spend 24 h $1.84+")
    expect(within(spend).getByText("Spend 24 h").className).toContain("max-tab:hidden")
    expect(spend.querySelector("b")!.textContent).toBe("$1.84+")
    expect(spend.querySelector("b")!.className).toContain("font-mono")
    const model = act("models")!
    expect(within(model).getByText("glm-4.6").tagName).toBe("B")
    const entry = within(model).getByText("primary")
    expect(entry.className).toContain("max-tab:hidden")
    expect(entry.className).not.toContain("max-lg:hidden")
    expect(act("palette")!.className).toContain("max-tab:hidden")
  })

  it("marks the Waiting button as the anchor of the tray", async () => {
    await mountBar()
    expect(act("tray")!.hasAttribute("data-waiting-anchor")).toBe(true)
    expect(document.querySelectorAll("[data-waiting-anchor]")).toHaveLength(1)
  })

  it("opens the tray and goes to the pages", async () => {
    const user = setup()
    const { router } = await mountBar()
    await user.click(act("tray")!)
    expect(ui.trayOpen).toBe(true)
    expect(document.querySelector("waiting-tray-stub")).toBeNull()
    await user.click(act("usage")!)
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("usage")
  })

  it("opens the palette with Ctrl+K", async () => {
    const user = setup()
    const seen: KeyboardEvent[] = []
    const f = (e: Event) => seen.push(e as KeyboardEvent)
    document.addEventListener("keydown", f)
    await mountBar()
    await user.click(act("palette")!)
    document.removeEventListener("keydown", f)
    expect(seen.some((e) => e.key === "k" && e.ctrlKey)).toBe(true)
  })

  it("hides the waiting badge at 0 and opens the tray", async () => {
    const user = setup()
    cards.open = []
    await mountBar()
    expect(act("tray")!.querySelector('[data-slot="badge"]')).toBeNull()
    await user.click(act("tray")!)
    expect(ui.trayOpen).toBe(true)
  })

  it("lists the running work by kind", async () => {
    Object.assign(status, {
      running_batches: 1,
      running_acp: 1,
      running_shell: 2,
      browser_on: true,
    })
    await mountBar()
    expect(act("system")!.textContent).toContain("🔀 1 · 🤖 1 · 💻 2 · 🌐")
    expect(act("system")!.querySelector(".max-lg\\:hidden")).not.toBeNull()
    Object.assign(status, {
      running_batches: 0,
      running_acp: 0,
      running_shell: 0,
      browser_on: false,
    })
    await flushPromises()
    expect(act("system")!.querySelector(".max-lg\\:hidden")).toBeNull()
  })

  it("sets the theme from the theme menu", async () => {
    const user = setup()
    setTheme("dark")
    await mountBar()
    expect(svgClass(themeButton())).toBe(iconClass(ICONS.theme))
    expect(themeButton().textContent?.trim()).toBe("")
    await user.click(themeButton())
    expect(radios().map((r) => r.textContent?.trim())).toEqual([
      "System (now dark)",
      "Dark",
      "Light",
    ])
    const sound = screen.getByRole("menuitem", { name: "Sound on a new card: On" })
    // A checked radio item also holds the indicator SVG, so look for the icon by its class.
    const rows = [...radios(), sound]
    const icons = [ICONS.monitor, ICONS.moon, ICONS.sun, ICONS.bell].map(iconClass)
    rows.forEach((row, i) =>
      expect([...row.querySelectorAll("svg")].map((s) => s.getAttribute("class"))).toContain(
        icons[i],
      ),
    )
    expect(screen.getByRole("menu").textContent).not.toMatch(/🎨|🌙|☀️|🔔/)
    expect(radios().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"])
    await user.click(radios()[2])
    expect(prefs.theme).toBe("light")
    expect(document.documentElement.classList.contains("light")).toBe(true)
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())
    await user.click(themeButton())
    await user.click(radios()[0])
    expect(prefs.theme).toBe("system")
    await user.click(themeButton())
    expect(radios()[0].getAttribute("aria-checked")).toBe("true")
    await user.click(screen.getByRole("menuitem", { name: "Sound on a new card: On" }))
    expect(prefs.sound).toBe(false)
  })

  it("asks for notifications only from the menu", async () => {
    const user = setup()
    const ask = vi.fn(async () => "granted")
    vi.stubGlobal(
      "Notification",
      Object.assign(function () {}, { permission: "default", requestPermission: ask }),
    )
    vi.stubGlobal("isSecureContext", true)
    await mountBar()
    document.dispatchEvent(new Event("pointerdown"))
    expect(ask).not.toHaveBeenCalled()
    await user.click(themeButton())
    await user.click(screen.getByRole("menuitem", { name: "Turn on notifications…" }))
    expect(ask).toHaveBeenCalledTimes(1)
  })

  it("shows no notification item on plain HTTP", async () => {
    const user = setup()
    vi.stubGlobal(
      "Notification",
      Object.assign(function () {}, { permission: "default", requestPermission: vi.fn() }),
    )
    vi.stubGlobal("isSecureContext", false)
    await mountBar()
    await user.click(themeButton())
    expect(screen.getByRole("menu").textContent).not.toContain("Turn on notifications…")
  })

  it("shows the offline time and retries now", async () => {
    vi.useFakeTimers()
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime, pointerEventsCheck: 0 })
    vi.setSystemTime(new Date(2026, 10, 23, 9, 1, 5))
    Object.assign(status, {
      stream: "reconnecting",
      offlineSince: new Date(2026, 10, 23, 9, 0, 0).getTime(),
    })
    await mountBar()
    expect(act("offline")!.textContent?.trim()).toBe("Offline 1:05 · Try now")
    await vi.advanceTimersByTimeAsync(1000)
    expect(act("offline")!.textContent?.trim()).toBe("Offline 1:06 · Try now")
    await user.click(act("offline")!)
    expect(retryNow).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  it("shows no offline chip while the first connect runs", async () => {
    Object.assign(status, { stream: "connecting", offlineSince: null })
    await mountBar()
    expect(act("offline")).toBeNull()
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
    const user = setup()
    prefs.theme = "system"
    let cb: (() => void) | null = null
    const mql = {
      matches: false,
      addEventListener: vi.fn((_: string, f: () => void) => (cb = f)),
      removeEventListener: vi.fn(),
    }
    vi.stubGlobal("matchMedia", () => mql)
    const { unmount } = await mountBar()
    await user.click(themeButton())
    expect(system().textContent?.trim()).toBe("System (now dark)")
    expect(system().getAttribute("aria-checked")).toBe("true")
    mql.matches = true
    cb!()
    await flushPromises()
    expect(system().textContent?.trim()).toBe("System (now light)")
    unmount()
    expect(mql.removeEventListener).toHaveBeenCalled()
  })
})

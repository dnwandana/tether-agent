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
import StatusBar from "./StatusBar.vue"
import { status } from "../stores/status"
import { cards } from "../stores/cards"
import { prefs } from "../stores/ui"

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
  Object.assign(status, {
    model: "glm-4.6",
    entry: "primary",
    fallback: false,
    fallback_cause: "",
    mode: "guarded",
    waiting: 0,
    running: 2,
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
    expect(t).toContain("$1.8421")
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
    expect(w.text()).toContain("$1.8421+")
    expect(w.text()).toContain("Reconnecting…")
    expect(document.title).toBe("Tether Agent Dashboard")
  })

  it("opens the tray and goes to the pages", async () => {
    const { w, router } = await mountBar()
    await w.find('[data-act="tray"]').trigger("click")
    expect(w.findComponent({ name: "WaitingTray" }).props("open")).toBe(true)
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

  it("switches the theme", async () => {
    document.documentElement.className = "tt theme-dark"
    const { w } = await mountBar()
    await w.find('[aria-label="Switch the theme"]').trigger("click")
    expect(document.documentElement.classList.contains("theme-light")).toBe(true)
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

  it("follows the system theme with a change listener and removes it", async () => {
    prefs.theme = "system"
    let cb: (() => void) | null = null
    const mql = {
      matches: false,
      addEventListener: vi.fn((_: string, f: () => void) => {
        cb = f
      }),
      removeEventListener: vi.fn(),
    }
    vi.stubGlobal("matchMedia", () => mql)
    const { w } = await mountBar()
    const path = () => w.find('[aria-label="Switch the theme"] path').attributes("d")
    const light = path()
    mql.matches = true
    cb!()
    await flushPromises()
    expect(path()).not.toBe(light)
    w.unmount()
    expect(mql.removeEventListener).toHaveBeenCalled()
  })
})

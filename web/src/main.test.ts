import { flushPromises } from "@vue/test-utils"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

// The test re-imports main.ts for each case. The mock factories then run again, so the
// shared functions live here and each factory returns the same objects.
const h = vi.hoisted(() => ({
  onEvents: null as null | ((b: { name: string; data: unknown }[]) => void),
  after: [] as ((to: { name: string }) => void)[],
  dirty: false,
  toast: vi.fn(),
  ui: { trayOpen: false, updateReady: false },
  app: null as null | { component: (name: string) => unknown },
}))
const blank = { default: { render: () => null } }
vi.mock("./App.vue", () => blank)
vi.mock("./components/StatusBar.vue", () => blank)
vi.mock("./components/NavRail.vue", () => blank)
vi.mock("./components/TabBar.vue", () => blank)
vi.mock("./components/CommandPalette.vue", () => blank)
vi.mock("./components/ConfirmDialog.vue", () => blank)
vi.mock("./components/AppToaster.vue", () => blank)
vi.mock("vue", async (importOriginal) => {
  const vue = await importOriginal<typeof import("vue")>()
  return {
    ...vue,
    createApp: (...a: Parameters<typeof vue.createApp>) => {
      const app = vue.createApp(...a)
      h.app = app
      return app
    },
  }
})
vi.mock("./router", () => ({
  router: {
    install: () => {},
    afterEach: (f: (to: { name: string }) => void) => h.after.push(f),
    currentRoute: { value: { name: "chat" } },
    push: vi.fn(),
  },
}))
vi.mock("./api/stream", () => ({
  openStream: (onEvents: (b: { name: string; data: unknown }[]) => void) => {
    h.onEvents = onEvents
    return () => {}
  },
  retryNow: vi.fn(),
}))
vi.mock("./api/client", () => ({
  api: vi.fn(async () => ({ version: "0.25.0" })),
  onSignedOut: vi.fn(),
  setSessionHeader: vi.fn(),
}))
vi.mock("./lib/bus", () => ({ emitEvents: vi.fn() }))
vi.mock("./lib/dirty", () => ({ anyDirty: () => h.dirty }))
vi.mock("./stores/session", () => ({ applySessionEvents: vi.fn() }))
vi.mock("./stores/turn", () => ({ applyTurnEvents: vi.fn() }))
vi.mock("./stores/cards", () => ({ applyCardEvents: vi.fn() }))
vi.mock("./stores/notices", () => ({ applyNoticeEvents: vi.fn() }))
vi.mock("./stores/status", () => ({ applyStatusEvents: vi.fn(), status: {} }))
vi.mock("./stores/ui", () => ({ applyTheme: vi.fn(), toast: h.toast, ui: h.ui }))

const toast = h.toast
const hello = (build_id: string) => [{ name: "hello", data: { build_id } }]

async function start() {
  vi.resetModules()
  h.after = []
  await import("./main")
  for (const f of h.after) f({ name: "chat" })
  h.onEvents!(hello("b1"))
  await flushPromises()
}

beforeEach(() => {
  document.body.innerHTML = '<div id="app"></div>'
  localStorage.setItem("tether.session", "h1")
  sessionStorage.clear()
  h.dirty = false
  h.ui.updateReady = false
  vi.mocked(toast).mockClear()
})
afterEach(() => (document.body.innerHTML = ""))

describe("main", () => {
  it("registers the toaster and not the old toast host", async () => {
    await start()
    expect(h.app!.component("AppToaster")).toBeDefined()
    expect(h.app!.component("ToastHost")).toBeUndefined()
  })

  it("waits with a banner while a form is dirty", async () => {
    await start()
    h.dirty = true
    h.onEvents!(hello("b2"))
    expect(h.ui.updateReady).toBe(true)
    expect(document.querySelector(".update-banner")).toBeNull()
    expect(sessionStorage.getItem("tether.updated-from")).toBeNull()
  })

  it("waits with a banner while the composer has a draft", async () => {
    await start()
    sessionStorage.setItem("tether.draft", "half a thought")
    h.onEvents!(hello("b2"))
    expect(h.ui.updateReady).toBe(true)
  })

  it("reloads at once when nothing is dirty, and keeps the old version", async () => {
    await start()
    h.onEvents!(hello("b2"))
    expect(h.ui.updateReady).toBe(false)
    expect(sessionStorage.getItem("tether.updated-from")).toBe("0.25.0")
  })

  it("says Updated to after a reload for a new build", async () => {
    sessionStorage.setItem("tether.updated-from", "0.24.0")
    await start()
    expect(toast).toHaveBeenCalledWith("Updated to v0.25.0.", "ok")
    expect(sessionStorage.getItem("tether.updated-from")).toBeNull()
  })

  it("says nothing when the version did not change", async () => {
    sessionStorage.setItem("tether.updated-from", "0.25.0")
    await start()
    expect(toast).not.toHaveBeenCalled()
  })

  it("shows the update banner on a failed preload, and stops the Vite error", async () => {
    await start()
    h.dirty = true
    const e = new Event("vite:preloadError", { cancelable: true })
    window.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
    expect(h.ui.updateReady).toBe(true)
  })
})

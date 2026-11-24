import { beforeEach, describe, expect, it, vi } from "vitest"

const h = vi.hoisted(() => ({ dirty: false }))
vi.mock("./dirty", () => ({ anyDirty: () => h.dirty }))

type Mod = typeof import("./update")
type Ui = typeof import("../stores/ui")
let upd: Mod
let store: Ui

beforeEach(async () => {
  h.dirty = false
  sessionStorage.clear()
  vi.resetModules()
  store = await import("../stores/ui")
  upd = await import("./update")
})

describe("onNewBuild", () => {
  it("starts with no update waiting", () => {
    expect(store.ui).toEqual({ trayOpen: false, updateReady: false })
  })

  it("waits with the banner while a form is dirty", () => {
    upd.setVersion("0.25.0")
    h.dirty = true
    upd.onNewBuild()
    expect(store.ui.updateReady).toBe(true)
    expect(sessionStorage.getItem("tether.updated-from")).toBeNull()
  })

  it("waits with the banner while the composer has a draft", () => {
    sessionStorage.setItem("tether.draft", "half a thought")
    upd.onNewBuild()
    expect(store.ui.updateReady).toBe(true)
  })

  it("reloads at once when nothing is dirty, and keeps the old version", () => {
    upd.setVersion("0.25.0")
    upd.onNewBuild()
    expect(store.ui.updateReady).toBe(false)
    expect(sessionStorage.getItem("tether.updated-from")).toBe("0.25.0")
  })
})

describe("reloadNow", () => {
  it("keeps no old version when the version is not known", () => {
    upd.reloadNow()
    expect(sessionStorage.getItem("tether.updated-from")).toBeNull()
  })
})

describe("setVersion", () => {
  it("says Updated to after a reload for a new build, once", () => {
    sessionStorage.setItem("tether.updated-from", "0.24.0")
    upd.setVersion("0.25.0")
    expect(store.toasts.map((t) => [t.text, t.kind])).toEqual([["Updated to v0.25.0.", "ok"]])
    expect(sessionStorage.getItem("tether.updated-from")).toBeNull()
  })

  it("adds no second v", () => {
    sessionStorage.setItem("tether.updated-from", "v0.24.0")
    upd.setVersion("v0.25.0")
    expect(store.toasts[0].text).toBe("Updated to v0.25.0.")
  })

  it("says nothing when the version did not change", () => {
    sessionStorage.setItem("tether.updated-from", "0.25.0")
    upd.setVersion("0.25.0")
    expect(store.toasts).toHaveLength(0)
  })
})

import { nextTick } from "vue"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

type Mod = typeof import("./ui")
let ui: Mod

function stubMedia(light: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q.includes("light") ? light : !light,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

beforeEach(async () => {
  vi.useFakeTimers()
  localStorage.clear()
  document.documentElement.className = "tt theme-dark"
  vi.resetModules()
  ui = await import("./ui")
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("toast", () => {
  it("keeps up to 3 toasts and drops the oldest toast that is not sticky", () => {
    ui.toast("One.")
    ui.toast("Failed.", "error")
    ui.toast("Two.", "ok")
    ui.toast("Three.")
    expect(ui.toasts.map((t) => t.text)).toEqual(["Failed.", "Two.", "Three."])
    expect(ui.toasts[0]).toMatchObject({
      kind: "error",
      sticky: true,
      markdown: false,
      actions: [],
    })
    expect(ui.toasts[1]).toMatchObject({ kind: "ok", sticky: false })
  })

  it("closes ok and info after 6 s, and keeps warn and error", () => {
    ui.toast("Saved. Applies to new sessions.")
    ui.toast("Renamed.", "ok")
    ui.toast("⏳ Busy", "warn")
    expect(ui.toasts[0].kind).toBe("info")
    vi.advanceTimersByTime(5999)
    expect(ui.toasts).toHaveLength(3)
    vi.advanceTimersByTime(1)
    expect(ui.toasts.map((t) => t.text)).toEqual(["⏳ Busy"])
    ui.closeToast(ui.toasts[0].id)
    expect(ui.toasts).toHaveLength(0)
  })

  it("keeps all sticky toasts when 3 are sticky", () => {
    for (const t of ["A", "B", "C", "D"]) ui.toast(t, "error")
    expect(ui.toasts.map((t) => t.text)).toEqual(["A", "B", "C", "D"])
  })

  it("stops the timer while the toast is held", () => {
    ui.toast("Held.")
    const id = ui.toasts[0].id
    vi.advanceTimersByTime(4000)
    ui.holdToast(id)
    vi.advanceTimersByTime(10_000)
    expect(ui.toasts).toHaveLength(1)
    ui.releaseToast(id)
    vi.advanceTimersByTime(1999)
    expect(ui.toasts).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(ui.toasts).toHaveLength(0)
  })

  it("keeps the actions and the Markdown flag", () => {
    ui.toast("🗄 **Archived**", "ok", {
      actions: [{ label: "↩️ Undo", data: "/unarchive 0123456789abcdef012" }],
      markdown: true,
    })
    expect(ui.toasts[0].actions).toEqual([
      { label: "↩️ Undo", data: "/unarchive 0123456789abcdef012" },
    ])
    expect(ui.toasts[0].markdown).toBe(true)
  })
})

describe("prefs", () => {
  it("reads the stored prefs", async () => {
    localStorage.setItem("tether.theme", "light")
    localStorage.setItem("tether.sound", "false")
    localStorage.setItem("tether.route", "/jobs")
    vi.resetModules()
    ui = await import("./ui")
    expect(ui.prefs).toEqual({ theme: "light", sound: false, lastRoute: "/jobs" })
  })

  it("has the defaults", () => {
    expect(ui.prefs).toEqual({ theme: "system", sound: true, lastRoute: "" })
  })

  it("sets and stores the theme", () => {
    ui.setTheme("light")
    expect(document.documentElement.classList.contains("tt")).toBe(true)
    expect(document.documentElement.classList.contains("theme-light")).toBe(true)
    expect(document.documentElement.classList.contains("theme-dark")).toBe(false)
    expect(localStorage.getItem("tether.theme")).toBe("light")
  })

  it("writes the theme key once on setTheme", async () => {
    const set = vi.spyOn(localStorage, "setItem")
    ui.setTheme("dark")
    await nextTick()
    expect(set.mock.calls.filter((c) => c[0] === "tether.theme")).toHaveLength(1)
    set.mockRestore()
  })

  it("writes a key only when that key changes", async () => {
    const set = vi.spyOn(localStorage, "setItem")
    ui.prefs.lastRoute = "/jobs"
    await nextTick()
    expect(set.mock.calls.map((c) => c[0])).toEqual(["tether.route"])
    set.mockClear()
    ui.prefs.sound = false
    await nextTick()
    expect(set.mock.calls.map((c) => c[0])).toEqual(["tether.sound"])
    set.mockRestore()
  })

  it("applies the theme with no write", () => {
    stubMedia(true)
    const set = vi.spyOn(localStorage, "setItem")
    ui.applyTheme()
    expect(document.documentElement.classList.contains("theme-light")).toBe(true)
    expect(set).not.toHaveBeenCalled()
    set.mockRestore()
  })

  it("follows the system theme", () => {
    stubMedia(true)
    ui.setTheme("system")
    expect(document.documentElement.classList.contains("theme-light")).toBe(true)
    stubMedia(false)
    ui.setTheme("system")
    expect(document.documentElement.classList.contains("theme-dark")).toBe(true)
  })
})

describe("isSecure", () => {
  it("reads the secure context flag", () => {
    vi.stubGlobal("isSecureContext", false)
    expect(ui.isSecure()).toBe(false)
    vi.stubGlobal("isSecureContext", true)
    expect(ui.isSecure()).toBe(true)
  })
})

describe("confirm", () => {
  it("waits for the answer", async () => {
    const p = ui.confirm({
      title: "Delete the job?",
      body: "nightly-backup",
      confirmLabel: "Delete",
      danger: true,
    })
    expect(ui.pendingConfirm.opts?.title).toBe("Delete the job?")
    ui.pendingConfirm.resolve!(true)
    await expect(p).resolves.toBe(true)
    expect(ui.pendingConfirm.opts).toBeNull()
  })

  it("answers an older confirm with false", async () => {
    const a = ui.confirm({ title: "A", body: "", confirmLabel: "OK" })
    const b = ui.confirm({ title: "B", body: "", confirmLabel: "OK" })
    await expect(a).resolves.toBe(false)
    expect(ui.pendingConfirm.opts?.title).toBe("B")
    ui.pendingConfirm.resolve!(false)
    await expect(b).resolves.toBe(false)
  })
})

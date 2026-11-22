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
  it("shows one toast for 6 s", () => {
    ui.toast("Saved. Applies to new sessions.")
    expect(ui.toasts).toHaveLength(1)
    expect(ui.toasts[0].text).toBe("Saved. Applies to new sessions.")
    expect(ui.toasts[0].kind).toBe("info")
    ui.toast("Renamed.", "ok")
    expect(ui.toasts.map((t) => t.text)).toEqual(["Renamed."])
    vi.advanceTimersByTime(5999)
    expect(ui.toasts).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(ui.toasts).toHaveLength(0)
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

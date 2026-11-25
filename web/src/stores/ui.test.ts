import { computed, nextTick } from "vue"
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

// Gives a matchMedia with an OS value that can change. A change calls each listener, as a
// browser does.
function liveMedia(dark: boolean): (next: boolean) => void {
  const state = { dark }
  const fns = new Set<(e: { matches: boolean }) => void>()
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() {
      return q.includes("dark") ? state.dark : !state.dark
    },
    media: q,
    addEventListener: (_t: string, f: (e: { matches: boolean }) => void) => fns.add(f),
    removeEventListener: (_t: string, f: (e: { matches: boolean }) => void) => fns.delete(f),
  }))
  return (next) => {
    state.dark = next
    for (const f of fns) f({ matches: next })
  }
}

beforeEach(async () => {
  vi.useFakeTimers()
  localStorage.clear()
  document.documentElement.className = ""
  vi.resetModules()
  ui = await import("./ui")
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("toast", () => {
  it("holds and releases all toasts at once", () => {
    ui.toast("One.")
    ui.toast("Two.", "ok")
    ui.toast("⏳ Busy", "warn")
    vi.advanceTimersByTime(4000)
    ui.holdAllToasts()
    vi.advanceTimersByTime(10_000)
    expect(ui.toasts).toHaveLength(3)
    ui.releaseAllToasts()
    vi.advanceTimersByTime(1999)
    expect(ui.toasts).toHaveLength(3)
    vi.advanceTimersByTime(1)
    expect(ui.toasts.map((t) => t.text)).toEqual(["⏳ Busy"])
  })

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
    const cls = document.documentElement.classList
    ui.setTheme("light")
    expect(cls.contains("light")).toBe(true)
    expect(cls.contains("dark")).toBe(false)
    expect(localStorage.getItem("tether.theme")).toBe("light")
    ui.setTheme("dark")
    expect(cls.contains("dark")).toBe(true)
    expect(cls.contains("light")).toBe(false)
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
    stubMedia(false)
    const set = vi.spyOn(localStorage, "setItem")
    ui.applyTheme()
    expect(document.documentElement.className).toBe("")
    expect(set).not.toHaveBeenCalled()
    set.mockRestore()
  })

  it("sets no class for System and keeps the other classes", () => {
    document.documentElement.classList.add("keep")
    ui.setTheme("dark")
    ui.setTheme("system")
    expect([...document.documentElement.classList]).toEqual(["keep"])
  })

  it("resolves the theme from the choice and the OS", () => {
    stubMedia(true)
    ui.setTheme("system")
    expect(ui.resolvedTheme()).toBe("light")
    stubMedia(false)
    expect(ui.resolvedTheme()).toBe("dark")
    ui.setTheme("light")
    expect(ui.resolvedTheme()).toBe("light")
    vi.stubGlobal("matchMedia", undefined)
    ui.setTheme("system")
    expect(ui.resolvedTheme()).toBe("light")
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

function addMeta(content = ""): HTMLMetaElement {
  const meta = document.createElement("meta")
  meta.name = "theme-color"
  if (content) meta.content = content
  document.head.append(meta)
  return meta
}

describe("theme-color", () => {
  afterEach(() => {
    for (const m of document.head.querySelectorAll('meta[name="theme-color"]')) m.remove()
  })

  it("has a hex value for each theme", () => {
    expect(ui.THEME_COLOR).toEqual({ light: "#ffffff", dark: "#030712" })
    expect(Object.isFrozen(ui.THEME_COLOR)).toBe(true)
  })

  it("sets the theme-color meta to the hex value of the theme", () => {
    const meta = addMeta()
    ui.setTheme("light")
    expect(meta.getAttribute("content")).toBe("#ffffff")
    ui.setTheme("dark")
    expect(meta.getAttribute("content")).toBe("#030712")
    stubMedia(true)
    ui.setTheme("system")
    expect(meta.getAttribute("content")).toBe("#ffffff")
    stubMedia(false)
    ui.setTheme("system")
    expect(meta.getAttribute("content")).toBe("#030712")
  })

  it("runs with no meta", () => {
    expect(() => ui.applyTheme()).not.toThrow()
  })

  it("follows an OS change while System is set", () => {
    const meta = addMeta()
    const osChange = liveMedia(false)
    ui.setTheme("system")
    const theme = computed(() => ui.resolvedTheme())
    expect(theme.value).toBe("light")
    expect(meta.getAttribute("content")).toBe("#ffffff")
    osChange(true)
    expect(theme.value).toBe("dark")
    expect(meta.getAttribute("content")).toBe("#030712")
    // The CSS media block gives the dark tokens. System sets no class.
    expect(document.documentElement.classList.contains("dark")).toBe(false)
    expect(document.documentElement.classList.contains("light")).toBe(false)
  })

  it("ignores an OS change while Light is set", () => {
    const meta = addMeta()
    const osChange = liveMedia(false)
    ui.setTheme("light")
    osChange(true)
    expect(ui.resolvedTheme()).toBe("light")
    expect(document.documentElement.classList.contains("light")).toBe(true)
    expect(meta.getAttribute("content")).toBe("#ffffff")
  })
})

describe("list query memory", () => {
  it("gives an empty query for a list with no remembered query", () => {
    expect(ui.listLink("sessions")).toEqual({ name: "sessions", query: {} })
  })

  it("gives the last remembered query of each list", () => {
    ui.rememberListQuery("sessions", { page: "3", archived: "1" })
    ui.rememberListQuery("skills", { tab: "catalog" })
    expect(ui.listLink("sessions")).toEqual({
      name: "sessions",
      query: { page: "3", archived: "1" },
    })
    ui.rememberListQuery("sessions", {})
    expect(ui.listLink("sessions")).toEqual({ name: "sessions", query: {} })
    expect(ui.listLink("skills")).toEqual({ name: "skills", query: { tab: "catalog" } })
  })

  it("keeps a copy of the query", () => {
    const q: Record<string, string> = { page: "2" }
    ui.rememberListQuery("jobs", q)
    q.page = "9"
    expect(ui.listLink("jobs")).toEqual({ name: "jobs", query: { page: "2" } })
  })
})

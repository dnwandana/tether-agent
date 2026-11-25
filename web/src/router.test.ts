import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

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

const h = vi.hoisted(() => ({ onNewBuild: vi.fn() }))
vi.mock("./lib/update", () => ({ onNewBuild: h.onNewBuild }))

afterEach(() => {
  document.body.innerHTML = ""
  vi.unstubAllGlobals()
})

beforeEach(() => {
  localStorage.clear()
  vi.resetModules()
  h.onNewBuild.mockClear()
})

async function load() {
  return (await import("./router")).router
}

describe("router", () => {
  it("shows the update banner when a page chunk does not load", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    r.addRoute({
      name: "old-chunk",
      path: "/old-chunk",
      component: () =>
        Promise.reject(
          new TypeError("Failed to fetch dynamically imported module: /assets/JobsPage-1a2b.js"),
        ),
    })
    await expect(r.replace("/old-chunk")).rejects.toThrow("dynamically imported module")
    expect(h.onNewBuild).toHaveBeenCalledTimes(1)
  })

  it("knows the chunk errors of Firefox, Safari, and the CSS preload", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    const texts = [
      "error loading dynamically imported module: /assets/A.js",
      "Importing a module script failed.",
      "Unable to preload CSS for /assets/A.css",
    ]
    texts.forEach((t, i) =>
      r.addRoute({ name: "c" + i, path: "/c" + i, component: () => Promise.reject(new Error(t)) }),
    )
    for (let i = 0; i < texts.length; i++) await r.replace("/c" + i).catch(() => {})
    expect(h.onNewBuild).toHaveBeenCalledTimes(3)
  })

  it("does not show the banner for another error", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    r.addRoute({ name: "bad", path: "/bad", component: () => Promise.reject(new Error("boom")) })
    await r.replace("/bad").catch(() => {})
    expect(h.onNewBuild).not.toHaveBeenCalled()
  })

  it("has each route name", async () => {
    const r = await load()
    expect(
      new Set(
        r
          .getRoutes()
          .map((x) => x.name)
          .filter(Boolean),
      ),
    ).toEqual(new Set(names))
    expect(r.resolve({ name: "ops" }).path).toBe("/system/ops")
    expect(r.resolve({ name: "transcript", params: { id: "s1" } }).path).toBe("/sessions/s1")
  })

  it("sends a page with no session to signin", async () => {
    const r = await load()
    await r.push("/jobs")
    expect(r.currentRoute.value.name).toBe("signin")
  })

  it("keeps the code of a sign-in link", async () => {
    const r = await load()
    await r.push("/#code=K7QF-2M9X")
    expect(r.currentRoute.value.name).toBe("signin")
    expect(r.currentRoute.value.hash).toBe("#code=K7QF-2M9X")
  })

  it("opens the last route at /", async () => {
    localStorage.setItem("tether.session", "h1")
    localStorage.setItem("tether.route", "/jobs")
    const r = await load()
    await r.push("/")
    expect(r.currentRoute.value.path).toBe("/jobs")
  })

  it("opens the chat at / with no last route, and on an unknown path", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    await r.push("/")
    expect(r.currentRoute.value.name).toBe("chat")
    await r.push("/nope/x")
    expect(r.currentRoute.value.name).toBe("chat")
  })

  it("keeps the last route, but not the sign-in page", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    const { prefs } = await import("./stores/ui")
    await r.push("/usage?range=7d")
    expect(prefs.lastRoute).toBe("/usage?range=7d")
    await r.push("/signin")
    expect(prefs.lastRoute).toBe("/usage?range=7d")
  })

  it("shows a page while its file does not exist", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    await r.push("/models")
    expect(r.currentRoute.value.matched[0].components?.default).toBeTruthy()
  })
  it("gives only the chat and the transcript their own scroll", async () => {
    const r = await load()
    expect(r.resolve({ name: "chat" }).meta.ownScroll).toBe(true)
    expect(r.resolve({ name: "transcript", params: { id: "x" } }).meta.ownScroll).toBe(true)
    for (const n of names.filter((x) => x !== "chat" && x !== "transcript")) {
      expect(
        r.resolve({ name: n, params: { id: "x", session: "x", name: "x" } }).meta.ownScroll,
        n,
      ).not.toBe(true)
    }
  })

  it("scrolls to the hash inside the box and pulses the target once", async () => {
    const r = await load()
    const el = document.createElement("div")
    el.id = "card-c1"
    el.scrollIntoView = vi.fn()
    document.body.appendChild(el)
    const sb = r.options.scrollBehavior!
    expect(await sb({ hash: "#card-c1" } as never, {} as never, null)).toBe(false)
    expect(el.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" })
    expect(el.classList.contains("pulse")).toBe(true)
    el.dispatchEvent(new Event("animationend"))
    expect(el.classList.contains("pulse")).toBe(false)
  })

  it("scrolls with no motion when the system asks for reduced motion", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q === "(prefers-reduced-motion: reduce)",
      media: q,
      addEventListener: () => {},
      removeEventListener: () => {},
    }))
    const r = await load()
    const el = document.createElement("div")
    el.id = "card-c2"
    el.scrollIntoView = vi.fn()
    document.body.appendChild(el)
    await r.options.scrollBehavior!({ hash: "#card-c2" } as never, {} as never, null)
    expect(el.scrollIntoView).toHaveBeenCalledWith({ behavior: "auto", block: "start" })
  })

  it("keeps only the newest scroll positions", async () => {
    localStorage.setItem("tether.session", "h1")
    const mod = await import("./router")
    const r = mod.router
    const box = document.createElement("div")
    box.className = "scroll"
    document.body.appendChild(box)
    await r.push("/jobs")
    for (let i = 0; i < 60; i++) {
      box.scrollTop = 250
      await r.push(i % 2 === 0 ? "/usage" : "/jobs")
    }
    // A new position is kept.
    r.go(-1)
    await vi.waitFor(() => expect(r.currentRoute.value.path).toBe("/usage"))
    await new Promise((res) => setTimeout(res, 0))
    mod.restorePendingScroll()
    expect(box.scrollTop).toBe(250)
    // An old position is dropped.
    box.scrollTop = 0
    r.go(-57)
    await vi.waitFor(() => expect(r.currentRoute.value.path).toBe("/jobs"))
    await new Promise((res) => setTimeout(res, 0))
    mod.restorePendingScroll()
    expect(box.scrollTop).toBe(0)
  })

  it("puts the box at the top on a push and never scrolls the window", async () => {
    localStorage.setItem("tether.session", "h1")
    const win = vi.fn()
    vi.stubGlobal("scrollTo", win)
    const r = await load()
    const box = document.createElement("div")
    box.className = "scroll"
    document.body.appendChild(box)
    await r.push("/jobs")
    box.scrollTop = 250
    await r.push("/usage")
    await new Promise((res) => setTimeout(res, 0))
    expect(box.scrollTop).toBe(0)
    expect(win).not.toHaveBeenCalled()
  })

  it("restores the box after a back, only when the page asks", async () => {
    localStorage.setItem("tether.session", "h1")
    const mod = await import("./router")
    const r = mod.router
    const box = document.createElement("div")
    box.className = "scroll"
    document.body.appendChild(box)
    await r.push("/jobs")
    box.scrollTop = 250
    await r.push("/usage")
    await new Promise((res) => setTimeout(res, 0))
    r.back()
    await vi.waitFor(() => expect(r.currentRoute.value.path).toBe("/jobs"))
    await new Promise((res) => setTimeout(res, 0))
    expect(box.scrollTop).toBe(0)
    mod.restorePendingScroll()
    expect(box.scrollTop).toBe(250)
    mod.restorePendingScroll()
    expect(box.scrollTop).toBe(250)
  })

  it("keeps the box position when only the query changes", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    const box = document.createElement("div")
    box.className = "scroll"
    document.body.appendChild(box)
    await r.push("/sessions")
    box.scrollTop = 250
    await r.replace("/sessions?page=2")
    await new Promise((res) => setTimeout(res, 0))
    expect(box.scrollTop).toBe(250)
    await r.push("/jobs")
    await new Promise((res) => setTimeout(res, 0))
    expect(box.scrollTop).toBe(0)
  })

  it("puts the box at the top when a param changes", async () => {
    localStorage.setItem("tether.session", "h1")
    const r = await load()
    const box = document.createElement("div")
    box.className = "scroll"
    document.body.appendChild(box)
    await r.push("/jobs/a")
    box.scrollTop = 250
    await r.push("/jobs/b")
    await new Promise((res) => setTimeout(res, 0))
    expect(box.scrollTop).toBe(0)
  })
})

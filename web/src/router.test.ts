import { beforeEach, describe, expect, it, vi } from "vitest"

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

beforeEach(() => {
  localStorage.clear()
  vi.resetModules()
})

async function load() {
  return (await import("./router")).router
}

describe("router", () => {
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
  it("scrolls to the hash of a route", async () => {
    const r = await load()
    const sb = r.options.scrollBehavior!
    const to = { hash: "#card-c1" } as never
    expect(await sb(to, {} as never, null)).toEqual({ el: "#card-c1", behavior: "smooth" })
    expect(await sb({ hash: "" } as never, {} as never, null)).toBe(false)
    expect(await sb({ hash: "" } as never, {} as never, { left: 0, top: 40 })).toEqual({
      left: 0,
      top: 40,
    })
  })
})

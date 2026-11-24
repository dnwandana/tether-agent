import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"
import type { SetupStep } from "../api/types.gen"

const h = vi.hoisted(() => ({
  live: null as null | { load: () => Promise<void>; opts?: { events?: string[] } },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", async () => ({
  ...(await vi.importActual<object>("../stores/ui")),
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/live", async () => {
  const actual = await vi.importActual<object>("../lib/live")
  const { onMounted, ref } = await import("vue")
  return {
    ...actual,
    useLiveData: (load: () => Promise<void>, opts?: any) => {
      h.live = { load, opts }
      onMounted(() => void load())
      return { stale: ref(false), updatedAt: ref(null), refresh: load }
    },
  }
})
import { api } from "../api/client"
import { isSecure, toast } from "../stores/ui"
import GettingStartedPage from "./GettingStartedPage.vue"

const Blank = { render: () => null }
const unknown = (name: string): SetupStep => ({
  name,
  state: "unknown",
  detail: "Run the doctor on the System page.",
  command: "tether doctor",
})
const steps: SetupStep[] = [
  { name: "Primary provider", state: "done", detail: "", command: "" },
  {
    name: "Fallback entries",
    state: "open",
    detail: "No fallback entry. A failed primary stops the turns.",
    command: "tether setup",
  },
  { name: "Telegram", state: "open", detail: "Telegram is off.", command: "tether setup" },
  unknown("Host programs"),
  unknown("Service unit"),
  { name: "Dashboard", state: "done", detail: "", command: "" },
  {
    name: "HTTPS",
    state: "open",
    detail:
      "Set dashboard.tls_cert and dashboard.tls_key, for example to the files of tailscale cert.",
    command: "tailscale cert <name>.<tailnet>.ts.net",
  },
]

const posts = () => vi.mocked(api).mock.calls.filter((c) => c[0] === "POST")

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/start", name: "start", component: GettingStartedPage },
      { path: "/system", name: "system", component: Blank },
    ],
  })
  await router.push("/start")
  const w = mount(GettingStartedPage, {
    global: { plugins: [router], stubs: { SidePanel: true, LiveHeader: true } },
  })
  await flushPromises()
  return Object.assign(w, { router })
}
const btns = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)

beforeEach(() => {
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string) => (m === "POST" ? { task_id: "t1" } : steps) as any)
  vi.mocked(isSecure).mockReturnValue(true)
  vi.mocked(toast).mockReset()
})

describe("GettingStartedPage", () => {
  it("shows a done step with ✓, an open step with its command, and an unknown step with ?", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/setup")
    const rows = w.findAll(".li")
    expect(rows[0].find(".lead").classes()).toContain("is-done")
    expect(rows[0].find(".lead").text()).toBe("✓")
    expect(rows[0].find("code").exists()).toBe(false)
    expect(rows[1].find(".lead").text()).toBe("2")
    expect(rows[1].find("code").text()).toBe("tether setup")
    expect(rows[3].find(".lead").classes()).toContain("is-unknown")
    expect(rows[3].find(".lead").text()).toBe("?")
    expect(rows[3].text()).toContain("Run the doctor on the System page.")
    expect(rows[3].find("code").exists()).toBe(false)
    expect(btns(w, "Run doctor")).toHaveLength(1)
    expect(rows[3].find("button").exists()).toBe(false)
    expect(rows[4].find("button").exists()).toBe(false)
    const html = w.html()
    expect(html.indexOf("Run doctor")).toBeLessThan(html.indexOf("Primary provider"))
    expect(rows[6].text()).toContain("How to turn on HTTPS")
    expect(btns(w, "Copy")).toHaveLength(3)
  })

  it("starts the doctor and opens System", async () => {
    const w = await mk()
    await btns(w, "Run doctor")[0].trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/doctor")
    expect(w.router.currentRoute.value.name).toBe("system")
  })

  it("posts the doctor once on a double tap and disables the buttons", async () => {
    const hold: { done: (v: unknown) => void } = { done: () => {} }
    vi.mocked(api).mockImplementation(async (m: string) =>
      m === "POST" ? new Promise((r) => (hold.done = r)) : (steps as any),
    )
    const w = await mk()
    await btns(w, "Run doctor")[0].trigger("click")
    await btns(w, "Run doctor")[0].trigger("click")
    expect(posts()).toHaveLength(1)
    expect(btns(w, "Run doctor")[0].attributes("disabled")).toBeDefined()
    hold.done({})
    await flushPromises()
    expect(posts()).toHaveLength(1)
  })

  it("shows the server text of a refused doctor run and still opens System", async () => {
    vi.mocked(api).mockImplementation(async (m: string) => {
      if (m === "POST") throw new Error("The doctor ran 12 s ago. Try again in 48 s.")
      return steps as any
    })
    const w = await mk()
    await btns(w, "Run doctor")[0].trigger("click")
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("The doctor ran 12 s ago. Try again in 48 s.", "error")
    expect(w.router.currentRoute.value.name).toBe("system")
  })

  it("checks the steps again", async () => {
    const w = await mk()
    vi.mocked(api).mockClear()
    await btns(w, "Check again")[0].trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/setup")
  })

  it("shows the HTTPS note in place of Copy on plain HTTP, but not in the HTTPS row", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    const w = await mk()
    expect(btns(w, "Copy")).toHaveLength(0)
    const rows = w.findAll(".li")
    expect(
      rows.filter((r: any) => r.text().includes("Copy needs HTTPS or localhost.")),
    ).toHaveLength(2)
    expect(rows[6].text()).not.toContain("Copy needs HTTPS or localhost.")
    expect(
      rows[6].findAll("button").filter((b: any) => b.text() === "How to turn on HTTPS"),
    ).toHaveLength(1)
  })

  it("shows no Run doctor button when no step is unknown", async () => {
    vi.mocked(api).mockImplementation(async (m: string) =>
      m === "POST"
        ? ({ task_id: "t1" } as any)
        : (steps.filter((s) => s.state !== "unknown") as any),
    )
    const w = await mk()
    expect(btns(w, "Run doctor")).toHaveLength(0)
  })

  it("reloads only on a focus and a reconnect", async () => {
    await mk()
    expect(h.live!.opts?.events).toBeUndefined()
  })
})

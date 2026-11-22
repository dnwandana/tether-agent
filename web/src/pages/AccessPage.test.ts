import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  setSessionHeader: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
  prefs: { lastRoute: "" },
}))
import { api } from "../api/client"
import { confirm, toast } from "../stores/ui"
import AccessPage from "./AccessPage.vue"

const Blank = { render: () => null }
const view = {
  pending: [
    {
      platform: "telegram",
      user_id: "58210993",
      name: "Mira K.",
      status: "pending",
      since: "2026-11-20T09:56:00Z",
      expires: "2026-11-20T10:56:00Z",
    },
  ],
  approved: [
    {
      platform: "telegram",
      user_id: "73300412",
      name: "Arif (laptop)",
      status: "approved",
      since: "2026-11-04T10:00:00Z",
      expires: "0001-01-01T00:00:00Z",
    },
  ],
  tries_left: 5,
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/access", name: "access", component: AccessPage },
      { path: "/start", name: "start", component: Blank },
    ],
  })
  await router.push("/access")
  const w = mount(AccessPage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string) => (m === "GET" ? view : {}) as any)
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

describe("AccessPage", () => {
  it("shows the tries left from the view", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, tries_left: 2 } : {}) as any,
    )
    const w = await mk()
    expect(w.text()).toContain("(2 tries left)")
  })

  it("approves a code after the owner confirm", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/pairing")
    expect(w.text()).toContain("5 wrong codes lock pairing for 1 hour (5 tries left).")
    await w.find("input.mono").setValue("k7q2mxrp")
    await btn(w, "Approve").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Approve Mira K.?",
      body: "This person becomes an owner. They can chat, approve commands, and change settings.",
      confirmLabel: "Approve",
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/approve", {
      platform: "telegram",
      code: "K7Q2MXRP",
    })
    expect(toast).toHaveBeenCalledWith("Approved Mira K..")
  })

  it("refuses a short code before a request", async () => {
    const w = await mk()
    await w.find("input.mono").setValue("K7Q2")
    await btn(w, "Approve").trigger("click")
    expect(toast).toHaveBeenCalledWith("The code has 8 characters.", "error")
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("revokes an approved person", async () => {
    const w = await mk()
    await btn(w, "Revoke").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Revoke Arif (laptop)?",
      body: "They lose access at once. Their sessions stay.",
      confirmLabel: "Revoke",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/revoke", {
      platform: "telegram",
      user_id: "73300412",
    })
  })
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  setSessionHeader: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, setSessionHeader } from "../api/client"
import { confirm, toast } from "../stores/ui"
import DevicesTable from "./DevicesTable.vue"

const Blank = { render: () => null }
const devices = [
  {
    id: "d1",
    label: "Laptop",
    browser: "Firefox 133 on Linux",
    ip: "10.8.0.2",
    created: "2026-11-20T09:12:00Z",
    last_seen: "2026-11-20T10:00:00Z",
    expires: "2026-11-21T09:12:00Z",
    this_device: true,
  },
  {
    id: "d2",
    label: "Pixel 8",
    browser: "Chrome 131 on Android",
    ip: "10.8.0.5",
    created: "2026-11-20T07:40:00Z",
    last_seen: "2026-11-20T09:48:00Z",
    expires: "2026-11-21T07:40:00Z",
    this_device: false,
  },
]

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/security", name: "security", component: Blank },
      { path: "/signin", name: "signin", component: Blank },
    ],
  })
  await router.push("/security")
  const w = mount(DevicesTable, { global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}
const btns = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string) => (m === "GET" ? devices : {}) as any)
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
  vi.mocked(setSessionHeader).mockReset()
})

describe("DevicesTable", () => {
  it("lists the devices and marks this device", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/devices")
    expect(w.text()).toContain("Chrome 131 on Android")
    expect(w.text()).toContain("10.8.0.5")
    expect(w.findAll(".chip").filter((c) => c.text() === "This device")).toHaveLength(1)
  })

  it("revokes another device after a confirm", async () => {
    const { w } = await mk()
    await btns(w, "Revoke")[1].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Sign out Pixel 8?",
      body: "Chrome 131 on Android at 10.8.0.5 must ask for access again.",
      confirmLabel: "Revoke",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/devices/d2/revoke")
    expect(toast).toHaveBeenCalledWith("Signed out Pixel 8.")
  })

  it("signs this device out and goes to the sign-in page", async () => {
    const { w, router } = await mk()
    await btns(w, "Revoke")[0].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Sign out Laptop?",
      body: "This is the device you use now. You go to the sign-in screen.",
      confirmLabel: "Revoke",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/devices/d1/revoke")
    expect(setSessionHeader).toHaveBeenCalledWith(null)
    expect(router.currentRoute.value.name).toBe("signin")
  })

  it("signs out all other devices", async () => {
    const { w } = await mk()
    await btns(w, "Sign out all other devices…")[0].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Sign out 1 other devices?",
      body: "Each of them must ask for access again. This device stays signed in.",
      confirmLabel: "Sign out all others",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/devices/revoke-others")
  })
})

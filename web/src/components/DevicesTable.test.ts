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
import { fullTime, shortTime } from "../lib/time"

const Blank = { render: () => null }
const devices = [
  {
    id: "d1",
    label: "Laptop",
    browser: "Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
    ip: "10.8.0.2",
    created: "2026-11-20T09:12:00Z",
    last_seen: "2026-11-20T10:00:00Z",
    expires: "2026-11-21T09:12:00Z",
    this_device: true,
  },
  {
    id: "d2",
    label: "Pixel 8",
    browser:
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
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
    expect(w.text()).toContain("Chrome on Android")
    expect(w.findAll("details")[1].text()).toContain("Chrome/131.0.0.0")
    expect(w.text()).toContain("10.8.0.5")
    expect(w.findAll(".chip").filter((c) => c.text() === "This device")).toHaveLength(1)
  })

  it("revokes another device after a confirm", async () => {
    const { w } = await mk()
    await btns(w, "Sign out")[1].trigger("click")
    // The revoke waits for the confirm and then for the API call. Wait for the last step, so
    // the test does not depend on the number of microtask turns.
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith("Signed out Pixel 8."))
    expect(confirm).toHaveBeenCalledWith({
      title: "Sign out Pixel 8?",
      body: "Chrome on Android at 10.8.0.5 must ask for access again.",
      confirmLabel: "Sign out",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/devices/d2/revoke")
  })

  it("signs this device out and goes to the sign-in page", async () => {
    const { w, router } = await mk()
    await btns(w, "Sign out")[0].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Sign out Laptop?",
      body: "This is the device you use now. You go to the sign-in screen.",
      confirmLabel: "Sign out",
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
      title: "Sign out 1 other device?",
      body: "Each of them must ask for access again. This device stays signed in.",
      confirmLabel: "Sign out all others",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/devices/revoke-others")
  })

  it("uses the plural for two other devices, and loads again from the error state", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? [...devices, { ...devices[1], id: "d3" }] : {}) as any,
    )
    const { w } = await mk()
    await btns(w, "Sign out all other devices…")[0].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({ title: "Sign out 2 other devices?" }),
    )
    vi.mocked(api).mockReset()
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const e = await mk()
    vi.mocked(api).mockImplementation(async () => devices as any)
    e.w.findComponent({ name: "StateBlock" }).vm.$emit("action")
    await flushPromises()
    expect(e.w.text()).toContain("10.8.0.5")
  })

  it("shows the user agent in the Device cell, a spaced chip, and no Browser column", async () => {
    const { w } = await mk()
    expect(w.findAll("th").map((t) => t.text())).not.toContain("Browser")
    const first = w.findAll("tbody tr")[0].findAll("td")
    expect(first[0].text()).toContain("Laptop This device")
    expect(first[0].find("details summary").text()).toBe("Firefox on Linux")
    expect(first[0].find("details").text()).toContain("Firefox/133.0")
  })

  it("shows a dash for a sign-in time that is not known", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) =>
        (m === "GET" ? [{ ...devices[0], created: "0001-01-01T00:00:00Z" }] : {}) as any,
    )
    const { w } = await mk()
    expect(w.findAll("tbody tr")[0].findAll("td")[2].find("span").text()).toBe("—")
  })

  it("shows the sign-in time in the short form with the full time in the tooltip", async () => {
    const { w } = await mk()
    const cell = w.findAll("tbody tr")[0].findAll("td")[2].find("span")
    expect(cell.text()).toBe(shortTime("2026-11-20T09:12:00Z"))
    expect(cell.attributes("title")).toBe(fullTime("2026-11-20T09:12:00Z"))
  })
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
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
  const r = render(DevicesTable, { global: { plugins: [router] } })
  await flushPromises()
  return { r, router, q: within(r.container), user: userEvent.setup() }
}
// Returns the body rows. Row 0 of the table is the header row.
const bodyRows = (q: ReturnType<typeof within>) => q.getAllByRole("row").slice(1)

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
    const { r, q } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/devices")
    expect(r.container.textContent).toContain("Chrome on Android")
    expect(r.container.querySelectorAll("details")[1].textContent).toContain("Chrome/131.0.0.0")
    expect(r.container.textContent).toContain("10.8.0.5")
    const marks = q.getAllByText("This device")
    expect(marks).toHaveLength(1)
    expect(marks[0].getAttribute("data-slot")).toBe("badge")
    expect(
      q.getByText(
        /A new device signs in when you tap Allow in Telegram or on a signed-in device\./,
      ),
    ).toBeTruthy()
    expect(r.container.textContent).not.toContain("✅")
  })

  it("revokes another device after a confirm", async () => {
    const { q, user } = await mk()
    await user.click(q.getAllByRole("button", { name: "Sign out" })[1])
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
    const { q, user, router } = await mk()
    await user.click(q.getAllByRole("button", { name: "Sign out" })[0])
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
    const { q, user } = await mk()
    const all = q.getByRole("button", { name: "Sign out all other devices…" })
    expect(all.getAttribute("data-slot")).toBe("button")
    await user.click(all)
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
    const a = await mk()
    await a.user.click(a.q.getByRole("button", { name: "Sign out all other devices…" }))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({ title: "Sign out 2 other devices?" }),
    )
    a.r.unmount()
    vi.mocked(api).mockReset()
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const e = await mk()
    vi.mocked(api).mockImplementation(async () => devices as any)
    await e.user.click(e.q.getByRole("button", { name: "Retry" }))
    await flushPromises()
    expect(e.r.container.textContent).toContain("10.8.0.5")
  })

  it("shows the user agent in the Device cell, a spaced chip, and no Browser column", async () => {
    const { q } = await mk()
    expect(q.getAllByRole("columnheader").map((t) => t.textContent!.trim())).not.toContain(
      "Browser",
    )
    const first = within(bodyRows(q)[0]).getAllByRole("cell")
    expect(first[0].textContent!.replace(/\s+/g, " ")).toContain("Laptop This device")
    expect(first[0].querySelector("details summary")!.textContent!.trim()).toBe("Firefox on Linux")
    expect(first[0].querySelector("details")!.textContent).toContain("Firefox/133.0")
  })

  it("shows a dash for a sign-in time that is not known", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) =>
        (m === "GET" ? [{ ...devices[0], created: "0001-01-01T00:00:00Z" }] : {}) as any,
    )
    const { q } = await mk()
    expect(within(bodyRows(q)[0]).getAllByRole("cell")[2].textContent!.trim()).toBe("—")
  })

  it("shows the sign-in time in the short form with the full time in the tooltip", async () => {
    const { q } = await mk()
    const cell = within(bodyRows(q)[0]).getAllByRole("cell")[2]
    const span = within(cell).getByText(shortTime("2026-11-20T09:12:00Z"))
    expect(span.getAttribute("title")).toBe(fullTime("2026-11-20T09:12:00Z"))
    expect(span.classList.contains("whitespace-nowrap")).toBe(true)
  })

  it("pages the devices with their own stored size", async () => {
    localStorage.setItem("tether.rows.devices", "10")
    const many = Array.from({ length: 11 }, (_, i) => ({
      ...devices[1],
      id: `d${i + 1}`,
      label: `Phone ${i + 1}`,
    }))
    vi.mocked(api).mockImplementation(async (m: string) => (m === "GET" ? many : {}) as any)
    const { q, user } = await mk()
    const nav = q.getByRole("navigation", { name: "Devices pages" })
    expect(q.getByText("1–10 of 11")).toBeTruthy()
    expect(q.getAllByRole("button", { name: "Sign out" })).toHaveLength(10)
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    expect(q.getByText("Phone 11")).toBeTruthy()
  })

  it("lets the Device cell grow and keeps Sign out at the right in a 7rem column", async () => {
    const { r, q } = await mk()
    const device = within(bodyRows(q)[0]).getAllByRole("cell")[0]
    expect(device.classList).toContain("whitespace-normal")
    expect(device.classList).not.toContain("overflow-hidden")
    const cell = q.getAllByRole("button", { name: "Sign out" })[0].closest("td")!
    expect(cell.classList).toContain("text-right")
    const cols = [...r.container.querySelectorAll("colgroup col")] as HTMLElement[]
    expect(cols[cols.length - 1].style.width).toBe("7rem")
  })

  it("shows cards with no column widths below 1024 px", async () => {
    window.matchMedia = ((q: string) => ({
      matches: q === "(max-width: 1023px)",
      media: q,
      addEventListener() {},
      removeEventListener() {},
    })) as any
    const { r } = await mk()
    expect(r.container.querySelector('[data-layout="cards"]')).not.toBeNull()
    expect(r.container.querySelector("colgroup")).toBeNull()
    expect(screen.getAllByRole("button", { name: "Sign out" })).toHaveLength(2)
  })
})

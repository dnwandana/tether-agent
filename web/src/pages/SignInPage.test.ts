import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  setSessionHeader: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class ApiError extends Error {
    status: number
    serverText: string | null
    constructor(s: number, m: string, serverText: string | null = null) {
      super(m)
      this.status = s
      this.serverText = serverText
    }
  },
}))
vi.mock("../stores/ui", () => ({
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
  prefs: { theme: "system", sound: true, lastRoute: "/jobs" },
}))
import { api, setSessionHeader, ApiError } from "../api/client"
import { isSecure } from "../stores/ui"
import SignInPage from "./SignInPage.vue"

const Blank = { render: () => null }
const android =
  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"

async function mk(path = "/signin") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/signin", name: "signin", component: SignInPage },
      { path: "/", name: "chat", component: Blank },
      { path: "/jobs", name: "jobs", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(SignInPage, { global: { plugins: [router], stubs: { SidePanel: true } } })
  await flushPromises()
  return { w, router }
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
  })
  Object.defineProperty(navigator, "userAgent", { value: android, configurable: true })
  vi.mocked(api).mockReset()
  vi.mocked(setSessionHeader).mockReset()
  vi.mocked(isSecure).mockReturnValue(true)
  history.replaceState(null, "", "/")
})
afterEach(() => vi.useRealTimers())

describe("SignInPage", () => {
  it("shows the brand mark before the name on the card", async () => {
    const { w } = await mk()
    const brand = w.find(".card-b .row")
    expect(brand.element.firstElementChild?.classList.contains("rail-mark")).toBe(true)
    expect(brand.find(".rail-mark").attributes("aria-hidden")).toBe("true")
    expect(brand.find(".rail-mark svg path").attributes("d")).toBe(
      "M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92",
    )
    const dot = brand.find(".rail-mark svg circle")
    expect(dot.attributes("cx")).toBe("17.28")
    expect(dot.attributes("cy")).toBe("15.84")
    expect(dot.attributes("r")).toBe("1.56")
    expect(brand.text()).toBe("Tether")
  })

  it("requests access with the device label and signs in after Allow", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "2026-11-20T10:05:00Z" })
      .mockResolvedValueOnce({ state: "pending", header: "" })
      .mockResolvedValueOnce({ state: "allowed", header: "hdr-value" })
    const { w, router } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/auth/request", { label: "Chrome on Android" })
    expect(w.find(".check-code").text()).toBe("4821")
    expect(w.text()).toContain("Approve it in Telegram, or on a device that is already signed in.")
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/auth/request/r1")
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(setSessionHeader).toHaveBeenCalledWith("hdr-value")
    expect(router.currentRoute.value.fullPath).toBe("/jobs")
  })

  it("shows Request denied and Request expired", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
      .mockResolvedValueOnce({ state: "denied", header: "" })
    const { w } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.find("h1").text()).toBe("Request denied")
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r2", code: "1234", expires: "" })
      .mockResolvedValueOnce({ state: "expired", header: "" })
    await btn(w, "Request access again").trigger("click")
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.find("h1").text()).toBe("Request expired")
  })

  it("disables Request access again while a request runs", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
      .mockResolvedValueOnce({ state: "denied", header: "" })
    const { w } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    vi.mocked(api)
      .mockClear()
      .mockReturnValue(new Promise(() => {}) as any)
    const again = btn(w, "Request access again")
    await again.trigger("click")
    expect(again.attributes("disabled")).toBeDefined()
    await again.trigger("click")
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("stops asking after Cancel", async () => {
    vi.mocked(api).mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
    const { w } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    await btn(w, "Cancel").trigger("click")
    vi.advanceTimersByTime(10000)
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("signs in with the code of the link fragment and removes the fragment", async () => {
    history.replaceState(null, "", "/signin#code=K7QF-2M9X")
    vi.mocked(api).mockResolvedValueOnce({ header: "hdr2" })
    await mk()
    expect(api).toHaveBeenCalledWith("POST", "/auth/signin", { code: "K7QF-2M9X" })
    expect(location.hash).toBe("")
    expect(setSessionHeader).toHaveBeenCalledWith("hdr2")
  })

  it("shows the code error, the ended session, and the HTTPS line", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    vi.mocked(api).mockRejectedValueOnce(new (ApiError as any)(401, "bad code"))
    const { w } = await mk("/signin?reason=ended")
    expect(w.find(".warn-banner").text()).toBe("Your session ended. Request access again.")
    expect(w.text()).toContain("Voice notes, Copy, and notifications need HTTPS or localhost")
    await btn(w, "Use a code from tether dashboard link").trigger("click")
    await w.find("input").setValue(" AAAA-BBBB ")
    await btn(w, "Sign in").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/auth/signin", { code: "AAAA-BBBB" })
    expect(w.find(".c-bad").text()).toBe("The code is not valid, or it expired.")
  })

  it("shows the server sentence of a code error", async () => {
    vi.mocked(api).mockRejectedValueOnce(
      new (ApiError as any)(401, "x", "The code is not valid or has expired."),
    )
    const { w } = await mk()
    await btn(w, "Use a code from tether dashboard link").trigger("click")
    await w.find("input").setValue("AAAA-BBBB")
    await btn(w, "Sign in").trigger("click")
    await flushPromises()
    expect(w.find(".c-bad").text()).toBe("The code is not valid or has expired.")
  })

  it("says that it tries again when the poll cannot reach the gateway", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce({ state: "pending", header: "" })
    const { w } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.text()).toContain("Cannot reach the gateway. The page tries again.")
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(w.text()).not.toContain("Cannot reach the gateway.")
  })

  it("counts down the 5 minutes of the request", async () => {
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    vi.mocked(api).mockResolvedValueOnce({
      id: "r1",
      code: "4821",
      expires: "2026-11-20T10:05:00Z",
    })
    const { w } = await mk()
    await btn(w, "Request access").trigger("click")
    await flushPromises()
    expect(w.text()).toContain("Expires in 5:00")
    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(w.text()).toContain("Expires in 4:59")
  })

  it("labels the code input and ignores Enter while busy", async () => {
    let release!: (v: unknown) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (release = r)) as any)
    const { w } = await mk()
    await btn(w, "Use a code from tether dashboard link").trigger("click")
    const input = w.find("input[aria-label='One-time code']")
    await input.setValue("AAAA-BBBB")
    await input.trigger("keydown", { key: "Enter" })
    await input.trigger("keydown", { key: "Enter" })
    expect(api).toHaveBeenCalledTimes(1)
    release({ header: "h" })
    await flushPromises()
  })
})

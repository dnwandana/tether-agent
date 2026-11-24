import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
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
  const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
  render(SignInPage, { global: { plugins: [router], stubs: { SidePanel: true } } })
  await flushPromises()
  return { router, user }
}
const btn = (name: string) => screen.getByRole("button", { name })
const h1 = () => screen.getByRole("heading", { level: 1 }).textContent?.trim()

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
    await mk()
    const name = screen.getByText("Tether")
    const mark = name.previousElementSibling!
    expect(
      mark.matches('[aria-hidden="true"]') || mark.querySelector('[aria-hidden="true"]') !== null,
    ).toBe(true)
    expect(mark.querySelector("svg path")?.getAttribute("d")).toBe(
      "M6.24 7.2H17.76M12 7.2v6.72c0 3.84 3.84 4.56 5.28 1.92",
    )
    const dot = mark.querySelector("svg circle")
    expect(dot?.getAttribute("cx")).toBe("17.28")
    expect(dot?.getAttribute("cy")).toBe("15.84")
    expect(dot?.getAttribute("r")).toBe("1.56")
    expect(name.parentElement?.textContent?.trim()).toBe("Tether")
  })

  it("requests access with the device label and signs in after Allow", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "2026-11-20T10:05:00Z" })
      .mockResolvedValueOnce({ state: "pending", header: "" })
      .mockResolvedValueOnce({ state: "allowed", header: "hdr-value" })
    const { router, user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/auth/request", { label: "Chrome on Android" })
    expect(screen.getByText("4821")).toBeTruthy()
    expect(
      screen.getByText("Approve it in Telegram, or on a device that is already signed in."),
    ).toBeTruthy()
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
    const { user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(h1()).toBe("Request denied")
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r2", code: "1234", expires: "" })
      .mockResolvedValueOnce({ state: "expired", header: "" })
    await user.click(btn("Request access again"))
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(h1()).toBe("Request expired")
  })

  it("disables Request access again while a request runs", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
      .mockResolvedValueOnce({ state: "denied", header: "" })
    const { user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    vi.mocked(api)
      .mockClear()
      .mockReturnValue(new Promise(() => {}) as any)
    const again = btn("Request access again")
    await user.click(again)
    expect(again.hasAttribute("disabled")).toBe(true)
    await user.click(again)
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("stops asking after Cancel", async () => {
    vi.mocked(api).mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
    const { user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    await user.click(btn("Cancel"))
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
    const { user } = await mk("/signin?reason=ended")
    expect(screen.getByRole("alert").textContent?.trim()).toBe(
      "Your session ended. Request access again.",
    )
    expect(document.body.textContent).toContain(
      "Voice notes, Copy, and notifications need HTTPS or localhost",
    )
    await user.click(btn("Use a code from tether dashboard link"))
    await user.type(screen.getByRole("textbox", { name: "One-time code" }), " AAAA-BBBB ")
    await user.click(btn("Sign in"))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/auth/signin", { code: "AAAA-BBBB" })
    expect(screen.getByRole("alert").textContent?.trim()).toBe(
      "The code is not valid, or it expired.",
    )
  })

  it("shows the server sentence of a code error", async () => {
    vi.mocked(api).mockRejectedValueOnce(
      new (ApiError as any)(401, "x", "The code is not valid or has expired."),
    )
    const { user } = await mk()
    await user.click(btn("Use a code from tether dashboard link"))
    await user.type(screen.getByRole("textbox", { name: "One-time code" }), "AAAA-BBBB")
    await user.click(btn("Sign in"))
    await flushPromises()
    expect(screen.getByRole("alert").textContent?.trim()).toBe(
      "The code is not valid or has expired.",
    )
  })

  it("says that it tries again when the poll cannot reach the gateway", async () => {
    vi.mocked(api)
      .mockResolvedValueOnce({ id: "r1", code: "4821", expires: "" })
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce({ state: "pending", header: "" })
    const { user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(
      screen.getByText("Cannot reach the gateway. The page tries again.").getAttribute("role"),
    ).toBe("status")
    vi.advanceTimersByTime(2000)
    await flushPromises()
    expect(document.body.textContent).not.toContain("Cannot reach the gateway.")
  })

  it("counts down the 5 minutes of the request", async () => {
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    vi.mocked(api).mockResolvedValueOnce({
      id: "r1",
      code: "4821",
      expires: "2026-11-20T10:05:00Z",
    })
    const { user } = await mk()
    await user.click(btn("Request access"))
    await flushPromises()
    expect(document.body.textContent).toContain("Expires in 5:00")
    vi.advanceTimersByTime(1000)
    await flushPromises()
    expect(document.body.textContent).toContain("Expires in 4:59")
  })

  it("labels the code input and ignores Enter while busy", async () => {
    let release!: (v: unknown) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (release = r)) as any)
    const { user } = await mk()
    await user.click(btn("Use a code from tether dashboard link"))
    const input = screen.getByRole("textbox", { name: "One-time code" })
    expect(input).toBeTruthy()
    await user.type(input, "AAAA-BBBB")
    await user.keyboard("{Enter}{Enter}")
    expect(api).toHaveBeenCalledTimes(1)
    release({ header: "h" })
    await flushPromises()
  })
})

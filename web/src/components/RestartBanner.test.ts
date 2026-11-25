import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen } from "@testing-library/vue"

const h = vi.hoisted(() => ({ on: new Map<string, (d: unknown) => void>() }))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../lib/bus", () => ({
  onEvent: (name: string, fn: (d: unknown) => void) => {
    h.on.set(name, fn)
    return () => h.on.delete(name)
  },
  onReconnected: () => () => {},
  emitEvents: vi.fn(),
}))
import { api } from "../api/client"
import { status } from "../stores/status"
import RestartBanner from "./RestartBanner.vue"

const fire = async (d: object) => {
  h.on.get("restart")!(d)
  await flushPromises()
}

beforeEach(() => {
  h.on.clear()
  status.stream = "open"
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
  })
  vi.mocked(api).mockReset().mockResolvedValue({ drain_s: 120 })
})
afterEach(() => vi.useRealTimers())

const banner = () => screen.queryByRole("status")

describe("RestartBanner", () => {
  it("shows nothing before a restart event", () => {
    render(RestartBanner)
    expect(banner()).toBeNull()
  })

  it("shows the drain progress of the restart event", async () => {
    render(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 12, drain_s: 180, turns: 2, goals: 1 })
    expect(banner()!.getAttribute("data-slot")).toBe("alert")
    expect(banner()!.getAttribute("aria-live")).toBe("polite")
    expect(banner()!.textContent).toContain("The gateway restarts.")
    expect(banner()!.textContent).not.toContain("♻")
    expect(banner()!.textContent).toContain(
      "12 s of 180 s · 2 turns run · 1 goal resumes after the start",
    )
    await fire({ reason: "restart", elapsed_s: 14, drain_s: 180, turns: 1, goals: 0 })
    expect(banner()!.textContent).toContain("14 s of 180 s · 1 turn runs")
    expect(banner()!.textContent).not.toContain("goal")
    expect(api).not.toHaveBeenCalled()
  })

  it("hides when the stream opens again after a break", async () => {
    render(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 2, drain_s: 180, turns: 0, goals: 0 })
    status.stream = "reconnecting"
    await flushPromises()
    expect(banner()).not.toBeNull()
    status.stream = "open"
    await flushPromises()
    expect(banner()).toBeNull()
  })

  it("tells the owner what to do when the gateway does not come back", async () => {
    render(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 0, drain_s: 180, turns: 0, goals: 0 })
    status.stream = "reconnecting"
    await vi.advanceTimersByTimeAsync(239_000)
    expect(banner()!.textContent).not.toContain("The gateway did not come back.")
    await vi.advanceTimersByTimeAsync(1_000)
    expect(banner()!.textContent).toContain(
      "The gateway did not come back. Reload the page, or check",
    )
    expect(banner()!.querySelector("code")?.textContent).toBe("systemctl --user status tether")
  })

  it("shows the Reload button only when the gateway does not come back", async () => {
    render(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 0, drain_s: 180, turns: 0, goals: 0 })
    expect(screen.queryByRole("button", { name: "Reload" })).toBeNull()
    status.stream = "reconnecting"
    await vi.advanceTimersByTimeAsync(240_000)
    const reload = screen.getByRole("button", { name: "Reload" })
    expect(reload.getAttribute("data-slot")).toBe("button")
  })

  it("reads the drain time from the bootstrap when the event has none", async () => {
    render(RestartBanner)
    await fire({ reason: "stopping" })
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    status.stream = "reconnecting"
    await vi.advanceTimersByTimeAsync(180_000)
    expect(banner()!.textContent).toContain("The gateway did not come back.")
  })

  it("stops listening when it unmounts", () => {
    const { unmount } = render(RestartBanner)
    unmount()
    expect(h.on.has("restart")).toBe(false)
  })
})

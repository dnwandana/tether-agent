import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

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

describe("RestartBanner", () => {
  it("shows nothing before a restart event", () => {
    const w = mount(RestartBanner)
    expect(w.find(".restart").exists()).toBe(false)
  })

  it("shows the drain progress of the restart event", async () => {
    const w = mount(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 12, drain_s: 180, turns: 2, goals: 1 })
    const b = w.find(".restart")
    expect(b.attributes("role")).toBe("status")
    expect(b.text()).toContain("♻️ The gateway restarts.")
    expect(b.text()).toContain("12 s of 180 s · 2 turns run · 1 goal resumes after the start")
    await fire({ reason: "restart", elapsed_s: 14, drain_s: 180, turns: 1, goals: 0 })
    expect(w.find(".restart").text()).toContain("14 s of 180 s · 1 turn runs")
    expect(w.find(".restart").text()).not.toContain("goal")
    expect(api).not.toHaveBeenCalled()
  })

  it("hides when the stream opens again after a break", async () => {
    const w = mount(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 2, drain_s: 180, turns: 0, goals: 0 })
    status.stream = "reconnecting"
    await flushPromises()
    expect(w.find(".restart").exists()).toBe(true)
    status.stream = "open"
    await flushPromises()
    expect(w.find(".restart").exists()).toBe(false)
  })

  it("tells the owner what to do when the gateway does not come back", async () => {
    const w = mount(RestartBanner)
    await fire({ reason: "restart", elapsed_s: 0, drain_s: 180, turns: 0, goals: 0 })
    status.stream = "reconnecting"
    await vi.advanceTimersByTimeAsync(239_000)
    expect(w.text()).not.toContain("The gateway did not come back.")
    await vi.advanceTimersByTimeAsync(1_000)
    expect(w.text()).toContain("The gateway did not come back. Reload the page, or check")
    expect(w.find(".restart code").text()).toBe("systemctl --user status tether")
  })

  it("reads the drain time from the bootstrap when the event has none", async () => {
    const w = mount(RestartBanner)
    await fire({ reason: "stopping" })
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    status.stream = "reconnecting"
    await vi.advanceTimersByTimeAsync(180_000)
    expect(w.text()).toContain("The gateway did not come back.")
  })

  it("stops listening when it unmounts", () => {
    const w = mount(RestartBanner)
    w.unmount()
    expect(h.on.has("restart")).toBe(false)
  })
})

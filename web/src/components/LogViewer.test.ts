import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
import { api } from "../api/client"
import LogViewer from "./LogViewer.vue"

const local = (h: number, m: number, s: number) => new Date(2026, 10, 20, h, m, s).toISOString()
const logs = {
  lines: [
    {
      time: local(13, 59, 58),
      level: "error",
      msg: "cron: run failed",
      attrs: 'job=gh-notify err="github: HTTP 401"',
    },
    {
      time: local(14, 5, 11),
      level: "info",
      msg: "gateway: turn done",
      attrs: "key=dashboard:owner tools=9",
    },
  ],
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] })
  vi.mocked(api).mockReset().mockResolvedValue(logs)
})
afterEach(() => vi.useRealTimers())

describe("LogViewer", () => {
  it("loads the last 500 lines and shows them as text", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/logs?level=&q=&limit=500")
    const text = w.find(".log").text()
    expect(text).toContain("13:59:58")
    expect(text).toContain("ERROR")
    expect(text).toContain("cron: run failed")
    expect(w.find(".log .lv-err").exists()).toBe(true)
    w.unmount()
  })

  it("maps the levels of Go to the CSS classes", async () => {
    vi.mocked(api).mockResolvedValue({
      lines: ["INFO", "WARN", "ERROR", "DEBUG"].map((level) => ({
        time: local(1, 2, 3),
        level,
        msg: "m",
        attrs: "",
      })),
    })
    const w = mount(LogViewer)
    await flushPromises()
    for (const c of ["lv-info", "lv-warn", "lv-err", "lv-dbg"])
      expect(w.find(".log ." + c).exists()).toBe(true)
    w.unmount()
  })

  it("marks the active level button with is-on", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Warn")!
      .trigger("click")
    expect(
      w
        .findAll(".seg button")
        .find((b) => b.text() === "Warn")!
        .classes(),
    ).toContain("is-on")
    w.unmount()
  })

  it("filters by level and by text", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Warn")!
      .trigger("click")
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=&limit=500")
    await w.find(".search input").setValue("401")
    expect(api).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(250)
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=401&limit=500")
    w.unmount()
  })

  it("follows the log every 2 s until Follow is off", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w.find("label.check input").setValue(true)
    vi.advanceTimersByTime(2000)
    expect(api).toHaveBeenCalledTimes(2)
    await w.find("label.check input").setValue(false)
    vi.advanceTimersByTime(6000)
    expect(api).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it("shows a message when no line matches", async () => {
    vi.mocked(api).mockResolvedValue({ lines: [] })
    const w = mount(LogViewer)
    await flushPromises()
    expect(w.find(".log").text()).toBe("No lines match.")
    w.unmount()
  })
})

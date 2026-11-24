import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

const h = vi.hoisted(() => ({ on: new Map<string, (d: unknown) => void>() }))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/bus", () => ({
  onEvent: (name: string, fn: (d: unknown) => void) => {
    h.on.set(name, fn)
    return () => h.on.delete(name)
  },
  onReconnected: () => () => {},
  emitEvents: vi.fn(),
}))
vi.mock("../api/stream", async () => ({
  ...(await vi.importActual<object>("../api/stream")),
  setFollowLogs: vi.fn(),
}))
import { api } from "../api/client"
import { setFollowLogs } from "../api/stream"
import LogViewer from "./LogViewer.vue"

const local = (hh: number, m: number, s: number, d = 20) =>
  new Date(2026, 10, d, hh, m, s).toISOString()
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
const phone = (on: boolean) => {
  window.matchMedia = ((q: string) => ({
    matches: on && q.includes("max-width: 599px"),
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
}
const line = (level: string, msg: string) => ({ time: local(14, 6, 0), level, msg, attrs: "" })

beforeEach(() => {
  h.on.clear()
  phone(false)
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
  })
  vi.setSystemTime(new Date(2026, 10, 20, 15, 0, 0))
  vi.mocked(api).mockReset().mockResolvedValue(logs)
  vi.mocked(setFollowLogs).mockReset()
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
      .find((b) => b.text() === "Warn+")!
      .trigger("click")
    expect(
      w
        .findAll(".seg button")
        .find((b) => b.text() === "Warn+")!
        .classes(),
    ).toContain("is-on")
    w.unmount()
  })

  it("drops a slow reply for an old level", async () => {
    const slow = { lines: [line("info", "old level")] }
    const fresh = { lines: [line("warn", "new level")] }
    let first!: (v: unknown) => void
    vi.mocked(api).mockReset()
    vi.mocked(api)
      .mockImplementationOnce(() => new Promise((r) => (first = r)))
      .mockResolvedValueOnce(fresh)
    const w = mount(LogViewer)
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Warn+")!
      .trigger("click")
    await flushPromises()
    expect(w.text()).toContain("new level")
    first(slow)
    await flushPromises()
    expect(w.text()).toContain("new level")
    expect(w.text()).not.toContain("old level")
    w.unmount()
  })

  it("filters by level and by text", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Warn+")!
      .trigger("click")
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=&limit=500")
    await w.find(".search input").setValue("401")
    expect(api).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(250)
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=401&limit=500")
    w.unmount()
  })

  it("follows the log by the log event until Follow is off", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w.findAll("label.check input")[0].setValue(true)
    expect(setFollowLogs).toHaveBeenLastCalledWith(true)
    h.on.get("log")!({ lines: [line("info", "new line")] })
    await flushPromises()
    expect(w.find(".log").text()).toContain("new line")
    vi.advanceTimersByTime(6000)
    expect(api).toHaveBeenCalledTimes(1)
    await w.findAll("label.check input")[0].setValue(false)
    expect(setFollowLogs).toHaveBeenLastCalledWith(false)
    expect(h.on.has("log")).toBe(false)
    w.unmount()
  })

  it("stops the follow of the stream when it unmounts", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w.findAll("label.check input")[0].setValue(true)
    w.unmount()
    expect(setFollowLogs).toHaveBeenLastCalledWith(false)
  })

  it("filters the followed lines by level and by text on the page", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Warn+")!
      .trigger("click")
    await flushPromises()
    await w.findAll("label.check input")[0].setValue(true)
    h.on.get("log")!({
      lines: [line("info", "quiet"), line("WARN", "disk low"), line("error", "other fail")],
    })
    await flushPromises()
    const text = w.find(".log").text()
    expect(text).not.toContain("quiet")
    expect(text).toContain("disk low")
    expect(text).toContain("other fail")
    w.unmount()
  })

  it("keeps at most 2,000 lines", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    await w.findAll("label.check input")[0].setValue(true)
    h.on.get("log")!({ lines: Array.from({ length: 2100 }, (_, i) => line("info", `l${i}`)) })
    await flushPromises()
    const rows = w.findAll(".log > div")
    expect(rows).toHaveLength(2000)
    expect(rows[rows.length - 1].text()).toContain("l2099")
    w.unmount()
  })

  it("scrolls to the end only when the box was at the bottom", async () => {
    const w = mount(LogViewer, { attachTo: document.body })
    await flushPromises()
    await w.findAll("label.check input")[0].setValue(true)
    const box = w.find(".log").element as HTMLElement
    Object.defineProperty(box, "scrollHeight", { configurable: true, value: 1000 })
    Object.defineProperty(box, "clientHeight", { configurable: true, value: 200 })
    box.scrollTop = 800
    h.on.get("log")!({ lines: [line("info", "a")] })
    await flushPromises()
    expect(box.scrollTop).toBe(1000)
    box.scrollTop = 100
    h.on.get("log")!({ lines: [line("info", "b")] })
    await flushPromises()
    expect(box.scrollTop).toBe(100)
    w.unmount()
  })

  it("shows the level labels, the styled filter, and the note", async () => {
    const w = mount(LogViewer)
    await flushPromises()
    expect(w.findAll(".seg button").map((b) => b.text())).toEqual([
      "All",
      "Debug+",
      "Info+",
      "Warn+",
      "Error",
    ])
    expect(w.find(".search input").classes()).toContain("input")
    expect(w.text()).toContain("The log shows the lines since the gateway started.")
    expect(w.find(".log").classes()).toContain("log-view")
    w.unmount()
  })

  it("shows the date of a line that is not from today", async () => {
    vi.mocked(api).mockResolvedValue({
      lines: [{ time: local(13, 59, 58, 19), level: "info", msg: "old", attrs: "" }],
    })
    const w = mount(LogViewer)
    await flushPromises()
    expect(w.find(".log").text()).toContain("2026-11-19 13:59:58")
    w.unmount()
  })

  it("wraps the lines by default on a phone", async () => {
    phone(true)
    const w = mount(LogViewer)
    await flushPromises()
    expect(w.find(".log").classes()).toContain("is-wrap")
    const wrap = w.findAll("label.check").find((l) => l.text() === "Wrap lines")!
    await wrap.find("input").setValue(false)
    expect(w.find(".log").classes()).not.toContain("is-wrap")
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

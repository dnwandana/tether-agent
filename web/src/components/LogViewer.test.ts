import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"

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

let user: ReturnType<typeof userEvent.setup>
beforeEach(() => {
  h.on.clear()
  phone(false)
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
  })
  vi.setSystemTime(new Date(2026, 10, 20, 15, 0, 0))
  vi.mocked(api).mockReset().mockResolvedValue(logs)
  vi.mocked(setFollowLogs).mockReset()
  user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
})
afterEach(() => vi.useRealTimers())

const box = () => screen.getByRole("region", { name: "Log lines" })
const rows = () => Array.from(box().querySelectorAll("[data-line]"))
const levels = () => screen.getByRole("group", { name: "Level" })
const level = (name: string) => within(levels()).getByRole("button", { name })
const follow = () => screen.getByRole("checkbox", { name: "Follow" })
const wrap = () => screen.getByRole("checkbox", { name: "Wrap lines" })
async function mk() {
  const r = render(LogViewer)
  await flushPromises()
  return r
}

describe("LogViewer", () => {
  it("loads the last 500 lines and shows them as text", async () => {
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/logs?level=&q=&limit=500")
    const text = box().textContent!
    expect(text).toContain("13:59:58")
    expect(text).toContain("ERROR")
    expect(text).toContain("cron: run failed")
    expect(box().querySelector('[data-level="error"]')).not.toBeNull()
  })

  it("maps the levels of Go to four tones", async () => {
    vi.mocked(api).mockResolvedValue({
      lines: ["INFO", "WARN", "ERROR", "DEBUG"].map((lv) => ({
        time: local(1, 2, 3),
        level: lv,
        msg: "m",
        attrs: "",
      })),
    })
    await mk()
    const tone: Record<string, string> = {
      info: "text-info",
      warn: "text-warning",
      error: "text-destructive",
      debug: "text-muted-foreground",
    }
    for (const [k, cls] of Object.entries(tone)) {
      const el = box().querySelector(`[data-level="${k}"]`)
      expect(el).not.toBeNull()
      expect(el!.classList.contains(cls)).toBe(true)
    }
  })

  it("marks the active level button as pressed", async () => {
    await mk()
    expect(level("All").getAttribute("aria-pressed")).toBe("true")
    await user.click(level("Warn+"))
    expect(level("Warn+").getAttribute("aria-pressed")).toBe("true")
    expect(level("All").getAttribute("aria-pressed")).toBe("false")
  })

  it("keeps the level when the pressed button is tapped again", async () => {
    await mk()
    await user.click(level("All"))
    expect(level("All").getAttribute("aria-pressed")).toBe("true")
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("drops a slow reply for an old level", async () => {
    const slow = { lines: [line("info", "old level")] }
    const fresh = { lines: [line("warn", "new level")] }
    let first!: (v: unknown) => void
    vi.mocked(api).mockReset()
    vi.mocked(api)
      .mockImplementationOnce(() => new Promise((r) => (first = r)))
      .mockResolvedValueOnce(fresh)
    const r = render(LogViewer)
    await user.click(level("Warn+"))
    await flushPromises()
    expect(r.container.textContent).toContain("new level")
    first(slow)
    await flushPromises()
    expect(r.container.textContent).toContain("new level")
    expect(r.container.textContent).not.toContain("old level")
  })

  it("filters by level and by text", async () => {
    await mk()
    await user.click(level("Warn+"))
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=&limit=500")
    await user.type(screen.getByRole("searchbox", { name: "Filter" }), "401")
    expect(api).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(250)
    expect(api).toHaveBeenLastCalledWith("GET", "/api/logs?level=warn&q=401&limit=500")
  })

  it("follows the log by the log event until Follow is off", async () => {
    await mk()
    await user.click(follow())
    expect(follow().getAttribute("aria-checked")).toBe("true")
    expect(setFollowLogs).toHaveBeenLastCalledWith(true)
    h.on.get("log")!({ lines: [line("info", "new line")] })
    await flushPromises()
    expect(box().textContent).toContain("new line")
    vi.advanceTimersByTime(6000)
    expect(api).toHaveBeenCalledTimes(1)
    await user.click(follow())
    expect(setFollowLogs).toHaveBeenLastCalledWith(false)
    expect(h.on.has("log")).toBe(false)
  })

  it("stops the follow of the stream when it unmounts", async () => {
    const r = await mk()
    await user.click(follow())
    r.unmount()
    expect(setFollowLogs).toHaveBeenLastCalledWith(false)
  })

  it("filters the followed lines by level and by text on the page", async () => {
    await mk()
    await user.click(level("Warn+"))
    await flushPromises()
    await user.click(follow())
    h.on.get("log")!({
      lines: [line("info", "quiet"), line("WARN", "disk low"), line("error", "other fail")],
    })
    await flushPromises()
    const text = box().textContent!
    expect(text).not.toContain("quiet")
    expect(text).toContain("disk low")
    expect(text).toContain("other fail")
  })

  it("keeps at most 2,000 lines", async () => {
    await mk()
    await user.click(follow())
    h.on.get("log")!({ lines: Array.from({ length: 2100 }, (_, i) => line("info", `l${i}`)) })
    await flushPromises()
    expect(rows()).toHaveLength(2000)
    expect(rows()[1999].textContent).toContain("l2099")
  })

  it("scrolls to the end only when the box was at the bottom", async () => {
    await mk()
    await user.click(follow())
    const el = box()
    Object.defineProperty(el, "scrollHeight", { configurable: true, value: 1000 })
    Object.defineProperty(el, "clientHeight", { configurable: true, value: 200 })
    el.scrollTop = 800
    h.on.get("log")!({ lines: [line("info", "a")] })
    await flushPromises()
    expect(el.scrollTop).toBe(1000)
    el.scrollTop = 100
    h.on.get("log")!({ lines: [line("info", "b")] })
    await flushPromises()
    expect(el.scrollTop).toBe(100)
  })

  it("shows the level labels, the filter input, and the note", async () => {
    await mk()
    expect(levels().getAttribute("data-slot")).toBe("toggle-group")
    expect(
      within(levels())
        .getAllByRole("button")
        .map((b) => b.textContent!.trim()),
    ).toEqual(["All", "Debug+", "Info+", "Warn+", "Error"])
    expect(screen.getByRole("searchbox", { name: "Filter" }).getAttribute("data-slot")).toBe(
      "input",
    )
    expect(screen.getByText("The log shows the lines since the gateway started.")).toBeTruthy()
    expect(follow().getAttribute("data-slot")).toBe("checkbox")
  })

  it("shows the date of a line that is not from today", async () => {
    vi.mocked(api).mockResolvedValue({
      lines: [{ time: local(13, 59, 58, 19), level: "info", msg: "old", attrs: "" }],
    })
    await mk()
    expect(box().textContent).toContain("2026-11-19 13:59:58")
  })

  it("wraps the lines by default on a phone", async () => {
    phone(true)
    await mk()
    expect(box().getAttribute("data-wrap")).toBe("true")
    expect(wrap().getAttribute("aria-checked")).toBe("true")
    await user.click(wrap())
    expect(box().getAttribute("data-wrap")).toBeNull()
  })

  it("shows a message when no line matches", async () => {
    vi.mocked(api).mockResolvedValue({ lines: [] })
    await mk()
    expect(box().textContent!.trim()).toBe("No lines match.")
    expect(rows()).toHaveLength(0)
  })
})

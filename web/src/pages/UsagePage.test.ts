import { describe, it, expect, vi, beforeEach, beforeAll, afterAll } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

// The page has a fallback for a browser with no ResizeObserver. Remove the setup stub for this file.
const savedObserver = (globalThis as any).ResizeObserver
beforeAll(() => {
  delete (globalThis as any).ResizeObserver
})
afterAll(() => {
  if (savedObserver) (globalThis as any).ResizeObserver = savedObserver
})

const h = vi.hoisted(() => ({
  live: null as null | { load: () => Promise<void>; opts?: { events?: string[] } },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/live", async () => {
  const actual = await vi.importActual<object>("../lib/live")
  const { onMounted, ref } = await import("vue")
  return {
    ...actual,
    useLiveData: (load: () => Promise<void>, opts?: any) => {
      h.live = { load, opts }
      onMounted(() => void load())
      return { stale: ref(false), updatedAt: ref(null), refresh: load }
    },
  }
})
import { api } from "../api/client"
import { session } from "../stores/session"
import UsagePage from "./UsagePage.vue"

const Blank = { render: () => null }
const usage = {
  range: "30d",
  session_id: "",
  rows: [
    {
      model: "glm-4.6",
      provider: "primary · zai",
      calls: 1102,
      in: 27900000,
      cached: 24100000,
      out: 940000,
      hit_pct: 86,
      hit_known: true,
      tok_per_sec: 62,
      usd: 12.8812,
      priced: true,
    },
    {
      model: "deepseek-chat",
      provider: "backup · deepseek",
      calls: 96,
      in: 2600000,
      cached: 1690000,
      out: 110000,
      hit_pct: 65,
      hit_known: true,
      tok_per_sec: 48,
      usd: 1.1203,
      priced: false,
    },
  ],
  total_usd: 14.0015,
  unpriced: true,
  compactions: 12,
}
const daily = {
  range: "30d",
  from: "2026-11-19",
  to: "2026-11-20",
  days: [
    { day: "2026-11-19", model: "glm-4.6", usd: 0.5, unpriced: false, in: 1, out: 1 },
    { day: "2026-11-19", model: "deepseek-chat", usd: 0.25, unpriced: true, in: 1, out: 1 },
    { day: "2026-11-20", model: "glm-4.6", usd: 1.0, unpriced: false, in: 1, out: 1 },
  ],
}
const cron = {
  range: "30d",
  jobs: [{ job_id: "j1", job_name: "nightly-backup", usd: 0.312, unpriced: false }],
  flagged: [
    { job_id: "j1", job_name: "nightly-backup", at: "2026-11-19T01:00:00Z", usd: 0.9, median: 0.3 },
  ],
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/usage", name: "usage", component: UsagePage },
      { path: "/jobs/:id", name: "job", component: Blank },
    ],
  })
  await router.push("/usage")
  const user = userEvent.setup()
  const r = render(UsagePage, { global: { plugins: [router], stubs: { LiveHeader: true } } })
  await flushPromises()
  return { ...r, user }
}

const reply = (d: object, c: object = cron, u: object = usage) =>
  vi
    .mocked(api)
    .mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/usage/daily") ? d : p.startsWith("/api/usage/cron") ? c : u) as any,
    )
const dayRow = (model: string, usd: number) => ({
  day: "2026-11-19",
  model,
  usd,
  unpriced: false,
  in: 1,
  out: 1,
})
const d = (day: string) => ({ day, model: "m", usd: 1, unpriced: false, in: 1, out: 1 })
const btn = (name: string) => screen.getByRole("button", { name })
// The chart bars: each bar is a button named "<label>: $<total>".
const bars = () => screen.queryAllByRole("button", { name: /: \$\d/ })
const axis = (c: HTMLElement, a: "x" | "y") =>
  [...c.querySelectorAll(`[data-axis="${a}"] > span`)].map((s) => s.textContent!.trim())
const labels = (c: HTMLElement) => axis(c, "x")
const ticks = (c: HTMLElement) => axis(c, "y")
const shown = (c: HTMLElement) => labels(c).filter((s) => s !== "")
const bodyRows = (el: HTMLElement) =>
  within(el)
    .getAllByRole("row")
    .filter((r) => within(r).queryAllByRole("cell").length > 0)
const cronRegion = () => screen.getByRole("region", { name: "Cron cost by job" })
const one = (usd: number) => ({
  range: "30d",
  from: "2026-11-19",
  to: "2026-11-19",
  days: [{ ...dayRow("m", usd) }],
})
let cb: (e: { contentRect: { width: number } }[]) => void = () => {}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/usage/daily")
          ? daily
          : p.startsWith("/api/usage/cron")
            ? cron
            : usage) as any,
    )
})

describe("UsagePage", () => {
  it("draws one row for one model under two providers", async () => {
    const r = usage.rows[0]
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/usage/daily")
          ? daily
          : p.startsWith("/api/usage/cron")
            ? cron
            : { ...usage, rows: [r, { ...r, provider: "backup · zai", calls: 7 }] }) as any,
    )
    await mk()
    const rows = screen
      .getAllByRole("row")
      .filter(
        (t) => t.textContent!.includes("glm-4.6") && within(t).queryAllByRole("cell").length > 0,
      )
    expect(rows).toHaveLength(2)
  })

  it("loads the three routes for 30 days and shows the tiles", async () => {
    await mk()
    for (const p of [
      "/api/usage?range=30d",
      "/api/usage/daily?range=30d",
      "/api/usage/cron?range=30d",
    ])
      expect(api).toHaveBeenCalledWith("GET", p)
    for (const s of [
      "$14.00+",
      "96 calls have no price",
      "1198",
      "across 2 models",
      "30.5M · 1.05M",
      "85%",
      "12 compactions",
    ])
      expect(screen.getByText(s)).toBeTruthy()
  })

  it("shows the model table with the hit rate and the unpriced mark", async () => {
    await mk()
    const text = document.body.textContent!
    expect(text).toContain("86%")
    expect(text).toContain("$1.1203+")
    expect(text).toContain("💸 1")
    expect(
      within(cronRegion()).getByRole("link", { name: "nightly-backup" }).getAttribute("href"),
    ).toBe("/jobs/j1")
  })

  it("draws one stacked bar for each day", async () => {
    await mk()
    const b = bars()
    expect(b).toHaveLength(2)
    expect([...b[0].querySelectorAll("i")].map((i) => i.getAttribute("style"))).toEqual([
      "height: 50%;",
      "height: 25%;",
    ])
    expect(b[1].querySelector("i")!.getAttribute("data-series")).toBe("s1")
  })

  it("loads again for another range", async () => {
    const { user } = await mk()
    await user.click(
      within(screen.getByRole("group", { name: "Range" })).getByRole("button", { name: "Today" }),
    )
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/usage?range=today")
    expect(api).toHaveBeenCalledWith("GET", "/api/usage/daily?range=today")
  })

  it("shows the empty text when there are no rows", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/usage/daily")
          ? { range: "today", from: "", to: "", days: [] }
          : p.startsWith("/api/usage/cron")
            ? { range: "today", jobs: [], flagged: [] }
            : { ...usage, rows: [] }) as any,
    )
    const { user } = await mk()
    expect(screen.getByText("No usage in this range.")).toBeTruthy()
    await user.click(btn("Show 30 days"))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/usage?range=30d")
  })

  it("groups the models after the 5th as Other with six series classes", async () => {
    reply({
      range: "30d",
      from: "2026-11-19",
      to: "2026-11-19",
      days: ["a", "b", "c", "d", "e", "f", "g"].map((m, i) => dayRow(m, 7 - i)),
    })
    const { container } = await mk()
    expect(
      [...container.querySelectorAll("[data-legend]")].map((l) => [
        l.textContent!.trim(),
        l.querySelector("i")!.getAttribute("data-series"),
      ]),
    ).toEqual([
      ["a", "s1"],
      ["b", "s2"],
      ["c", "s3"],
      ["d", "s4"],
      ["e", "s5"],
      ["Other", "s6"],
    ])
    expect(bars()[0].querySelectorAll('i[data-series="s6"]')).toHaveLength(1)
  })

  it("puts the dates on the x-axis and makes each bar a button", async () => {
    const { container } = await mk()
    expect(labels(container)).toEqual(["Nov 19", "Nov 20"])
    const bar = bars()[0]
    expect(bar.tagName).toBe("BUTTON")
    expect(bar.getAttribute("type")).toBe("button")
    expect(bar.getAttribute("aria-label")).toBe("Nov 19: $0.75")
  })

  it("marks a day with a flagged cron run", async () => {
    await mk()
    const b = bars()
    expect(b[0].querySelector("[data-spike]")!.textContent).toBe("💸")
    expect(b[1].querySelector("[data-spike]")).toBeNull()
  })

  it("shows the days as a table", async () => {
    const { container, user } = await mk()
    await user.click(btn("View as table"))
    const t = screen.getByRole("table", { name: "Spend per day" })
    expect(
      within(t)
        .getAllByRole("columnheader")
        .map((x) => x.textContent!.trim()),
    ).toEqual(["Day", "Total", "Tokens in · out", "Top model"])
    expect(
      within(bodyRows(t)[0])
        .getAllByRole("cell")
        .map((x) => x.textContent!.trim()),
    ).toEqual(["Nov 19", "$0.75", "2 · 2", "glm-4.6"])
    expect(container.querySelector('[data-chart="bars"]')).toBeNull()
  })

  it("groups the range All by week above 60 days and by month above 365 days", async () => {
    reply({
      range: "all",
      from: "2026-09-01",
      to: "2026-11-09",
      days: [d("2026-09-01"), d("2026-09-02"), d("2026-11-09")],
    })
    const { container, user } = await mk()
    await user.click(btn("All"))
    await flushPromises()
    expect(labels(container)).toHaveLength(11)
    expect(labels(container).filter(Boolean)).toEqual([
      "Aug 31",
      "Sep 14",
      "Sep 28",
      "Oct 12",
      "Oct 26",
      "Nov 9",
    ])
    reply({
      range: "all",
      from: "2025-01-05",
      to: "2026-02-01",
      days: [d("2025-01-05"), d("2025-01-20"), d("2026-02-01")],
    })
    await user.click(btn("7 days"))
    await user.click(btn("All"))
    await flushPromises()
    expect(labels(container)).toHaveLength(14)
    expect(labels(container).filter(Boolean)).toEqual([
      "Jan 2025",
      "Mar 2025",
      "May 2025",
      "Jul 2025",
      "Sep 2025",
      "Nov 2025",
      "Feb 2026",
    ])
  })

  it("hides the chart and the cron card for This session and shows the title", async () => {
    session.title = "Release prep"
    const { container, user } = await mk()
    await user.click(btn("This session"))
    await flushPromises()
    expect(container.querySelector('[data-chart="bars"]')).toBeNull()
    expect(document.body.textContent).not.toContain("Cron cost by job")
    expect(document.body.textContent).toContain("This session: Release prep")
    session.title = ""
  })

  it("lists the flagged runs and adds a total row to the cron table", async () => {
    await mk()
    expect(screen.getByRole("region", { name: "Flagged runs" }).textContent).toContain(
      "2026-11-19 · $0.9000 · median $0.3000",
    )
    const rows = bodyRows(cronRegion())
    expect(rows[rows.length - 1].textContent).toContain("Total · 1 job")
    expect(rows[rows.length - 1].textContent).toContain("$0.3120")
  })

  it("shows a deleted job as text", async () => {
    reply(daily, {
      ...cron,
      jobs: [{ job_id: "j9", job_name: "", usd: 0.1, unpriced: false }],
      flagged: [],
    })
    await mk()
    expect(screen.queryAllByRole("link").some((a) => a.getAttribute("href") === "/jobs/j9")).toBe(
      false,
    )
    expect(cronRegion().textContent).toContain("Deleted job")
  })

  it("fills each day from from to to, and shows one bar for Today", async () => {
    reply({ ...daily, from: "2026-11-17", to: "2026-11-21" })
    const { user } = await mk()
    expect(bars().map((b) => b.getAttribute("aria-label"))).toEqual([
      "Nov 17: $0.00",
      "Nov 18: $0.00",
      "Nov 19: $0.75",
      "Nov 20: $1.00",
      "Nov 21: $0.00",
    ])
    await user.click(btn("View as table"))
    expect(bodyRows(screen.getByRole("table", { name: "Spend per day" }))).toHaveLength(5)
    reply({ range: "today", from: "2026-11-20", to: "2026-11-20", days: [d("2026-11-20")] })
    await user.click(btn("View as chart"))
    await user.click(btn("Today"))
    await flushPromises()
    expect(bars()).toHaveLength(1)
  })

  it("spaces the labels by the width of the bar area, not the card", async () => {
    reply({ ...daily, from: "2026-10-21", to: "2026-11-20" })
    ;(globalThis as any).ResizeObserver = class {
      constructor(f: typeof cb) {
        cb = f
      }
      observe() {}
      disconnect() {}
    }
    const spy = vi.spyOn(Element.prototype, "clientWidth", "get").mockImplementation(function (
      this: Element,
    ) {
      return this.getAttribute("data-chart") === "bars" ? 300 : 0
    })
    try {
      const { container } = await mk()
      cb([{ contentRect: { width: 600 } }])
      await flushPromises()
      expect(shown(container)).toHaveLength(4) // 300 px of bars: step 6, not step 3
    } finally {
      spy.mockRestore()
      delete (globalThis as any).ResizeObserver
    }
  })

  it("shows round ticks for a normal, a small, and a zero max", async () => {
    expect(ticks((await mk()).container)).toEqual(["$1", "$0.50", "$0"])
    reply(one(0.0013))
    expect(ticks((await mk()).container)).toEqual(["$0.002", "$0.001", "$0"])
    reply(one(0))
    expect(ticks((await mk()).container)).toEqual(["$1", "$0.50", "$0"])
  })

  it("spaces the labels by the measured width and keeps two ticks on a short chart", async () => {
    expect("ResizeObserver" in globalThis).toBe(false)
    reply({ ...daily, from: "2026-10-21", to: "2026-11-20" })
    // 31 bars at the default 600 px: step 3. A middle label keeps 1.5 steps from each end,
    // because the first and the last label grow inward by their full width.
    const first = await mk()
    expect(shown(first.container)).toHaveLength(9)
    const at = labels(first.container)
      .map((s, i) => (s ? i : -1))
      .filter((i) => i >= 0)
    expect(at).toEqual([0, 6, 9, 12, 15, 18, 21, 24, 30])
    first.unmount()
    ;(globalThis as any).ResizeObserver = class {
      constructor(f: typeof cb) {
        cb = f
      }
      observe() {}
      disconnect() {}
    }
    const spy = vi.spyOn(Element.prototype, "clientHeight", "get").mockImplementation(function (
      this: Element,
    ) {
      return this.getAttribute("data-chart") === "bars" ? 120 : 0
    })
    try {
      const w = await mk()
      cb([{ contentRect: { width: 300 } }])
      await flushPromises()
      expect(shown(w.container)).toHaveLength(4) // step 6
      cb([{ contentRect: { width: 0 } }])
      await flushPromises()
      expect(shown(w.container)).toHaveLength(4) // a width of 0 keeps the last width
      expect(ticks(w.container)).toEqual(["$1", "$0"])
    } finally {
      spy.mockRestore()
      delete (globalThis as any).ResizeObserver
    }
  })

  it("opens the tooltip on hover and click, and closes it on Esc and an outside tap", async () => {
    const { user } = await mk()
    await user.hover(bars()[0])
    for (const s of [
      "Nov 19",
      "glm-4.6",
      "$0.5000",
      "deepseek-chat",
      "$0.2500",
      "Total",
      "$0.7500+",
    ])
      expect(screen.getByRole("tooltip").textContent).toContain(s)
    await user.click(bars()[1])
    await user.click(bars()[1])
    expect(screen.getByRole("tooltip").textContent).toContain("Nov 20")
    await user.keyboard("{Escape}")
    expect(screen.queryByRole("tooltip")).toBeNull()
    await user.click(bars()[1])
    await user.click(document.body)
    expect(screen.queryByRole("tooltip")).toBeNull()
  })

  it("marks an unpriced job cost in the cron table", async () => {
    reply(daily, { ...cron, jobs: [{ ...cron.jobs[0], unpriced: true }] })
    await mk()
    const rows = bodyRows(cronRegion())
    expect(rows[0].textContent).toContain("$0.3120+")
    expect(rows[rows.length - 1].textContent).toContain("$0.3120+")
  })

  it("reloads only on a focus and a reconnect", async () => {
    await mk()
    expect(h.live!.opts?.events).toBeUndefined()
  })

  it("uses no legacy class", async () => {
    const { container } = await mk()
    expect(
      container.querySelector(
        ".page-h, .seg, .grid-4, .tile, .eyebrow, .sbars, .sbar, .axis-x, .axis-y, .lg, .usage-tip, .popover, .section-h, .cron-cost, .flagged, .li, .btn, .s1",
      ),
    ).toBeNull()
  })
})

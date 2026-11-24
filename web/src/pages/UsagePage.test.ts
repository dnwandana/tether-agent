import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

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
  const w = mount(UsagePage, { global: { plugins: [router], stubs: { LiveHeader: true } } })
  await flushPromises()
  return w
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
const labels = (w: any) => w.findAll(".axis-x span").map((s: any) => s.text())
const ticks = (w: any) => w.findAll(".axis-y span").map((s: any) => s.text())
const shown = (w: any) => w.findAll(".axis-x span").filter((s: any) => s.text() !== "")
const one = (usd: number) => ({
  range: "30d",
  from: "2026-11-19",
  to: "2026-11-19",
  days: [{ ...dayRow("m", usd) }],
})
let cb: (e: { contentRect: { width: number } }[]) => void = () => {}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

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
    const w = await mk()
    expect(w.findAll("tbody tr").filter((t) => t.text().includes("glm-4.6"))).toHaveLength(2)
  })

  it("loads the three routes for 30 days and shows the tiles", async () => {
    const w = await mk()
    for (const p of [
      "/api/usage?range=30d",
      "/api/usage/daily?range=30d",
      "/api/usage/cron?range=30d",
    ])
      expect(api).toHaveBeenCalledWith("GET", p)
    const tiles = w.find(".grid-4").text()
    for (const s of [
      "$14.00+",
      "96 calls have no price",
      "1198",
      "across 2 models",
      "30.5M · 1.05M",
      "85%",
      "12 compactions",
    ])
      expect(tiles).toContain(s)
  })

  it("shows the model table with the hit rate and the unpriced mark", async () => {
    const w = await mk()
    expect(w.text()).toContain("86%")
    expect(w.text()).toContain("$1.1203+")
    expect(w.text()).toContain("💸 1")
    expect(w.find('a[href="/jobs/j1"]').text()).toBe("nightly-backup")
  })

  it("draws one stacked bar for each day", async () => {
    const w = await mk()
    const bars = w.findAll(".sbar")
    expect(bars).toHaveLength(2)
    expect(bars[0].findAll("i").map((i) => i.attributes("style"))).toEqual([
      "height: 50%;",
      "height: 25%;",
    ])
    expect(bars[1].find("i").classes()).toContain("s1")
  })

  it("loads again for another range", async () => {
    const w = await mk()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Today")!
      .trigger("click")
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
    const w = await mk()
    expect(w.text()).toContain("No usage in this range.")
    await btn(w, "Show 30 days").trigger("click")
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
    const w = await mk()
    expect(w.findAll(".lg").map((l: any) => [l.text(), l.find("i").classes()[0]])).toEqual([
      ["a", "s1"],
      ["b", "s2"],
      ["c", "s3"],
      ["d", "s4"],
      ["e", "s5"],
      ["Other", "s6"],
    ])
    expect(w.find(".sbar").findAll("i.s6")).toHaveLength(1)
  })

  it("puts the dates on the x-axis and makes each bar a button", async () => {
    const w = await mk()
    expect(w.findAll(".axis-x span").map((s: any) => s.text())).toEqual(["Nov 19", "Nov 20"])
    const bar = w.find(".sbar")
    expect(bar.element.tagName).toBe("BUTTON")
    expect(bar.attributes("type")).toBe("button")
    expect(bar.attributes("aria-label")).toBe("Nov 19: $0.75")
  })

  it("marks a day with a flagged cron run", async () => {
    const w = await mk()
    const bars = w.findAll(".sbar")
    expect(bars[0].find(".spike").text()).toBe("💸")
    expect(bars[1].find(".spike").exists()).toBe(false)
  })

  it("shows the days as a table", async () => {
    const w = await mk()
    await btn(w, "View as table").trigger("click")
    const t = w.find("table.days")
    expect(t.findAll("th").map((x: any) => x.text())).toEqual([
      "Day",
      "Total",
      "Tokens in · out",
      "Top model",
    ])
    expect(
      t
        .findAll("tbody tr")[0]
        .findAll("td")
        .map((x: any) => x.text()),
    ).toEqual(["Nov 19", "$0.75", "2 · 2", "glm-4.6"])
    expect(w.find(".sbars").exists()).toBe(false)
  })

  it("groups the range All by week above 60 days and by month above 365 days", async () => {
    reply({
      range: "all",
      from: "2026-09-01",
      to: "2026-11-09",
      days: [d("2026-09-01"), d("2026-09-02"), d("2026-11-09")],
    })
    const w = await mk()
    await btn(w, "All").trigger("click")
    await flushPromises()
    expect(labels(w)).toHaveLength(11)
    expect(labels(w).filter(Boolean)).toEqual([
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
    await btn(w, "7 days").trigger("click")
    await btn(w, "All").trigger("click")
    await flushPromises()
    expect(labels(w)).toHaveLength(14)
    expect(labels(w).filter(Boolean)).toEqual([
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
    const w = await mk()
    await btn(w, "This session").trigger("click")
    await flushPromises()
    expect(w.find(".sbars").exists()).toBe(false)
    expect(w.text()).not.toContain("Cron cost by job")
    expect(w.text()).toContain("This session: Release prep")
    session.title = ""
  })

  it("lists the flagged runs and adds a total row to the cron table", async () => {
    const w = await mk()
    expect(w.find(".flagged").text()).toContain("2026-11-19 · $0.9000 · median $0.3000")
    const rows = w.findAll(".cron-cost tbody tr")
    expect(rows[rows.length - 1].text()).toContain("Total · 1 job")
    expect(rows[rows.length - 1].text()).toContain("$0.3120")
  })

  it("shows a deleted job as text", async () => {
    reply(daily, {
      ...cron,
      jobs: [{ job_id: "j9", job_name: "", usd: 0.1, unpriced: false }],
      flagged: [],
    })
    const w = await mk()
    expect(w.find('a[href="/jobs/j9"]').exists()).toBe(false)
    expect(w.find(".cron-cost").text()).toContain("Deleted job")
  })

  it("fills each day from from to to, and shows one bar for Today", async () => {
    reply({ ...daily, from: "2026-11-17", to: "2026-11-21" })
    const w = await mk()
    expect(w.findAll(".sbar").map((b: any) => b.attributes("aria-label"))).toEqual([
      "Nov 17: $0.00",
      "Nov 18: $0.00",
      "Nov 19: $0.75",
      "Nov 20: $1.00",
      "Nov 21: $0.00",
    ])
    await btn(w, "View as table").trigger("click")
    expect(w.findAll("table.days tbody tr")).toHaveLength(5)
    reply({ range: "today", from: "2026-11-20", to: "2026-11-20", days: [d("2026-11-20")] })
    await btn(w, "View as chart").trigger("click")
    await btn(w, "Today").trigger("click")
    await flushPromises()
    expect(w.findAll(".sbar")).toHaveLength(1)
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
      return this.classList.contains("sbars") ? 300 : 0
    })
    try {
      const w = await mk()
      cb([{ contentRect: { width: 600 } }])
      await flushPromises()
      expect(shown(w)).toHaveLength(4) // 300 px of bars: step 6, not step 3
    } finally {
      spy.mockRestore()
      delete (globalThis as any).ResizeObserver
    }
  })

  it("shows round ticks for a normal, a small, and a zero max", async () => {
    expect(ticks(await mk())).toEqual(["$1", "$0.50", "$0"])
    reply(one(0.0013))
    expect(ticks(await mk())).toEqual(["$0.002", "$0.001", "$0"])
    reply(one(0))
    expect(ticks(await mk())).toEqual(["$1", "$0.50", "$0"])
  })

  it("spaces the labels by the measured width and keeps two ticks on a short chart", async () => {
    expect("ResizeObserver" in globalThis).toBe(false)
    reply({ ...daily, from: "2026-10-21", to: "2026-11-20" })
    // 31 bars at the default 600 px: step 3. A middle label keeps 1.5 steps from each end,
    // because the first and the last label grow inward by their full width.
    const first = await mk()
    expect(shown(first)).toHaveLength(9)
    const at = first
      .findAll(".axis-x span")
      .map((s: any, i: number) => (s.text() ? i : -1))
      .filter((i: number) => i >= 0)
    expect(at).toEqual([0, 6, 9, 12, 15, 18, 21, 24, 30])
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
      return this.classList.contains("sbars") ? 120 : 0
    })
    try {
      const w = await mk()
      cb([{ contentRect: { width: 300 } }])
      await flushPromises()
      expect(shown(w)).toHaveLength(4) // step 6
      cb([{ contentRect: { width: 0 } }])
      await flushPromises()
      expect(shown(w)).toHaveLength(4) // a width of 0 keeps the last width
      expect(w.findAll(".axis-y span").map((s: any) => s.text())).toEqual(["$1", "$0"])
    } finally {
      spy.mockRestore()
      delete (globalThis as any).ResizeObserver
    }
  })

  it("opens the tooltip on hover and click, and closes it on Esc and an outside tap", async () => {
    const w = await mk()
    const bars = w.findAll(".sbar")
    await bars[0].trigger("pointerenter")
    const tip = () => w.find(".usage-tip")
    for (const s of [
      "Nov 19",
      "glm-4.6",
      "$0.5000",
      "deepseek-chat",
      "$0.2500",
      "Total",
      "$0.7500+",
    ])
      expect(tip().text()).toContain(s)
    await bars[1].trigger("click")
    await bars[1].trigger("click")
    expect(tip().text()).toContain("Nov 20")
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    await flushPromises()
    expect(tip().exists()).toBe(false)
    await bars[1].trigger("click")
    document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }))
    await flushPromises()
    expect(tip().exists()).toBe(false)
  })

  it("marks an unpriced job cost in the cron table", async () => {
    reply(daily, { ...cron, jobs: [{ ...cron.jobs[0], unpriced: true }] })
    const w = await mk()
    const rows = w.findAll(".cron-cost tbody tr")
    expect(rows[0].text()).toContain("$0.3120+")
    expect(rows[rows.length - 1].text()).toContain("$0.3120+")
  })

  it("reloads only on a focus and a reconnect", async () => {
    await mk()
    expect(h.live!.opts?.events).toBeUndefined()
  })
})

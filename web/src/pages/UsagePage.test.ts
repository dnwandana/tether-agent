import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
import { api } from "../api/client"
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
  days: [
    { day: "2026-11-19", model: "glm-4.6", usd: 0.5, unpriced: false, in: 1, out: 1 },
    { day: "2026-11-19", model: "deepseek-chat", usd: 0.25, unpriced: true, in: 1, out: 1 },
    { day: "2026-11-20", model: "glm-4.6", usd: 1.0, unpriced: false, in: 1, out: 1 },
  ],
}
const cron = {
  range: "30d",
  jobs: [{ job_id: "j1", job_name: "nightly-backup", usd: 0.312 }],
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
  const w = mount(UsagePage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}

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
    expect(bars[0].attributes("title")).toBe("2026-11-19: $0.7500 (+ unpriced calls)")
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
          ? { range: "today", days: [] }
          : p.startsWith("/api/usage/cron")
            ? { range: "today", jobs: [], flagged: [] }
            : { ...usage, rows: [] }) as any,
    )
    const w = await mk()
    expect(w.text()).toContain("No model calls in this range.")
  })
})

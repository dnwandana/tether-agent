import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, runCommand } from "../api/client"
import JobsPage from "./JobsPage.vue"

const Blank = { render: () => null }
const job = (o: object) => ({
  id: "",
  icon: "✅",
  name: "",
  schedule: "0 6 * * *",
  next_run: "2026-11-21T06:00:00Z",
  last_run: "",
  last_status: "ok",
  fail_streak: 0,
  ok: 7,
  last_cost_usd: 0.0102,
  last_cost_known: true,
  enabled: true,
  watch: false,
  draft_only: false,
  pause_reason: "",
  history_off: false,
  last7: Array(7).fill("ok"),
  ...o,
})
const held = (o: object) => ({
  id: "",
  kind: "hold",
  job_id: "j",
  job_name: "",
  target: "t",
  target_title: "",
  text: "",
  reason: "",
  sources: [],
  created: "2026-11-20T08:00:00Z",
  actions: [],
  can_post: true,
  ...o,
})
const data: Record<string, unknown> = {
  "/api/brief": {
    lines: [
      {
        kind: "failing",
        name: "gh-notify",
        detail: "failing · 3 runs in a row",
        count: 3,
        age_seconds: 18000,
        cost_usd: 0,
        ratio: 0,
        job_id: "j2",
        session_id: "",
      },
      {
        kind: "goal",
        name: "ship it",
        detail: "waiting",
        count: 0,
        age_seconds: 60,
        cost_usd: 0,
        ratio: 0,
        job_id: "",
        session_id: "s9",
      },
    ],
    runs_ok: 41,
    runs_total: 44,
    history_off: false,
    spend_usd: 0.731,
    spend_unpriced: false,
    default_deliver: "origin",
  },
  "/api/jobs": [
    job({ id: "j1", name: "nightly-backup" }),
    job({
      id: "j4",
      icon: "⏸",
      name: "report-weekly",
      enabled: false,
      ok: 2,
      last7: ["ok", "error", "ok", "stopped"],
    }),
  ],
  "/api/held": [
    held({
      id: "h1",
      kind: "hold",
      job_name: "price-watch",
      target_title: "Ops chat",
      text: "Pi 5: €119",
      reason: "A private address.",
      actions: [
        { text: "❌ Drop", data: "drop" },
        { text: '✅ Send to "Ops chat"', data: "post" },
      ],
    }),
    held({
      id: "h2",
      kind: "original",
      job_name: "gh-notify",
      target_title: "DM",
      text: "3 notifications",
      reason: "A masked original.",
      actions: [{ text: "✅ Post the full text there", data: "post" }],
    }),
  ],
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/jobs", name: "jobs", component: JobsPage },
      { path: "/jobs/:id", name: "job", component: Blank },
      { path: "/goals/:session", name: "goal", component: Blank },
    ],
  })
  await router.push("/jobs")
  const w = mount(JobsPage, { global: { plugins: [router], stubs: { JobForm: true } } })
  await flushPromises()
  return w
}
const btns = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)

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
      async (m: string, p: string) => (m === "GET" ? data[p] : { text: "❌ Dropped." }) as any,
    )
  vi.mocked(runCommand).mockReset().mockResolvedValue({
    request_id: "r",
    text: "⏸ Paused **nightly-backup**.",
    buttons: [],
    edit: false,
  })
})

describe("JobsPage", () => {
  it("loads the brief, the jobs, and the held sends", async () => {
    const w = await mk()
    for (const p of ["/api/brief", "/api/jobs", "/api/held"])
      expect(api).toHaveBeenCalledWith("GET", p)
    expect(w.text()).toContain("Jobs: 41 of 44 runs OK in 24 h")
    expect(w.text()).toContain("$0.7310")
    expect(w.text()).toContain("2/7 ok")
    const bars = w.findAll(".runs7")[1].findAll("i")
    expect(bars.map((b) => b.classes().find((c) => c === "bad" || c === "none") ?? "ok")).toEqual([
      "none",
      "none",
      "none",
      "ok",
      "bad",
      "ok",
      "none",
    ])
  })

  it("pauses a job with the command and disables Run now on a paused job", async () => {
    const w = await mk()
    await btns(w, "⏸ Pause")[0].trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/jobs pause j1")
    const run = btns(w, "⚡ Run now")
    expect(run[0].attributes("disabled")).toBeUndefined()
    expect(run[1].attributes("disabled")).toBeDefined()
  })

  it("shows the buttons of each held kind and drops a hold", async () => {
    const w = await mk()
    expect(btns(w, '✅ Send to "Ops chat"')).toHaveLength(1)
    expect(btns(w, "✅ Post the full text there")).toHaveLength(1)
    expect(btns(w, "❌ Drop")).toHaveLength(1)
    await btns(w, "❌ Drop")[0].trigger("click")
    expect(api).toHaveBeenCalledWith("POST", "/api/held/h1", { action: "drop" })
  })

  it("links the brief lines to the job and the goal", async () => {
    const w = await mk()
    const links = w.findAll("a.li").map((a) => a.attributes("href"))
    expect(links).toEqual(["/jobs/j2", "/goals/s9"])
  })

  it("opens the job form in a side panel", async () => {
    const w = await mk()
    await btns(w, "+ New job")[0].trigger("click")
    expect(w.findComponent({ name: "SidePanel" }).props()).toMatchObject({
      open: true,
      title: "New job",
    })
  })
})

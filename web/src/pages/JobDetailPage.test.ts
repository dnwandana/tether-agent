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
import { confirm } from "../stores/ui"
import JobDetailPage from "./JobDetailPage.vue"

const Blank = { render: () => null }
const run = (id: number) => ({
  id,
  started_at: "2026-11-20T07:00:00Z",
  finished_at: "2026-11-20T07:00:02Z",
  duration_ms: 2100,
  status: "error",
  delivery: "failed",
  output: "HTTP 401",
  delivery_err: "",
  session_id: "cron:j2:1",
  cost_usd: 0.0021,
  cost_known: true,
  cost_flag: false,
})
const detail = {
  job: {
    id: "j2",
    icon: "⚠",
    name: "gh-notify",
    schedule: "0 7 * * 1-5",
    next_run: "0001-01-01T00:00:00Z",
    last_run: "2026-11-20T07:00:00Z",
    last_status: "error",
    fail_streak: 3,
    enabled: true,
    watch: false,
    draft_only: false,
    ok: 4,
    last_cost_usd: 0.0021,
    last_cost_known: true,
    pause_reason: "",
    history_off: false,
  },
  prompt: "p",
  script: "",
  workdir: "",
  deliver: "telegram:123",
  model: "",
  effort: "low",
  skills: ["github"],
  toolsets: [],
  no_agent: false,
  continuity: false,
  repeat_left: 0,
  last_output: "",
  last_error: "github: HTTP 401: bad credentials",
  watch_output: "",
  last7: [run(1), run(2), run(3), run(4), run(5), run(6), run(7)].map((r, i) => ({
    ...r,
    status: i < 4 ? "ok" : "error",
  })),
  week_ok: 4,
  week_total: 7,
  week_cost_usd: 0.0192,
  runs_total: 38,
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/jobs", name: "jobs", component: Blank },
      { path: "/jobs/:id", name: "job", component: JobDetailPage },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/jobs/j2")
  const w = mount(JobDetailPage, { global: { plugins: [router], stubs: { JobForm: true } } })
  await flushPromises()
  return { w, router }
}
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
      async (_m: string, p: string) => (p === "/api/jobs/j2" ? detail : [run(1), run(2)]) as any,
    )
  vi.mocked(runCommand).mockReset()
  vi.mocked(confirm).mockReset()
})

describe("JobDetailPage", () => {
  it("loads the job and the first page of runs", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2")
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2/runs?offset=0&limit=10")
    expect(w.find(".bad-banner").text()).toContain("⚠ Failing · 3 in a row.")
    expect(w.find(".bad-banner").text()).toContain("github: HTTP 401: bad credentials")
    expect(w.text()).toContain("4 of 7 OK · $0.0192")
    expect(w.find(".pager").text()).toContain("1–2 of 38")
    await btn(w, "Next ▶").trigger("click")
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2/runs?offset=10&limit=10")
  })

  it("shows the fail streak with the limit and survives null lists", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/jobs/j2"
          ? { ...detail, skills: null, toolsets: null, fail_limit: 5 }
          : [run(1)]) as any,
    )
    const { w } = await mk()
    expect(w.text()).toContain("3 (pauses at 5)")
  })

  it("shows the output of a run", async () => {
    const { w } = await mk()
    await w
      .findAll("button")
      .filter((b) => b.text() === "Output")[0]
      .trigger("click")
    expect(w.text()).toContain("HTTP 401")
    expect(w.find(".cmd").text()).toBe("HTTP 401")
  })

  it("deletes the job after the confirm of the command", async () => {
    vi.mocked(runCommand)
      .mockResolvedValueOnce({
        request_id: "r1",
        text: "Delete gh-notify?\nThe run history goes too.",
        buttons: [[{ text: "🗑 Delete", data: "/jobs delete j2 confirm" }]],
        edit: false,
      })
      .mockResolvedValueOnce({
        request_id: "r2",
        text: "🗑 Deleted gh-notify.",
        buttons: [],
        edit: false,
      })
    vi.mocked(confirm).mockResolvedValue(true)
    const { w, router } = await mk()
    await btn(w, "🗑 Delete…").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenNthCalledWith(1, "/jobs delete j2")
    expect(confirm).toHaveBeenCalledWith({
      title: "Delete gh-notify?",
      body: "The run history goes too.",
      confirmLabel: "🗑 Delete",
      danger: true,
    })
    expect(runCommand).toHaveBeenNthCalledWith(2, "/jobs delete j2 confirm")
    expect(router.currentRoute.value.name).toBe("jobs")
  })

  it("shows the bad schedule banner", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/jobs/j2"
          ? {
              ...detail,
              job: {
                ...detail.job,
                enabled: false,
                pause_reason: "schedule",
                schedule: "0 9 * * 8",
              },
              last_error: "",
            }
          : []) as any,
    )
    const { w } = await mk()
    expect(w.find(".warn-banner").text()).toContain(
      "⏸ Paused: the schedule 0 9 * * 8 is not valid. Edit the schedule to resume.",
    )
    expect(btn(w, "⚡ Run now").attributes("disabled")).toBeDefined()
  })
})

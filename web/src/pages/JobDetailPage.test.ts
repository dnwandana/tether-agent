import { describe, it, expect, vi, beforeEach } from "vitest"
import { defineComponent, h, onMounted } from "vue"
import { render, screen, within, cleanup } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
}))
vi.mock("../stores/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../stores/ui")>()),
  toast: vi.fn(),
  confirm: vi.fn(),
}))
import { api, runCommand } from "../api/client"
import { confirm } from "../stores/ui"
import JobDetailPage from "./JobDetailPage.vue"
import { ApiError } from "../api/client"
import { emitEvents } from "../lib/bus"
import { shortTime } from "../lib/time"
const notFound = () =>
  Object.assign(Object.create(ApiError.prototype), {
    status: 404,
    serverText: "No job with this id.",
    message: "No job with this id.",
  })
const hhmm = (iso: string) =>
  new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })

let mounts = 0
const JobForm = defineComponent({
  name: "JobForm",
  props: ["job"],
  emits: ["saved", "cancel", "dirty"],
  setup(_, { emit }) {
    onMounted(() => mounts++)
    return () =>
      h("button", { type: "button", onClick: () => emit("dirty", true) }, "Make the form dirty")
  },
})
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
    pause_reason: "",
    history_off: false,
    state: "failing",
    cost_7d_usd: 0.0192,
    cost_7d_known: true,
    cost_7d_unpriced: false,
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
  watch_limit_hit: false,
  prompt_masked: false,
  deliver_label: "",
  fail_limit: 0,
  last7: [run(1), run(2), run(3), run(4), run(5), run(6), run(7)].map((r, i) => ({
    ...r,
    status: i < 4 ? "ok" : "error",
  })),
  week_ok: 4,
  week_total: 7,
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
  const r = render(JobDetailPage, { global: { plugins: [router], stubs: { JobForm } } })
  await flushPromises()
  return { ...r, router }
}
const alertWith = (text: string) =>
  screen.getAllByRole("alert").find((a) => a.textContent!.includes(text))!
const phoneMedia = () => {
  window.matchMedia = ((q: string) => ({
    matches: q.includes("max-width: 599px"),
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
}
const outputName = "Output of the run at " + hhmm("2026-11-20T07:00:00Z")

beforeEach(() => {
  mounts = 0
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

const withDetail = (extra: object, runs: unknown[] = [run(1)]) =>
  vi
    .mocked(api)
    .mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/jobs/j2" ? { ...detail, ...extra } : runs) as any,
    )

describe("JobDetailPage", () => {
  it("loads the job and the first page of runs", async () => {
    const user = userEvent.setup()
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2")
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2/runs?offset=0&limit=10")
    expect(alertWith("⚠ Failing · 3 in a row.").textContent).toContain(
      "github: HTTP 401: bad credentials",
    )
    expect(screen.getByText(/4 of 7 OK · \$0\.0192/)).toBeTruthy()
    expect(screen.getByText(/1–2 of\s+38/)).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Next ▶" }))
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2/runs?offset=10&limit=10")
  })

  it("shows the fail streak with the limit and survives null lists", async () => {
    withDetail({ skills: null, toolsets: null, fail_limit: 5 })
    await mk()
    expect(screen.getByText(/3 \(pauses at 5\)/)).toBeTruthy()
  })

  it("shows the output of a run", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    await mk()
    await user.click(screen.getAllByRole("button", { name: "Output" })[0])
    const dlg = screen.getByRole("dialog", { name: outputName })
    expect(dlg.querySelector("pre")!.textContent).toBe("HTTP 401")
  })

  it("deletes the job after the confirm of the command", async () => {
    const user = userEvent.setup()
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
    const { router } = await mk()
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (p === "/api/jobs/j2" && vi.mocked(runCommand).mock.calls.length >= 2) throw notFound()
      return (p === "/api/jobs/j2" ? detail : [run(1), run(2)]) as any
    })
    await user.click(screen.getByRole("button", { name: "🗑 Delete…" }))
    await flushPromises()
    expect(runCommand).toHaveBeenNthCalledWith(1, "/jobs delete j2")
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({ title: "Delete gh-notify?", confirmLabel: "🗑 Delete" }),
    )
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
    await mk()
    expect(alertWith("⏸ Paused").textContent).toContain(
      "⏸ Paused: the schedule 0 9 * * 8 is not valid. Edit the schedule to resume.",
    )
    expect((screen.getByRole("button", { name: "⚡ Run now" }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect(screen.getByText("The schedule is not valid")).toBeTruthy()
    expect(screen.getByText("Resume the job first.")).toBeTruthy()
  })

  it("shows the script of a no-agent job and the deliver label", async () => {
    withDetail({ no_agent: true, script: "disk.sh", deliver_label: "Ops chat" })
    await mk()
    const def = screen.getByText("Definition").closest('[data-slot="card"]')!
    const text = def.textContent!
    expect(text).toContain("Script")
    expect(text).toContain("disk.sh")
    expect(text).not.toContain("Prompt")
    expect(text).toContain("Ops chat")
    expect(text).not.toContain("telegram:123")
  })

  it("shows the last output, the watch limit, and the failure pause", async () => {
    withDetail({
      last_output: "3 new issues",
      watch_limit_hit: true,
      job: { ...detail.job, enabled: false, pause_reason: "failures" },
    })
    await mk()
    expect(screen.getByText("3 new issues")).toBeTruthy()
    expect(screen.getByText("The watch limit is reached.")).toBeTruthy()
    expect(screen.getByText("Paused after 3 failures")).toBeTruthy()
  })

  it("has the Finished and Delivery error columns", async () => {
    await mk()
    const heads = screen.getAllByRole("columnheader").map((t) => t.textContent!.trim())
    expect(heads).toEqual(expect.arrayContaining(["Finished", "Delivery error"]))
  })

  it("opens the output as an expanded card on a phone", async () => {
    const user = userEvent.setup()
    phoneMedia()
    await mk()
    await user.click(screen.getAllByRole("button", { name: "Output" })[0])
    const card = screen.getByRole("region", { name: outputName })
    expect(card.textContent).toContain(outputName)
    await user.click(within(card).getByRole("button", { name: "Close" }))
    expect(screen.queryByRole("region")).toBeNull()
  })

  it("shows the empty runs with Run now, and the history-off text", async () => {
    const user = userEvent.setup()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/jobs/j2"
          ? {
              ...detail,
              runs_total: 0,
              last7: [],
              job: { ...detail.job, next_run: "2026-11-24T07:00:00Z" },
            }
          : []) as any,
    )
    vi.mocked(runCommand).mockResolvedValue({
      request_id: "r",
      text: "✅ Started.",
      buttons: [],
      edit: false,
    })
    const first = await mk()
    expect(
      screen.getByText(
        "No runs yet. The first run is at " + shortTime("2026-11-24T07:00:00Z") + ".",
      ),
    ).toBeTruthy()
    const runNow = screen.getAllByRole("button", { name: "⚡ Run now" })
    await user.click(runNow[runNow.length - 1])
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/jobs run j2")
    first.unmount()
    cleanup()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/jobs/j2"
          ? { ...detail, job: { ...detail.job, history_off: true } }
          : []) as any,
    )
    await mk()
    expect(screen.getByText("Run history is off (cron.run_history is 0).")).toBeTruthy()
  })

  it("loads again only on the job.run event of this job", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "job.run", data: { job_id: "j9", status: "ok" } }])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
    emitEvents([{ name: "job.run", data: { job_id: "j2", status: "ok" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs/j2")
  })

  it("keeps the open form mounted on a live reload and passes its dirty state", async () => {
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    await mk()
    await user.click(screen.getByRole("button", { name: "✎ Edit" }))
    await user.click(screen.getByRole("button", { name: "Make the form dirty" }))
    const dlg = screen.getByRole("dialog", { name: "Edit gh-notify" })
    await user.click(within(dlg).getByRole("button", { name: "Close" }))
    expect(screen.getByText("Discard the changes?")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    emitEvents([{ name: "job.run", data: { job_id: "j2", status: "ok" } }])
    await flushPromises()
    expect(mounts).toBe(1)
  })

  it("shows the 7-day cost of the job, the state glyph, and a stopped run", async () => {
    withDetail({
      job: { ...detail.job, icon: "" },
      last7: [{ ...run(1), status: "stopped" }, ...detail.last7.slice(1)],
    })
    await mk()
    expect(screen.getByText(/4 of 7 OK · \$0\.0192/)).toBeTruthy()
    expect(screen.getByRole("heading", { level: 1 }).textContent).toContain("⚠")
    const bars = document.querySelectorAll("[data-bar]")
    expect(bars[bars.length - 1].getAttribute("data-bar")).toBe("stop")
  })

  it("shows a dash for a cost that is not known, and done as muted text", async () => {
    withDetail({ job: { ...detail.job, state: "done", enabled: false, cost_7d_known: false } })
    await mk()
    expect(screen.getByText(/4 of 7 OK · —/)).toBeTruthy()
    const h1 = screen.getByRole("heading", { level: 1 })
    expect(h1.parentElement!.querySelector('[data-slot="badge"]')).toBeNull()
    expect(within(h1.parentElement!).getByText("done").classList).toContain("text-muted-foreground")
    expect(screen.queryByRole("button", { name: "⏸ Pause" })).toBeNull()
  })
})

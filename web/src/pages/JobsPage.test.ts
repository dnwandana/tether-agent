import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { defineComponent, h, onMounted } from "vue"
import { render, screen, within } from "@testing-library/vue"
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
import { setHost } from "../stores/host"
import { emitEvents } from "../lib/bus"
import { fullTime, shortTime } from "../lib/time"
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
  enabled: true,
  watch: false,
  draft_only: false,
  pause_reason: "",
  history_off: false,
  last7: Array(7).fill("ok"),
  state: "ok",
  cost_7d_usd: 0.0102,
  cost_7d_known: true,
  cost_7d_unpriced: false,
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
let data: Record<string, unknown>
const fresh = (): Record<string, unknown> => ({
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
      state: "paused",
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
})

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
  const r = render(JobsPage, { global: { plugins: [router], stubs: { JobForm } } })
  await flushPromises()
  return r
}
const rowOf = (text: string) =>
  screen.getAllByRole("row").find((r) => r.textContent!.includes(text))!
const bars = (el: Element) =>
  Array.from(el.querySelectorAll<HTMLElement>("[data-bar]")).map((b) => b.dataset.bar)

beforeEach(() => {
  data = fresh()
  mounts = 0
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
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

afterEach(() => vi.restoreAllMocks())

describe("JobsPage", () => {
  it("shows the schedule in the browser zone with the raw text in the title", async () => {
    setHost("UTC", 0)
    vi.spyOn(Date.prototype, "getTimezoneOffset").mockReturnValue(-420)
    data["/api/jobs"] = [job({ id: "j1", name: "nightly", schedule: "0 1 * * *" })]
    await mk()
    expect(screen.getByTitle("0 1 * * * · host time UTC").textContent).toBe("daily 08:00")
  })

  it("loads the brief, the jobs, and the held sends", async () => {
    const { container } = await mk()
    for (const p of ["/api/brief", "/api/jobs", "/api/held"])
      expect(api).toHaveBeenCalledWith("GET", p)
    expect(screen.getByText("Jobs: 41 of 44 runs OK in 24 h")).toBeTruthy()
    expect(container.textContent).toContain("$0.7310")
    expect(container.textContent).toContain("2/4 ok")
    expect(bars(container.querySelectorAll("[data-runs7]")[1])).toEqual([
      "none",
      "none",
      "none",
      "ok",
      "bad",
      "ok",
      "stop",
    ])
  })

  it("pauses a job with the command and disables Run now on a paused job", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getAllByRole("button", { name: "⏸ Pause" })[0])
    expect(runCommand).toHaveBeenCalledWith("/jobs pause j1")
    const run = screen.getAllByRole("button", { name: "⚡ Run now" }) as HTMLButtonElement[]
    expect(run[0].disabled).toBe(false)
    expect(run[1].disabled).toBe(true)
  })

  it("shows the buttons of each held kind and drops a hold", async () => {
    const user = userEvent.setup()
    await mk()
    expect(screen.getAllByRole("button", { name: '✅ Send to "Ops chat"' })).toHaveLength(1)
    expect(screen.getAllByRole("button", { name: "✅ Post the full text there" })).toHaveLength(1)
    expect(screen.getAllByRole("button", { name: "❌ Drop" })).toHaveLength(1)
    await user.click(screen.getByRole("button", { name: "❌ Drop" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/held/h1", { action: "drop" })
  })

  it("links the brief lines to the job and the goal", async () => {
    await mk()
    expect(screen.getByRole("link", { name: /gh-notify/ }).getAttribute("href")).toBe("/jobs/j2")
    expect(screen.getByRole("link", { name: /ship it/ }).getAttribute("href")).toBe("/goals/s9")
  })

  it("opens the job form in a side panel", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "+ New job" }))
    const dlg = screen
      .getByRole("button", { name: "Make the form dirty" })
      .closest('[role="dialog"]')
    expect(dlg?.getAttribute("aria-label")).toBe("New job")
  })

  it("asks before it drops a held send", async () => {
    const user = userEvent.setup()
    vi.mocked(confirm).mockResolvedValueOnce(false)
    await mk()
    await user.click(screen.getByRole("button", { name: "❌ Drop" }))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Drop this held send?",
      body: "The text is deleted.",
      confirmLabel: "❌ Drop",
      danger: true,
    })
    expect(api).not.toHaveBeenCalledWith("POST", "/api/held/h1", { action: "drop" })
  })

  it("disables Post with its reason and shows the kind and the sources", async () => {
    data["/api/held"] = [
      held({
        id: "h3",
        kind: "hold",
        job_name: "price-watch",
        target_title: "Ops chat",
        sources: ["web_fetch", "browser_read"],
        can_post: false,
        actions: [
          { text: "❌ Drop", data: "drop" },
          { text: '✅ Send to "Ops chat"', data: "post" },
        ],
      }),
    ]
    const { container } = await mk()
    const post = screen.getByRole("button", { name: '✅ Send to "Ops chat"' }) as HTMLButtonElement
    expect(post.disabled).toBe(true)
    expect(container.textContent).toContain("Telegram is off.")
    expect(container.textContent).toContain("web_fetch, browser_read")
    expect(container.textContent).toContain("hold")
  })

  it("shows the counts, the costs, and the bad schedule icon in the Brief", async () => {
    data["/api/brief"] = {
      ...(data["/api/brief"] as object),
      lines: [
        {
          kind: "bad_schedule",
          name: "report",
          detail: "bad schedule",
          count: 3,
          age_seconds: 60,
          cost_usd: 0.12,
          ratio: 0,
          job_id: "j4",
          session_id: "",
        },
      ],
      spend_unpriced: true,
    }
    const { container } = await mk()
    const line = screen.getByRole("link", { name: /bad schedule/ })
    expect(line.textContent).toContain("⚠️")
    expect(line.textContent).toContain("×3")
    expect(line.textContent).toContain("$0.1200")
    expect(container.textContent).toContain("unpriced")
  })

  it("says that nothing needs attention, and that run history is off", async () => {
    data["/api/brief"] = { ...(data["/api/brief"] as object), lines: [], history_off: true }
    const { container } = await mk()
    expect(screen.getByText("Nothing needs your attention.")).toBeTruthy()
    expect(screen.getByText("Run history is off.")).toBeTruthy()
    expect(container.textContent).not.toContain("runs OK in 24 h")
  })

  it("loads again on the job.run and held events", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "job.run", data: { job_id: "j1", status: "ok" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/jobs")
    vi.mocked(api).mockClear()
    emitEvents([{ name: "held", data: { id: "h1" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/held")
  })

  it("shows the empty text with + New job", async () => {
    const user = userEvent.setup()
    data["/api/jobs"] = []
    await mk()
    expect(screen.getByText("No jobs yet.")).toBeTruthy()
    await user.click(screen.getAllByRole("button", { name: "+ New job" })[1])
    await flushPromises()
    expect(screen.getByRole("button", { name: "Make the form dirty" })).toBeTruthy()
    expect(mounts).toBe(1)
  })

  it("passes the dirty state of the new-job form to the panel", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "+ New job" }))
    await user.click(screen.getByRole("button", { name: "Make the form dirty" }))
    const dlg = screen.getByRole("dialog", { name: "New job" })
    await user.click(within(dlg).getByRole("button", { name: "Close" }))
    expect(await screen.findByText("Discard the changes?")).toBeTruthy()
  })

  it("shows No runs, the state glyph, done as muted text, and the 7-day cost", async () => {
    data["/api/jobs"] = [
      job({ id: "j5", name: "fresh", icon: "", state: "pending", last7: [] }),
      job({ id: "j6", name: "gone", icon: "", state: "done", enabled: false }),
      job({ id: "j7", name: "cheap", icon: "", state: "failing", cost_7d_unpriced: true }),
      job({ id: "j8", name: "script", icon: "", cost_7d_known: false, cost_7d_usd: 0 }),
    ]
    await mk()
    expect(screen.getAllByRole("columnheader").map((t) => t.textContent)).toContain("Cost · 7 d")
    expect(rowOf("fresh").textContent).toContain("No runs")
    expect(rowOf("fresh").textContent).toContain("⏳")
    expect(rowOf("fresh").querySelector("[data-runs7]")).toBeNull()
    expect(rowOf("gone").querySelector('[data-slot="badge"]')).toBeNull()
    expect(within(rowOf("gone")).getByText("done").className).toContain("text-muted-foreground")
    expect(within(rowOf("gone")).queryAllByRole("button")).toHaveLength(0)
    expect(rowOf("cheap").textContent).toContain("⚠")
    expect(rowOf("cheap").textContent).toContain("$0.0102+")
    expect(rowOf("script").textContent).toContain("—")
  })

  it("shows the next run in the short format with the full time in the title", async () => {
    await mk()
    expect(screen.getAllByTitle(fullTime("2026-11-21T06:00:00Z"))[0].textContent).toBe(
      shortTime("2026-11-21T06:00:00Z"),
    )
  })

  it("puts a space between the job icon, the name, and the watch chip", async () => {
    data["/api/jobs"] = [job({ id: "j9", name: "daily-digest", watch: true })]
    await mk()
    const cell = within(rowOf("daily-digest")).getAllByRole("cell")[0]
    expect(cell.textContent!.replace(/\s+/g, " ").trim()).toBe("✅ daily-digest watch")
  })
})

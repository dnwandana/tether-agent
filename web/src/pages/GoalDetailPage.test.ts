import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"
import { defineComponent, h } from "vue"

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
import { ApiError } from "../api/client"
import { emitEvents } from "../lib/bus"
import { api, runCommand } from "../api/client"
import { confirm } from "../stores/ui"
import { session } from "../stores/session"
import GoalDetailPage from "./GoalDetailPage.vue"

const Blank = { render: () => null }
const detail = {
  row: {
    session_id: "s1",
    title: "Release notes",
    source: "dashboard",
    goal: "Ship v0.24",
    status: "active",
    waiting: false,
    wait_target: "",
    paused_reason: "",
    turns_used: 8,
    max_turns: 20,
    updated_at: "2026-11-20T09:00:00Z",
    last_verdict: "continue",
    last_turn_at: "2026-11-20T09:30:00Z",
    can_stop: true,
    can_resume: true,
  },
  status_line: "",
  last_verdict: "continue",
  last_reason: "2 tests fail",
  outcome: "v0.24.0 is tagged.",
  verification: "go test ./... passes.",
  constraints: "No force push.",
  boundaries: "Only ledger.",
  stop_when: "20 turns.",
  subgoals: ["Write CHANGELOG.md", "Tag v0.24.0"],
  subgoal_hashes: ["aaaaaaaa", "bbbbbbbb"],
  gate_hashes: ["cccccccc"],
  gates: [
    {
      command: "go test ./...",
      timeout_seconds: 600,
      max_retries: 2,
      attempts: 1,
      last_exit_code: 1,
      last_output_tail: "ok internal/cron\nok internal/goals\nFAIL internal/gateway",
    },
  ],
  waiting_reason: "",
  waiting_since: "0001-01-01T00:00:00Z",
  waiting_until: "0001-01-01T00:00:00Z",
  waiting_on_pid: 0,
  waiting_on_session: "",
  waiting_on_delegations: 0,
  parse_failures: 0,
  transport_failures: 0,
  turn_failures: 1,
}

const other = { ...detail, row: { ...detail.row, can_resume: false } }

let router: ReturnType<typeof createRouter>
async function mk() {
  router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/goals", name: "goals", component: Blank },
      { path: "/goals/:session", name: "goal", component: GoalDetailPage },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/goals/s1")
  // The panel stub always shows its content, so the test can fill the Wait fields.
  const SidePanel = defineComponent({
    name: "SidePanel",
    props: ["open", "title", "dirty"],
    setup:
      (_, { slots }) =>
      () =>
        h("div", slots.default?.()),
  })
  const w = mount(GoalDetailPage, { global: { plugins: [router], stubs: { SidePanel } } })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(detail)
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "Done.", buttons: [], edit: false })
  vi.mocked(confirm).mockReset()
  session.id = "s1"
})

describe("GoalDetailPage", () => {
  it("shows the contract, the subgoals, and the gates", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals/s1")
    expect(w.text()).toContain("8 / 20")
    expect(w.text()).toContain("v0.24.0 is tagged.")
    expect(w.text()).toContain("Tag v0.24.0")
    expect(w.text()).toContain("1 / 2")
    expect(w.text()).toContain("FAIL internal/gateway")
    expect(w.text()).not.toContain("ok internal/cron")
  })

  it("removes a subgoal and adds a gate with the commands", async () => {
    const w = await mk()
    await w
      .findAll("button")
      .filter((b) => b.text() === "Remove")[1]
      .trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/subgoal remove 2 bbbbbbbb")
    await w.find('input[placeholder="Add a gate, for example: go vet ./..."]').setValue("make lint")
    await btn(w, "Add gate").trigger("click")
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate add make lint")
  })

  it("sends the gate confirm of the server, or its cancel", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⚠ This gate runs a dangerous command.",
      edit: false,
      buttons: [
        [
          { text: "Add gate", data: "/goal gate ok 3" },
          { text: "Cancel", data: "/goal gate no 3" },
        ],
      ],
    })
    vi.mocked(confirm).mockResolvedValue(false)
    const w = await mk()
    await w
      .find('input[placeholder="Add a gate, for example: go vet ./..."]')
      .setValue("rm -rf build")
    await btn(w, "Add gate").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "⚠ This gate runs a dangerous command.",
        confirmLabel: "Add gate",
      }),
    )
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate no 3")
  })

  it("parks the goal on a process", async () => {
    const w = await mk()
    await btn(w, "Wait…").trigger("click")
    await w.find('[data-field="pid"]').setValue("48390")
    await w.find('[data-field="reason"]').setValue("the CI run must finish")
    await btn(w, "Wait").trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/goal wait 48390 the CI run must finish")
  })

  it("asks before a gate remove and sends the hash", async () => {
    vi.mocked(confirm).mockResolvedValue(true)
    const w = await mk()
    const gateRemove = w.findAll("tr").find((r) => r.text().includes("go test ./..."))!
    await gateRemove
      .findAll("button")
      .find((b) => b.text() === "Remove")!
      .trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "Remove this gate?",
        items: ["go test ./..."],
        danger: true,
      }),
    )
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate remove 1 cccccccc")
  })

  it("clears all subgoals and all gates after a confirm", async () => {
    vi.mocked(confirm).mockResolvedValue(true)
    const w = await mk()
    const all = w.findAll("button").filter((b) => b.text() === "Clear all")
    await all[0].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/subgoal clear")
    await all[1].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate clear")
  })

  it("acts on another session with --session and shows where to resume", async () => {
    vi.mocked(api).mockResolvedValue(other)
    vi.mocked(confirm).mockResolvedValue(true)
    session.id = "s9"
    const w = await mk()
    expect(w.text()).toContain("To resume, set, or wait, send /goal in that chat.")
    expect(w.findAll("button").some((b) => b.text() === "Wait…")).toBe(false)
    expect(w.findAll("button").some((b) => b.text() === "Add gate")).toBe(false)
    await btn(w, "⏸ Pause").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause --session s1")
    await w
      .findAll("button")
      .filter((b) => b.text() === "Remove")[1]
      .trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/subgoal remove 2 bbbbbbbb --session s1")
    await btn(w, "Clear").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal clear --session s1")
  })

  it("shows a CLI goal as view only", async () => {
    vi.mocked(api).mockResolvedValue({
      ...detail,
      row: { ...detail.row, source: "cli", can_stop: false, can_resume: false },
    })
    const w = await mk()
    expect(w.text()).toContain("This goal runs in tether chat. Change it there.")
    expect(w.findAll("button").some((b) => ["⏸ Pause", "Clear", "Remove"].includes(b.text()))).toBe(
      false,
    )
  })

  it("resumes with a new budget, unwaits a parked goal, and drafts again", async () => {
    vi.mocked(api).mockResolvedValue({
      ...detail,
      row: { ...detail.row, waiting: true, wait_target: "pid 7" },
      waiting_reason: "the CI run must finish",
    })
    const w = await mk()
    expect(w.text()).toContain("the CI run must finish")
    await btn(w, "Unwait").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal unwait")
    await btn(w, "Draft again").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal draft Ship v0.24")
    vi.mocked(api).mockResolvedValue({ ...detail, row: { ...detail.row, status: "paused" } })
    const p = await mk()
    await btn(p, "▶ Resume (new budget)").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal resume")
  })

  it("sets a new goal text", async () => {
    const w = await mk()
    await w.find('input[aria-label="New goal"]').setValue("Ship v0.25")
    await btn(w, "Set").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal -- Ship v0.25")
  })

  it("opens the full gate output in the side panel", async () => {
    const w = await mk()
    await btn(w, "Show all").trigger("click")
    expect(w.find(".gate-full").text()).toContain("ok internal/cron")
  })

  it("shows the 404 state with Back to Goals", async () => {
    vi.mocked(api).mockRejectedValue(
      Object.assign(Object.create(ApiError.prototype), {
        status: 404,
        serverText: "No goal for this session.",
        message: "No goal for this session.",
      }),
    )
    const w = await mk()
    expect(w.text()).toContain("No goal for this session.")
    w.findComponent({ name: "StateBlock" }).vm.$emit("action")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("goals")
  })

  it("loads again only on the goal.state event of this session", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "goal.state", data: { session_id: "s9", status: "active" } }])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
    emitEvents([{ name: "goal.state", data: { session_id: "s1", status: "paused" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals/s1")
  })

  it("marks the Wait panel dirty while it has text", async () => {
    const w = await mk()
    await btn(w, "Wait…").trigger("click")
    await w.find('[data-field="pid"]').setValue("48390")
    const panel = w
      .findAllComponents({ name: "SidePanel" })
      .find((p) => p.props("title") === "Wait on a background process")!
    expect(panel.props("dirty")).toBe(true)
  })

  it("loads no goal when the user leaves the page", async () => {
    await mk()
    vi.mocked(api).mockClear()
    await router.push("/goals")
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })
})

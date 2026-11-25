import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within, cleanup } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
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
import { fullTime, shortTime } from "../lib/time"
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
      (props, { slots }) =>
      () =>
        h(
          "div",
          { role: "dialog", "aria-label": props.title, "data-dirty": String(!!props.dirty) },
          slots.default?.(),
        ),
  })
  const r = render(GoalDetailPage, { global: { plugins: [router], stubs: { SidePanel } } })
  await flushPromises()
  return r
}
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const btn = (t: string) => screen.getByRole("button", { name: t })
const hasBtn = (t: string) => screen.queryByRole("button", { name: t }) !== null
const GATE_PH = "Add a gate, for example: go vet ./..."

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
    const { container } = await mk()
    const text = container.textContent!
    expect(api).toHaveBeenCalledWith("GET", "/api/goals/s1")
    expect(text).toContain("8 / 20")
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("40")
    expect(text).toContain("v0.24.0 is tagged.")
    expect(text).toContain("Tag v0.24.0")
    expect(text).toContain("1 / 2")
    expect(text).toContain("FAIL internal/gateway")
    expect(text).not.toContain("ok internal/cron")
  })

  it("shows the times in the short format inside a page stack", async () => {
    await mk()
    for (const t of ["2026-11-20T09:30:00Z", "2026-11-20T09:00:00Z"])
      expect(screen.getByTitle(fullTime(t)).textContent).toBe(shortTime(t))
  })

  it("removes a subgoal and adds a gate with the commands", async () => {
    const user = setup()
    await mk()
    await user.click(screen.getAllByRole("button", { name: "Remove" })[1])
    expect(runCommand).toHaveBeenCalledWith("/subgoal remove 2 bbbbbbbb")
    await user.type(screen.getByPlaceholderText(GATE_PH), "make lint")
    await user.click(btn("Add gate"))
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate add make lint")
  })

  it("sends the gate confirm of the server, or its cancel", async () => {
    const user = setup()
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
    await mk()
    await user.type(screen.getByPlaceholderText(GATE_PH), "rm -rf build")
    await user.click(btn("Add gate"))
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
    const user = setup()
    await mk()
    await user.click(btn("Wait…"))
    await user.type(screen.getByLabelText("PID"), "48390")
    await user.type(screen.getByLabelText("Reason"), "the CI run must finish")
    await user.click(screen.getByRole("button", { name: "Wait" }))
    expect(runCommand).toHaveBeenCalledWith("/goal wait 48390 the CI run must finish")
  })

  it("asks before a gate remove and sends the hash", async () => {
    const user = setup()
    vi.mocked(confirm).mockResolvedValue(true)
    await mk()
    const gateRow = screen
      .getAllByRole("row")
      .find((r) => r.textContent!.includes("go test ./..."))!
    await user.click(within(gateRow).getByRole("button", { name: "Remove" }))
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
    const user = setup()
    vi.mocked(confirm).mockResolvedValue(true)
    await mk()
    const all = screen.getAllByRole("button", { name: "Clear all" })
    await user.click(all[0])
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/subgoal clear")
    await user.click(all[1])
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate clear")
  })

  it("acts on another session with --session and shows where to resume", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue(other)
    vi.mocked(confirm).mockResolvedValue(true)
    session.id = "s9"
    await mk()
    expect(screen.getByRole("alert").textContent).toContain(
      "To resume, set, or wait, send /goal in that chat.",
    )
    expect(hasBtn("Wait…")).toBe(false)
    expect(hasBtn("Add gate")).toBe(false)
    await user.click(btn("⏸ Pause"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause --session s1")
    await user.click(screen.getAllByRole("button", { name: "Remove" })[1])
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/subgoal remove 2 bbbbbbbb --session s1")
    await user.click(btn("Clear"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal clear --session s1")
  })

  it("shows a CLI goal as view only", async () => {
    vi.mocked(api).mockResolvedValue({
      ...detail,
      row: { ...detail.row, source: "cli", can_stop: false, can_resume: false },
    })
    const { container } = await mk()
    expect(container.textContent).toContain("This goal runs in tether chat. Change it there.")
    for (const t of ["⏸ Pause", "Clear", "Remove"]) expect(hasBtn(t)).toBe(false)
  })

  it("resumes with a new budget, unwaits a parked goal, and drafts again", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue({
      ...detail,
      row: { ...detail.row, waiting: true, wait_target: "pid 7" },
      waiting_reason: "the CI run must finish",
    })
    const { container } = await mk()
    expect(container.textContent).toContain("the CI run must finish")
    await user.click(btn("Unwait"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal unwait")
    await user.click(btn("Draft again"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal draft Ship v0.24")
    cleanup()
    vi.mocked(api).mockResolvedValue({ ...detail, row: { ...detail.row, status: "paused" } })
    await mk()
    await user.click(btn("▶ Resume (new budget)"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal resume")
  })

  it("sets a new goal text", async () => {
    const user = setup()
    await mk()
    await user.type(screen.getByRole("textbox", { name: "New goal" }), "Ship v0.25")
    await user.click(btn("Set"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal -- Ship v0.25")
  })

  it("opens the full gate output in the side panel", async () => {
    const user = setup()
    await mk()
    await user.click(btn("Show all"))
    expect(
      within(screen.getByRole("dialog", { name: "Gate output" })).getByText(/ok internal\/cron/),
    ).toBeTruthy()
  })

  it("shows the 404 state with Back to Goals", async () => {
    const user = setup()
    vi.mocked(api).mockRejectedValue(
      Object.assign(Object.create(ApiError.prototype), {
        status: 404,
        serverText: "No goal for this session.",
        message: "No goal for this session.",
      }),
    )
    await mk()
    expect(screen.getByText("No goal for this session.")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Back to Goals" }))
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
    const user = setup()
    await mk()
    await user.click(btn("Wait…"))
    await user.type(screen.getByLabelText("PID"), "48390")
    expect(
      screen
        .getByRole("dialog", { name: "Wait on a background process" })
        .getAttribute("data-dirty"),
    ).toBe("true")
  })

  it("loads no goal when the user leaves the page", async () => {
    await mk()
    vi.mocked(api).mockClear()
    await router.push("/goals")
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })
  it("lets the output tail wrap, keeps Remove at the right edge, and names the gate pager", async () => {
    const gates = Array.from({ length: 12 }, (_, i) => ({
      ...detail.gates[0],
      command: "make check" + i,
    }))
    vi.mocked(api).mockResolvedValue({ ...detail, gates, gate_hashes: gates.map(() => "cccccccc") })
    await mk()
    expect(screen.getByRole("navigation", { name: "Gates pages" })).toBeTruthy()
    expect(screen.getByText("1–12 of 12")).toBeTruthy()
    const command = screen.getByText("make check0")
    expect(command.getAttribute("title")).toBe("make check0")
    const row = command.closest("tr")!
    const tail = within(row).getByText(/FAIL internal\/gateway/)
    expect(tail.closest("td")!.classList.contains("whitespace-normal")).toBe(true)
    const remove = within(row).getByRole("button", { name: "Remove" })
    expect(remove.closest("td")!.classList.contains("text-right")).toBe(true)
  })
})

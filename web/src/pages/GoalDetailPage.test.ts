import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"
import { defineComponent, h } from "vue"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
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
    actionable: true,
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
  gates: [
    {
      command: "go test ./...",
      timeout_seconds: 600,
      max_retries: 2,
      attempts: 1,
      last_exit_code: 1,
      last_output_tail: "FAIL internal/gateway",
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

let router: ReturnType<typeof createRouter>
async function mk() {
  router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/goals", name: "goals", component: Blank },
      { path: "/goals/:session", name: "goal", component: GoalDetailPage },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/goals/s1")
  // The panel stub always shows its content, so the test can fill the Wait fields.
  const SidePanel = defineComponent(
    (_, { slots }) =>
      () =>
        h("div", slots.default?.()),
  )
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
  })

  it("removes a subgoal and adds a gate with the commands", async () => {
    const w = await mk()
    await w
      .findAll("button")
      .filter((b) => b.text() === "Remove")[1]
      .trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/subgoal remove 2")
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
    expect(confirm).toHaveBeenCalledWith({
      title: "⚠ This gate runs a dangerous command.",
      body: "",
      confirmLabel: "Add gate",
      danger: true,
    })
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

  it("is read-only for a goal of another session", async () => {
    session.id = "s9"
    const w = await mk()
    expect(w.find(".ro-banner").text()).toContain("👁 This goal belongs to Dashboard.")
    expect(w.findAll("button").some((b) => b.text() === "Add gate")).toBe(false)
  })

  it("loads no goal when the user leaves the page", async () => {
    await mk()
    vi.mocked(api).mockClear()
    await router.push("/goals")
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })
})

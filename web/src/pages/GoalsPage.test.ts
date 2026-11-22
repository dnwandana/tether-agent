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
import { session } from "../stores/session"
import GoalsPage from "./GoalsPage.vue"

const Blank = { render: () => null }
const goal = (o: object) => ({
  session_id: "",
  title: "",
  source: "dashboard",
  goal: "",
  status: "active",
  waiting: false,
  wait_target: "",
  paused_reason: "",
  turns_used: 8,
  max_turns: 20,
  updated_at: "2026-11-20T09:00:00Z",
  actionable: false,
  ...o,
})
const list = [
  goal({ session_id: "s1", title: "Release notes for v0.24", goal: "Ship v0.24 with green tests" }),
  goal({
    session_id: "s2",
    title: "Photos to the NAS",
    goal: "Migrate photos",
    source: "telegram",
    waiting: true,
    wait_target: "pid 48211",
  }),
]

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/goals", name: "goals", component: GoalsPage },
      { path: "/goals/:session", name: "goal", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/goals")
  const w = mount(GoalsPage, { global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)

beforeEach(() => {
  vi.mocked(api).mockReset().mockResolvedValue(list)
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "⏸ Goal paused.", buttons: [], edit: false })
  vi.mocked(confirm).mockReset()
  session.id = "s1"
})

describe("GoalsPage", () => {
  it("lists the goals with the session title", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals")
    expect(w.text()).toContain("Dashboard · “Release notes for v0.24” · 8/20 turns")
    expect(w.text()).toContain("Telegram · “Photos to the NAS” · 8/20 turns · waits on pid 48211")
    expect(w.text()).toContain("parked")
  })

  it("gives actions only to the goal of the active dashboard session", async () => {
    const { w } = await mk()
    expect(w.findAll("button").filter((b) => b.text() === "⏸ Pause")).toHaveLength(1)
    expect(w.text()).toContain("read-only")
    expect(w.text()).toContain("Open session")
    await btn(w, "⏸ Pause")!.trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/goal pause")
  })

  it("asks before Clear", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const { w } = await mk()
    await btn(w, "Clear")!.trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Clear the goal?",
      body: "The goal stops, and its budget, gates, and subgoals are cleared.",
      confirmLabel: "Clear the goal",
      danger: true,
    })
    expect(runCommand).not.toHaveBeenCalled()
  })

  it("shows the finished goals with the label", async () => {
    const { w } = await mk()
    await btn(w, "Finished · 30 days")!.trigger("click")
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/goals?finished=1")
    expect(w.text()).toContain("Last goal of each session")
  })

  it("shows the empty text", async () => {
    vi.mocked(api).mockResolvedValue([])
    const { w } = await mk()
    expect(w.text()).toContain("No goals. Send /goal <text> in Chat to start one.")
  })
})

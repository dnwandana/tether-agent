import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
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
import { emitEvents } from "../lib/bus"
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
  last_verdict: "",
  last_turn_at: null,
  can_stop: false,
  can_resume: false,
  ...o,
})
const list = [
  goal({
    session_id: "s1",
    title: "Release notes for v0.24",
    goal: "Ship v0.24 with green tests",
    last_verdict: "continue",
    last_turn_at: "2026-11-20T09:30:00Z",
    can_stop: true,
    can_resume: true,
  }),
  goal({
    session_id: "s2",
    title: "Photos to the NAS",
    goal: "Migrate photos",
    source: "telegram",
    waiting: true,
    wait_target: "pid 48211",
    can_stop: true,
  }),
  goal({
    session_id: "s3",
    title: "Disk check",
    goal: "Free 20 GB",
    source: "cli",
    status: "paused",
  }),
]

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/goals", name: "goals", component: GoalsPage },
      { path: "/goals/:session", name: "goal", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/goals")
  const w = mount(GoalsPage, { global: { plugins: [router], stubs: { teleport: true } } })
  await flushPromises()
  return { w, router }
}
const rowOf = (w: any, goalText: string) =>
  w.findAll("tr").find((r: any) => r.text().includes(goalText))!
const rowBtn = (w: any, goalText: string, label: string) =>
  rowOf(w, goalText)
    .findAll("button")
    .find((b: any) => b.text() === label)

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

  it("asks before Clear", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const { w } = await mk()
    await w
      .findAll("tr")
      .find((r) => r.text().includes("Ship v0.24"))!
      .findAll("button")
      .find((b) => b.text() === "Clear")!
      .trigger("click")
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

  it("shows the Last verdict and Last turn columns", async () => {
    const { w } = await mk()
    const heads = w.findAll("th").map((t) => t.text())
    expect(heads).toEqual(expect.arrayContaining(["Last verdict", "Last turn"]))
    expect(rowOf(w, "Ship v0.24").text()).toContain("continue")
  })

  it("gives Pause and Clear to dashboard and Telegram rows", async () => {
    const { w } = await mk()
    await rowBtn(w, "Ship v0.24", "⏸ Pause")!.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause")
    await rowBtn(w, "Migrate photos", "⏸ Pause")!.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause --session s2")
    vi.mocked(confirm).mockResolvedValue(true)
    await rowBtn(w, "Migrate photos", "Clear")!.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal clear --session s2")
  })

  it("keeps Resume and Unwait on the active dashboard session only", async () => {
    vi.mocked(api).mockResolvedValue([
      { ...list[0], waiting: true, wait_target: "pid 7" },
      { ...list[1], status: "paused", waiting: false },
    ])
    const { w } = await mk()
    await rowBtn(w, "Ship v0.24", "Unwait")!.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal unwait")
    expect(rowBtn(w, "Migrate photos", "▶ Resume")).toBeUndefined()
    expect(rowBtn(w, "Migrate photos", "Clear")).toBeDefined()
  })

  it("shows a CLI goal as view only", async () => {
    const { w } = await mk()
    const cli = rowOf(w, "Free 20 GB")
    expect(cli.findAll("button")).toHaveLength(0)
    expect(cli.text()).toContain("view only")
    expect(cli.text()).toContain("Open session")
  })

  it("filters the open goals by state", async () => {
    const { w } = await mk()
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Parked")!
      .trigger("click")
    expect(w.text()).toContain("Migrate photos")
    expect(w.text()).not.toContain("Ship v0.24")
    expect(w.text()).not.toContain("Free 20 GB")
    await w
      .findAll(".seg button")
      .find((b) => b.text() === "Paused")!
      .trigger("click")
    expect(w.text()).toContain("Free 20 GB")
    expect(w.text()).not.toContain("Migrate photos")
  })

  it("loads again on the goal.state event", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "goal.state", data: { session_id: "s2", status: "paused" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals")
  })

  it("shows No open goals with Open chat", async () => {
    vi.mocked(api).mockResolvedValue([])
    const { w, router } = await mk()
    expect(w.text()).toContain("No open goals.")
    w.findAllComponents({ name: "StateBlock" })
      .find((s) => s.props("action") === "Open chat")!
      .vm.$emit("action")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
  })
})

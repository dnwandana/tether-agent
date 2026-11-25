import { describe, it, expect, vi, beforeEach } from "vitest"
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
import { emitEvents } from "../lib/bus"
import { api, runCommand } from "../api/client"
import { confirm } from "../stores/ui"
import { session } from "../stores/session"
import { fullTime, shortTime } from "../lib/time"
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

async function mk(path = "/goals") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/goals", name: "goals", component: GoalsPage },
      { path: "/goals/:session", name: "goal", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push(path)
  const r = render(GoalsPage, { global: { plugins: [router] } })
  await flushPromises()
  return { ...r, router }
}
const rowOf = (text: string) =>
  screen.getAllByRole("row").find((r) => r.textContent!.includes(text))!
const rowBtn = (text: string, label: string) =>
  within(rowOf(text)).queryByRole("button", { name: label })
const filterGroup = () =>
  screen.getByRole("button", { name: "All open" }).closest('[role="group"]') as HTMLElement
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })

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
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals")
    expect(screen.getByText("Dashboard · “Release notes for v0.24” · 8/20 turns")).toBeTruthy()
    expect(
      screen.getByText("Telegram · “Photos to the NAS” · 8/20 turns · waits on pid 48211"),
    ).toBeTruthy()
    expect(screen.getByText("parked")).toBeTruthy()
  })

  it("asks before Clear", async () => {
    const user = setup()
    vi.mocked(confirm).mockResolvedValue(false)
    await mk()
    await user.click(rowBtn("Ship v0.24", "Clear")!)
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Clear the goal?",
      body: "The goal stops, and its budget, gates, and subgoals are cleared.",
      confirmLabel: "Clear the goal",
      danger: true,
    })
    expect(runCommand).not.toHaveBeenCalled()
  })

  it("shows one control for the open filters and the finished goals", async () => {
    const user = setup()
    const { container } = await mk()
    expect(container.querySelectorAll('[data-slot="toggle-group"]')).toHaveLength(1)
    expect(
      within(filterGroup())
        .getAllByRole("button")
        .map((b) => b.textContent!.trim()),
    ).toEqual(["All open", "Active", "Parked", "Paused", "Finished (30 d)"])
    await user.click(screen.getByRole("button", { name: "Finished (30 d)" }))
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/goals?finished=1")
    expect(
      screen.getByRole("button", { name: "Finished (30 d)" }).getAttribute("aria-pressed"),
    ).toBe("true")
    expect(screen.getByText("Last goal of each session")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Parked" }))
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/goals")
    expect(screen.queryByText("Last goal of each session")).toBeNull()
    expect(screen.getByText("Migrate photos")).toBeTruthy()
    expect(screen.queryByText("Ship v0.24 with green tests")).toBeNull()
  })

  it("does not load again for a filter of the open goals", async () => {
    const user = setup()
    await mk()
    vi.mocked(api).mockClear()
    await user.click(screen.getByRole("button", { name: "Active" }))
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })

  it("shows the last turn in the short format inside a page stack", async () => {
    await mk()
    const cell = within(rowOf("Ship v0.24")).getByTitle(fullTime("2026-11-20T09:30:00Z"))
    expect(cell.textContent).toBe(shortTime("2026-11-20T09:30:00Z"))
    expect(rowOf("Migrate photos").textContent).toContain("—")
  })

  it("shows the Last verdict and Last turn columns", async () => {
    await mk()
    const heads = screen.getAllByRole("columnheader").map((t) => t.textContent!.trim())
    expect(heads).toEqual(expect.arrayContaining(["Last verdict", "Last turn"]))
    expect(rowOf("Ship v0.24").textContent).toContain("continue")
  })

  it("gives Pause and Clear to dashboard and Telegram rows", async () => {
    const user = setup()
    await mk()
    await user.click(rowBtn("Ship v0.24", "⏸ Pause")!)
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause")
    await user.click(rowBtn("Migrate photos", "⏸ Pause")!)
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal pause --session s2")
    vi.mocked(confirm).mockResolvedValue(true)
    await user.click(rowBtn("Migrate photos", "Clear")!)
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal clear --session s2")
  })

  it("keeps Resume and Unwait on the active dashboard session only", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue([
      { ...list[0], waiting: true, wait_target: "pid 7" },
      { ...list[1], status: "paused", waiting: false },
    ])
    await mk()
    await user.click(rowBtn("Ship v0.24", "Unwait")!)
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/goal unwait")
    expect(rowBtn("Migrate photos", "▶ Resume")).toBeNull()
    expect(rowBtn("Migrate photos", "Clear")).not.toBeNull()
  })

  it("shows a CLI goal as view only", async () => {
    await mk()
    const cli = rowOf("Free 20 GB")
    expect(within(cli).queryAllByRole("button")).toHaveLength(0)
    expect(cli.textContent).toContain("view only")
    expect(within(cli).getByRole("link", { name: "Open session" }).getAttribute("href")).toBe(
      "/sessions/s3",
    )
  })

  it("filters the open goals by state", async () => {
    const user = setup()
    await mk()
    await user.click(within(filterGroup()).getByRole("button", { name: "Parked" }))
    expect(screen.getByText("Migrate photos")).toBeTruthy()
    expect(screen.queryByText("Ship v0.24 with green tests")).toBeNull()
    expect(screen.queryByText("Free 20 GB")).toBeNull()
    await user.click(within(filterGroup()).getByRole("button", { name: "Paused" }))
    expect(screen.getByText("Free 20 GB")).toBeTruthy()
    expect(screen.queryByText("Migrate photos")).toBeNull()
  })

  it("loads again on the goal.state event", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "goal.state", data: { session_id: "s2", status: "paused" } }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/goals")
  })

  it("shows No open goals with Open chat", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue([])
    const { router } = await mk()
    expect(screen.getByText("No open goals.")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Open chat" }))
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
  })
  it("opens the page of the route and goes to page 1 on a filter change", async () => {
    const user = setup()
    const many = Array.from({ length: 25 }, (_, i) =>
      goal({ session_id: "g" + i, title: "T" + i, goal: "Goal " + i }),
    )
    vi.mocked(api).mockResolvedValue(many)
    const { router } = await mk("/goals?page=2")
    expect(screen.getByText("21–25 of 25")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Page 2" }).getAttribute("aria-current")).toBe("page")
    expect(screen.getByText("Goal 24")).toBeTruthy()
    expect(screen.queryByText("Goal 0")).toBeNull()
    await user.click(within(filterGroup()).getByRole("button", { name: "Active" }))
    await flushPromises()
    expect(router.currentRoute.value.query.page).toBeUndefined()
    expect(screen.getByText("1–20 of 25")).toBeTruthy()
    expect(screen.getByText("Goal 0")).toBeTruthy()
  })
})

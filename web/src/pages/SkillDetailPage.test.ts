import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn(), isSecure: vi.fn(() => true) }))
import { api } from "../api/client"
import { confirm, toast } from "../stores/ui"
import SkillDetailPage from "./SkillDetailPage.vue"

const Blank = { render: () => null }
const row = (o: object) => ({
  name: "release-notes",
  description: "d",
  trust: "community",
  source: "github.com/agentskills/release-notes",
  commit: "8c41d0e",
  pinned: false,
  verdict: "caution",
  required_env: [],
  check: "",
  update: false,
  actions: ["update", "uninstall"],
  ...o,
})
const view = (o: object = {}, r: object = {}) => ({
  skill: row(r),
  body: "# release-notes\n\n<script>alert(1)</script>",
  files: ["scripts/notes.sh"],
  ref: "v1.4.0",
  installed_at: "2026-11-02T10:00:00Z",
  updated_at: "2026-11-14T10:00:00Z",
  content_hash: "ab12",
  override: false,
  findings: [
    {
      severity: "medium",
      category: "persistence",
      description: "Writes a cron entry",
      file: "SKILL.md",
      line: 41,
      match: "crontab -l",
    },
  ],
  unscanned: ["big.bin"],
  ...o,
})

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/skills", name: "skills", component: Blank },
      { path: "/skills/:name", name: "skill", component: SkillDetailPage },
    ],
  })
  await router.push("/skills/release-notes")
  const w = mount(SkillDetailPage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(view())
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})
afterEach(() => vi.useRealTimers())

const finding = (severity: string) => ({
  severity,
  category: "c",
  description: "d" + severity,
  file: "SKILL.md",
  line: 3,
  match: "m",
})

describe("SkillDetailPage", () => {
  it("draws two findings at the same place and marks critical and high as bad", async () => {
    vi.mocked(api).mockResolvedValue(
      view({ findings: [finding("critical"), finding("high"), finding("medium")] }),
    )
    const w = await mk()
    const chips = w.findAll("td .chip").map((c) => [c.text(), c.classes().includes("chip-bad")])
    expect(chips).toEqual([
      ["critical", true],
      ["high", true],
      ["medium", false],
    ])
    expect(w.findAll("tbody tr")).toHaveLength(3)
  })

  it("toasts the error text of a failed task", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(
      async (m: string, p: string) =>
        (m === "POST"
          ? { task_id: "t1" }
          : p === "/api/tasks/t1"
            ? { id: "t1", kind: "skills", state: "error", text: "network down" }
            : view()) as any,
    )
    const w = await mk()
    await w
      .findAll("button")
      .find((b) => b.text() === "Update")!
      .trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("network down", "error")
  })

  it("reloads the detail when the task is done", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    vi.mocked(api).mockImplementation(
      async (m: string, p: string) =>
        (m === "POST"
          ? { task_id: "t1" }
          : p === "/api/tasks/t1"
            ? { id: "t1", kind: "skills", state: "done", text: "" }
            : view()) as any,
    )
    const w = await mk()
    vi.mocked(api).mockClear()
    await w
      .findAll("button")
      .find((b) => b.text() === "Update")!
      .trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/release-notes")
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.")
  })

  it("shows SKILL.md as safe Markdown, the lock data, and the findings", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/release-notes")
    expect(w.find(".md script").exists()).toBe(false)
    expect(w.text()).toContain("8c41d0e")
    expect(w.text()).toContain("1 finding · 1 not scanned")
    expect(w.text()).toContain("SKILL.md:41")
    expect(w.text()).toContain("scripts/notes.sh")
    expect(w.findAll("button").map((b) => b.text())).toEqual(["Update", "Uninstall"])
  })

  it("shows no scan for a builtin skill", async () => {
    vi.mocked(api).mockResolvedValue(
      view(
        { findings: [], ref: "", installed_at: "", updated_at: "", content_hash: "" },
        { trust: "builtin", source: "", commit: "", actions: ["reset"] },
      ),
    )
    const w = await mk()
    expect(w.text()).not.toContain("Guard scan")
    expect(w.text()).toContain("Not installed from a source.")
    expect(w.findAll("button").map((b) => b.text())).toEqual(["Reset"])
  })
})

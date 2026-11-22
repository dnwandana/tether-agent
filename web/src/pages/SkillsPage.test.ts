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
import { confirm, isSecure, toast } from "../stores/ui"
import SkillsPage from "./SkillsPage.vue"

const Blank = { render: () => null }
const skill = (o: object) => ({
  name: "",
  description: "d",
  trust: "community",
  source: "src",
  commit: "",
  pinned: false,
  verdict: "safe",
  required_env: [],
  check: "",
  update: false,
  actions: ["update", "uninstall"],
  ...o,
})
const installed = [
  skill({ name: "tether-skills", trust: "builtin", check: "modified locally", actions: ["reset"] }),
  skill({ name: "release-notes", check: "update available", update: true }),
  skill({
    name: "pr-review",
    trust: "trusted",
    check: "pinned",
    pinned: true,
    actions: ["uninstall"],
  }),
]
const catalog = [
  {
    name: "changelog-check",
    description: "Check CHANGELOG.md",
    url: "github.com/a/changelog-check",
    installed: false,
    verdict: "",
    source_trust: "community",
  },
  {
    name: "repo-sync",
    description: "Sync forks",
    url: "github.com/a/repo-sync",
    installed: false,
    verdict: "dangerous",
    source_trust: "external",
  },
  {
    name: "old-one",
    description: "Done",
    url: "github.com/a/old-one",
    installed: true,
    verdict: "caution",
    source_trust: "trusted",
  },
]

async function mk(path = "/skills") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/skills", name: "skills", component: SkillsPage },
      { path: "/skills/:name", name: "skill", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(SkillsPage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}
const btns = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)

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
      async (m: string, p: string) =>
        (m === "POST" ? { task_id: "t1" } : p === "/api/skills" ? installed : catalog) as any,
    )
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

afterEach(() => vi.useRealTimers())
const fake = () =>
  vi.useFakeTimers({
    toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
  })

describe("SkillsPage", () => {
  it("toasts the error text of a failed task", async () => {
    fake()
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation(async (m: string, p: string, ...r: any[]) =>
      p === "/api/tasks/t1"
        ? ({
            id: "t1",
            kind: "skills",
            state: "error",
            text: "The guard found a dangerous skill.",
          } as any)
        : base(m as any, p, ...r),
    )
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[0].trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks/t1")
    expect(toast).toHaveBeenCalledWith("The guard found a dangerous skill.", "error")
  })

  it("reloads the list when the task is done", async () => {
    fake()
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation(async (m: string, p: string, ...r: any[]) =>
      p === "/api/tasks/t1"
        ? ({ id: "t1", kind: "skills", state: "done", text: "ok" } as any)
        : base(m as any, p, ...r),
    )
    const w = await mk()
    vi.mocked(api).mockClear()
    await btns(w, "Update")[0].trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills")
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.")
  })

  it("shows the verdict and the source trust in the catalog", async () => {
    const w = await mk("/skills?tab=catalog")
    const row = w.findAll("tr").find((r) => r.text().includes("old-one"))!
    expect(row.find(".chip-warn").text()).toBe("caution")
    expect(row.find(".chip-ok").text()).toBe("trusted")
    expect(
      w
        .findAll("tr")
        .find((r) => r.text().includes("repo-sync"))!
        .find(".chip-bad")
        .text(),
    ).toBe("dangerous")
  })

  it("opens the terminal command instead of a dangerous install", async () => {
    const w = await mk("/skills?tab=catalog")
    vi.mocked(api).mockClear()
    await btns(w, "Install")[1].trigger("click")
    await flushPromises()
    expect(api).not.toHaveBeenCalledWith("POST", "/api/skills/repo-sync/install")
    expect(w.text()).toContain("tether skills install github.com/a/repo-sync")
    expect(w.text()).toContain(
      "Install it from a terminal on the host. The terminal asks you to type the name.",
    )
  })

  it("copies the terminal command of a dangerous skill", async () => {
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[1].trigger("click")
    await flushPromises()
    await btns(w, "Copy command")[0].trigger("click")
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith("tether skills install github.com/a/repo-sync")
    expect(toast).toHaveBeenCalledWith("Copied.")
  })

  it("shows the HTTPS note in place of Copy on a page that is not secure", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    try {
      const w = await mk("/skills?tab=catalog")
      await btns(w, "Install")[1].trigger("click")
      await flushPromises()
      expect(btns(w, "Copy command")).toHaveLength(0)
      expect(w.text()).toContain("Needs HTTPS or localhost")
    } finally {
      vi.mocked(isSecure).mockReturnValue(true)
    }
  })

  it("shows Update, Reset, and Uninstall only where they apply", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills")
    expect(btns(w, "Update")).toHaveLength(1)
    expect(btns(w, "Reset")).toHaveLength(1)
    expect(btns(w, "Uninstall")).toHaveLength(2)
    expect(w.text()).toContain("update available")
    expect(w.text()).toContain("modified locally")
    await btns(w, "Update")[0].trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/release-notes/update")
    expect(toast).toHaveBeenCalledWith("Updating /release-notes…", "info")
  })

  it("asks before an uninstall", async () => {
    const w = await mk()
    await btns(w, "Uninstall")[1].trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Uninstall /pr-review?",
      body: "The skill folder is removed. Cron jobs that use it lose it.",
      confirmLabel: "Uninstall",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/pr-review/uninstall")
  })

  it("does not post when the owner declines", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const w = await mk()
    await btns(w, "Reset")[0].trigger("click")
    await flushPromises()
    expect(api).not.toHaveBeenCalledWith("POST", "/api/skills/tether-skills/reset")
  })

  it("installs a catalog skill", async () => {
    const w = await mk("/skills?tab=catalog")
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/catalog")
    expect(btns(w, "Install")).toHaveLength(2)
    expect(w.text()).toContain("installed")
    await btns(w, "Install")[0].trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/changelog-check/install")
    expect(toast).toHaveBeenCalledWith("Installing /changelog-check…", "info")
  })

  it("filters the catalog by the search text", async () => {
    const w = await mk("/skills?tab=catalog")
    await w.find(".search input").setValue("fork")
    expect(w.text()).toContain("repo-sync")
    expect(w.text()).not.toContain("changelog-check")
  })
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

const h = vi.hoisted(() => ({
  follow: new Map<string, (t: unknown) => void>(),
  live: null as null | {
    load: () => Promise<void>
    opts?: { events?: string[]; filter?: (n: string, d: unknown) => boolean }
  },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", async () => ({
  ...(await vi.importActual<object>("../stores/ui")),
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/live", async () => {
  const actual = await vi.importActual<object>("../lib/live")
  const { onMounted, ref } = await import("vue")
  return {
    ...actual,
    useLiveData: (load: () => Promise<void>, opts?: any) => {
      h.live = { load, opts }
      onMounted(() => void load())
      return { stale: ref(false), updatedAt: ref(null), refresh: load }
    },
    followTask: (id: string, fn: (t: unknown) => void) => {
      h.follow.set(id, fn)
      return () => h.follow.delete(id)
    },
  }
})
import { api } from "../api/client"
import { confirm, toast, ui } from "../stores/ui"
import FileViewer from "../components/FileViewer.vue"
import ResultNote from "../components/ResultNote.vue"
import SkillDetailPage from "./SkillDetailPage.vue"

const Blank = { render: () => null }
const row = (o: object) => ({
  name: "release-notes",
  description: "Writes the release notes.",
  trust: "community",
  source: "github.com/agentskills/release-notes",
  commit: "8c41d0e",
  pinned: false,
  verdict: "caution",
  required_env: ["GH_TOKEN"],
  check: "",
  update: false,
  actions: ["update", "uninstall"],
  ...o,
})
const view = (o: object = {}, r: object = {}) => ({
  skill: row(r),
  body: "# release-notes\n\n<script>alert(1)</script>",
  files: ["SKILL.md", "scripts/notes.sh"],
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
      pattern: "cron_persistence",
    },
  ],
  unscanned: ["big.bin"],
  ...o,
})
const task = (o: object) => ({
  id: "t1",
  kind: "skills",
  state: "running",
  text: "",
  subject: "release-notes",
  code: "",
  waiting: false,
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
  const w = mount(SkillDetailPage, {
    global: { plugins: [router], stubs: { LiveHeader: true, FileViewer: true } },
  })
  await flushPromises()
  return Object.assign(w, { router })
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!
const note = (w: any) => w.findComponent(ResultNote).props("note")
const end = async (t: object) => {
  h.follow.get("t1")!(task(t))
  await flushPromises()
}

beforeEach(() => {
  h.follow.clear()
  h.live = null
  ui.trayOpen = false
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string, p: string) => {
      if (m === "POST") return { task_id: "t1" } as any
      if (p.startsWith("/api/tasks")) return [] as any
      return view() as any
    })
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

const finding = (severity: string) => ({
  severity,
  category: "c",
  description: "d" + severity,
  file: "SKILL.md",
  line: 3,
  match: "m",
  pattern: "p",
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

  it("shows the error text of a failed task in a result note", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await flushPromises()
    await end({ state: "error", text: "network down" })
    expect(note(w)).toMatchObject({ title: "/release-notes", kind: "error", text: "network down" })
  })

  it("reloads the detail only at the end of a skills task", async () => {
    await mk()
    expect(h.live!.opts!.events).toEqual(["task"])
    const f = h.live!.opts!.filter!
    expect(f("task", task({ state: "done" }))).toBe(true)
    expect(f("task", task({ state: "running" }))).toBe(false)
    expect(f("task", task({ kind: "backup", state: "done" }))).toBe(false)
  })

  it("opens a support file in the viewer", async () => {
    const w = await mk()
    await btn(w, "scripts/notes.sh").trigger("click")
    await flushPromises()
    expect(w.findComponent(FileViewer).props()).toEqual({
      skill: "release-notes",
      path: "scripts/notes.sh",
      line: null,
    })
  })

  it("links a finding to its line and shows its pattern", async () => {
    const w = await mk()
    expect(w.text()).toContain("cron_persistence")
    await btn(w, "SKILL.md:41").trigger("click")
    await flushPromises()
    expect(w.findComponent(FileViewer).props()).toEqual({
      skill: "release-notes",
      path: "SKILL.md",
      line: 41,
    })
  })

  it("shows the description, the env, the run line, the override, and the unscanned files", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string, p: string) =>
        (p.startsWith("/api/tasks") ? [] : view({ override: true })) as any,
    )
    const w = await mk()
    expect(w.text()).toContain("Writes the release notes.")
    expect(w.text()).toContain("Needs: GH_TOKEN")
    expect(w.text()).toContain("Run it in chat: /release-notes")
    expect(w.find(".chip-warn").text()).toBe("Owner override")
    expect(w.text()).toContain("Not scanned: big.bin")
  })

  it("offers Update anyway after a caution refusal and posts force", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await flushPromises()
    await end({ state: "error", code: "need_force", text: "caution: 1 finding" })
    expect(note(w)).toMatchObject({ kind: "warn", text: "caution: 1 finding" })
    await btn(w, "Update anyway…").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Update /release-notes anyway?",
      body: "The skill guard rated it caution. A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Update anyway",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/release-notes/update", { force: true })
  })

  it("opens the terminal panel after a dangerous refusal", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await flushPromises()
    await end({ state: "error", code: "need_override", text: "dangerous: 2 findings" })
    expect(w.text()).toContain("tether skills update release-notes")
    expect(w.text()).toContain("The terminal asks you to type the name of the skill.")
    expect(w.text()).not.toContain("--force")
  })

  it("disables the actions while a task runs and tells the owner to answer the card", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await flushPromises()
    expect(btn(w, "Update").attributes("disabled")).toBeDefined()
    expect(btn(w, "Uninstall").attributes("disabled")).toBeDefined()
    expect(w.find(".page-h").text()).toContain("Running…")
    await end({ waiting: true })
    expect(w.text()).toContain("Answer the card in ⏳ Waiting.")
    await btn(w, "Open Waiting").trigger("click")
    expect(ui.trayOpen).toBe(true)
    await end({ state: "done", text: "" })
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.", "ok")
  })

  it("asks one confirm before an uninstall and says Not removed. after No on the card", async () => {
    const w = await mk()
    await btn(w, "Uninstall").trigger("click")
    await vi.waitFor(() => expect(h.follow.has("t1")).toBe(true))
    expect(confirm).toHaveBeenCalledWith({
      title: "Remove /release-notes?",
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    await end({ state: "error", text: "Remove /x?\nCancelled.\ncancelled" })
    expect(toast).toHaveBeenCalledWith("Not removed.", "info")
    expect(w.router.currentRoute.value.name).toBe("skill")
  })

  it("stops following a task when the page unmounts", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await vi.waitFor(() => expect(h.follow.has("t1")).toBe(true))
    w.unmount()
    expect(h.follow.has("t1")).toBe(false)
  })

  it("stops a follow that starts after the page unmounts", async () => {
    const w = await mk()
    let post!: (v: unknown) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (post = r)) as never)
    await btn(w, "Update").trigger("click")
    await flushPromises()
    w.unmount()
    post({ task_id: "t1" })
    await flushPromises()
    expect(h.follow.has("t1")).toBe(false)
  })

  it("keeps the shown skill when a reload fails", async () => {
    const w = await mk()
    vi.mocked(api).mockRejectedValueOnce(new Error("The gateway did not answer."))
    await h.live!.load()
    await flushPromises()
    expect(w.text()).toContain("release-notes")
    expect(w.findComponent({ name: "StateBlock" }).exists()).toBe(false)
    expect(toast).toHaveBeenCalledWith("The gateway did not answer.", "error")
  })

  it("goes back to Skills after an uninstall", async () => {
    const w = await mk()
    await btn(w, "Uninstall").trigger("click")
    await flushPromises()
    await end({ state: "done", text: "Removed." })
    await flushPromises()
    expect(w.router.currentRoute.value.name).toBe("skills")
  })

  it("shows the lost text when the gateway restarted", async () => {
    const w = await mk()
    await btn(w, "Update").trigger("click")
    await flushPromises()
    await end({ state: "lost" })
    expect(note(w)).toMatchObject({
      kind: "error",
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows SKILL.md as safe Markdown, the lock data, and the findings", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/release-notes")
    expect(w.find(".md script").exists()).toBe(false)
    expect(w.text()).toContain("8c41d0e")
    expect(w.text()).toContain("1 finding · 1 not scanned")
    expect(w.text()).toContain("SKILL.md:41")
    expect(w.text()).toContain("scripts/notes.sh")
    expect(w.findAll(".page-h button").map((b) => b.text())).toEqual(["Update", "Uninstall"])
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
    expect(w.findAll(".page-h button").map((b) => b.text())).toEqual(["Reset"])
  })
})

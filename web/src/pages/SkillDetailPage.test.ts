import { describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h as hv } from "vue"
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
import SkillDetailPage from "./SkillDetailPage.vue"
import { fullTime, shortTime } from "../lib/time"

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

// Keeps the last props of the stubbed children.
const seen: { note: any; file: any } = { note: null, file: null }
const NoteStub = defineComponent({
  name: "ResultNote",
  props: ["note"],
  setup(p) {
    return () => ((seen.note = p.note), p.note ? hv("div", p.note.text) : null)
  },
})
const FileStub = defineComponent({
  name: "FileViewer",
  props: ["skill", "path", "line"],
  setup(p) {
    return () => ((seen.file = { skill: p.skill, path: p.path, line: p.line }), hv("div"))
  },
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
  const user = userEvent.setup()
  const r = render(SkillDetailPage, {
    global: {
      plugins: [router],
      stubs: { LiveHeader: true, FileViewer: FileStub, ResultNote: NoteStub },
    },
  })
  await flushPromises()
  return { ...r, router, user }
}
const btn = (name: string) => screen.getByRole("button", { name })
const note = () => seen.note
const bodyRows = () =>
  screen.getAllByRole("row").filter((r) => within(r).queryAllByRole("cell").length > 0)
const tone = (el: HTMLElement) => el.closest("[data-tone]")?.getAttribute("data-tone")
// The h1 of the page. The Markdown of SKILL.md can hold its own h1.
const pageTitle = () =>
  screen.getAllByRole("heading", { level: 1 }).find((x) => !x.closest(".typeset"))!
// The buttons of the page header: the row that holds the h1.
const headerButtons = () =>
  within(pageTitle().parentElement!)
    .queryAllByRole("button")
    .map((b) => b.textContent!.trim())
const text = () => document.body.textContent!.replace(/\s+/g, " ")
const end = async (t: object) => {
  h.follow.get("t1")!(task(t))
  await flushPromises()
}
const click = async (user: ReturnType<typeof userEvent.setup>, name: string) => {
  await user.click(btn(name))
  await flushPromises()
}

beforeEach(() => {
  seen.note = null
  seen.file = null
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
    await mk()
    const rows = bodyRows()
    expect(rows).toHaveLength(3)
    const badges = rows.map((r) => {
      const b = r.querySelector<HTMLElement>("[data-tone]")!
      return [b.textContent!.trim(), tone(b) === "bad"]
    })
    expect(badges).toEqual([
      ["critical", true],
      ["high", true],
      ["medium", false],
    ])
  })

  it("shows the error text of a failed task in a result note", async () => {
    const { user } = await mk()
    await click(user, "Update")
    await end({ state: "error", text: "network down" })
    expect(note()).toMatchObject({ title: "/release-notes", kind: "error", text: "network down" })
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
    const { user } = await mk()
    await click(user, "scripts/notes.sh")
    expect(seen.file).toEqual({ skill: "release-notes", path: "scripts/notes.sh", line: null })
  })

  it("links a finding to its line and shows its pattern", async () => {
    const { user } = await mk()
    expect(screen.getByText("cron_persistence")).toBeTruthy()
    await click(user, "SKILL.md:41")
    expect(seen.file).toEqual({ skill: "release-notes", path: "SKILL.md", line: 41 })
  })

  it("shows the description, the env, the run line, the override, and the unscanned files", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string, p: string) =>
        (p.startsWith("/api/tasks") ? [] : view({ override: true })) as any,
    )
    await mk()
    expect(text()).toContain("Writes the release notes.")
    expect(text()).toContain("Needs: GH_TOKEN")
    expect(text()).toContain("Run it in chat: /release-notes")
    expect(tone(screen.getByText("Owner override"))).toBe("warn")
    expect(text()).toContain("Not scanned: big.bin")
  })

  it("offers Update anyway after a caution refusal and posts force", async () => {
    const { user } = await mk()
    await click(user, "Update")
    await end({ state: "error", code: "need_force", text: "caution: 1 finding" })
    expect(note()).toMatchObject({ kind: "warn", text: "caution: 1 finding" })
    await click(user, "Update anyway…")
    expect(confirm).toHaveBeenCalledWith({
      title: "Update /release-notes anyway?",
      body: "The skill guard rated it caution. A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Update anyway",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/release-notes/update", { force: true })
  })

  it("opens the terminal panel after a dangerous refusal", async () => {
    const { user } = await mk()
    await click(user, "Update")
    await end({ state: "error", code: "need_override", text: "dangerous: 2 findings" })
    expect(text()).toContain("tether skills update release-notes")
    expect(text()).toContain("The terminal asks you to type the name of the skill.")
    expect(text()).not.toContain("--force")
  })

  it("disables the actions while a task runs and tells the owner to answer the card", async () => {
    const { user } = await mk()
    await click(user, "Update")
    expect((btn("Update") as HTMLButtonElement).disabled).toBe(true)
    expect((btn("Uninstall") as HTMLButtonElement).disabled).toBe(true)
    expect(within(pageTitle().parentElement!).getByText("Running…")).toBeTruthy()
    await end({ waiting: true })
    expect(screen.getByText("Answer the card in ⏳ Waiting.")).toBeTruthy()
    await click(user, "Open Waiting")
    expect(ui.trayOpen).toBe(true)
    await end({ state: "done", text: "" })
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.", "ok")
  })

  it("asks one confirm before an uninstall and says Not removed. after No on the card", async () => {
    const { user, router } = await mk()
    await user.click(btn("Uninstall"))
    await vi.waitFor(() => expect(h.follow.has("t1")).toBe(true))
    expect(confirm).toHaveBeenCalledWith({
      title: "Remove /release-notes?",
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    await end({ state: "error", text: "Remove /x?\nCancelled.\ncancelled" })
    expect(toast).toHaveBeenCalledWith("Not removed.", "info")
    expect(router.currentRoute.value.name).toBe("skill")
  })

  it("stops following a task when the page unmounts", async () => {
    const { user, unmount } = await mk()
    await user.click(btn("Update"))
    await vi.waitFor(() => expect(h.follow.has("t1")).toBe(true))
    unmount()
    expect(h.follow.has("t1")).toBe(false)
  })

  it("stops a follow that starts after the page unmounts", async () => {
    const { user, unmount } = await mk()
    let post!: (v: unknown) => void
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (post = r)) as never)
    await click(user, "Update")
    unmount()
    post({ task_id: "t1" })
    await flushPromises()
    expect(h.follow.has("t1")).toBe(false)
  })

  it("keeps the shown skill when a reload fails", async () => {
    await mk()
    vi.mocked(api).mockRejectedValueOnce(new Error("The gateway did not answer."))
    await h.live!.load()
    await flushPromises()
    expect(pageTitle().textContent).toBe("/release-notes")
    expect(screen.queryByText("The gateway did not answer.")).toBeNull()
    expect(toast).toHaveBeenCalledWith("The gateway did not answer.", "error")
  })

  it("goes back to Skills after an uninstall", async () => {
    const { user, router } = await mk()
    await click(user, "Uninstall")
    await end({ state: "done", text: "Removed." })
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("skills")
  })

  it("shows the lost text when the gateway restarted", async () => {
    const { user } = await mk()
    await click(user, "Update")
    await end({ state: "lost" })
    expect(note()).toMatchObject({
      kind: "error",
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows SKILL.md as safe Markdown, the lock data, and the findings", async () => {
    const { container } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/release-notes")
    expect(container.querySelector(".typeset")).not.toBeNull()
    expect(container.querySelector(".typeset script")).toBeNull()
    expect(text()).toContain("8c41d0e")
    expect(text()).toContain("1 finding · 1 not scanned")
    expect(text()).toContain("SKILL.md:41")
    expect(text()).toContain("scripts/notes.sh")
    expect(headerButtons()).toEqual(["Update", "Uninstall"])
  })

  it("shows no scan for a builtin skill", async () => {
    vi.mocked(api).mockResolvedValue(
      view(
        { findings: [], ref: "", installed_at: "", updated_at: "", content_hash: "" },
        { trust: "builtin", source: "", commit: "", actions: ["reset"] },
      ),
    )
    await mk()
    expect(screen.queryByText("Guard scan")).toBeNull()
    expect(screen.getByText("Not installed from a source.")).toBeTruthy()
    expect(headerButtons()).toEqual(["Reset"])
  })

  it("shows the install date in the shared short format", async () => {
    const { container } = await mk()
    const dd = container.querySelector('[data-test="installed-at"]')!
    expect(dd.textContent!.trim()).toBe(shortTime("2026-11-02T10:00:00Z"))
    const span = dd.querySelector("span")!
    expect(span.getAttribute("title")).toBe(fullTime("2026-11-02T10:00:00Z"))
    expect(span.classList.contains("whitespace-nowrap")).toBe(true)
    const upd = container.querySelector('[data-test="updated-at"] span')!
    expect(upd.classList.contains("whitespace-nowrap")).toBe(true)
  })

  it("shows a dash for an empty install date", async () => {
    vi.mocked(api).mockResolvedValue(view({ installed_at: "", updated_at: "" }) as any)
    const { container } = await mk()
    expect(container.querySelector('[data-test="installed-at"]')!.textContent!.trim()).toBe("—")
  })

  it("uses no legacy class", async () => {
    const { container } = await mk()
    expect(
      container.querySelector(
        ".page-h, .back, .chip, .btn, .li, .grid-2, .card-h, .kv, .md, .section-h, .mono, .c-3",
      ),
    ).toBeNull()
  })
})

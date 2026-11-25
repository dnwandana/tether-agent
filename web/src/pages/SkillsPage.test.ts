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
import { confirm, isSecure, listLink, toast, ui } from "../stores/ui"
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
const many = (n: number) =>
  Array.from({ length: n }, (_, i) => skill({ name: `k${String(i).padStart(2, "0")}` }))
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
const task = (o: object) => ({
  id: "t1",
  kind: "skills",
  state: "running",
  text: "",
  subject: "",
  code: "",
  waiting: false,
  ...o,
})
let tasks: unknown[] = []
let rowsOf = installed
let catalogOf: unknown[] = catalog

// Keeps the last note prop, so the tests check the kind and the title of the result note.
let lastNote: any = null
const NoteStub = defineComponent({
  name: "ResultNote",
  props: ["note"],
  setup(p) {
    return () => ((lastNote = p.note), p.note ? hv("div", p.note.text) : null)
  },
})

async function mk(path = "/skills") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/skills", name: "skills", component: SkillsPage },
      { path: "/skills/:name", name: "skill", component: Blank },
    ],
  })
  await router.push(path)
  const user = userEvent.setup()
  const r = render(SkillsPage, {
    global: { plugins: [router], stubs: { LiveHeader: true, ResultNote: NoteStub } },
  })
  await flushPromises()
  return { ...r, router, user }
}
const btns = (name: string) => screen.queryAllByRole("button", { name })
const note = () => lastNote
const bodyRows = () =>
  screen.getAllByRole("row").filter((r) => within(r).queryAllByRole("cell").length > 0)
const rowOf = (text: string) => bodyRows().find((r) => r.textContent!.includes(text))!
const tone = (el: HTMLElement) => el.closest("[data-tone]")?.getAttribute("data-tone")
const end = async (id: string, t: object) => {
  h.follow.get(id)!(task(t))
  await flushPromises()
}

beforeEach(() => {
  lastNote = null
  h.follow.clear()
  h.live = null
  tasks = []
  rowsOf = installed
  catalogOf = catalog
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
      if (p === "/api/tasks?kind=skills") return tasks as any
      if (p === "/api/skills") return rowsOf as any
      return catalogOf as any
    })
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

describe("SkillsPage", () => {
  it("shows the trust agent-created as local and clamps a long description", async () => {
    const long = "Writes the release notes of the repository. ".repeat(12).trim()
    rowsOf = [
      skill({
        name: "my-notes",
        trust: "agent-created",
        description: long,
        actions: ["uninstall"],
      }),
    ]
    await mk()
    const row = rowOf("my-notes")
    expect(tone(within(row).getByText("local"))).toBe("info")
    expect(row.textContent).not.toContain("agent-created")
    expect(within(row).getByTitle(long).classList.contains("line-clamp-2")).toBe(true)
  })

  it("shows a catalog source trust agent-created as local and clamps the description", async () => {
    const long = "Syncs the forks of the owner. ".repeat(12).trim()
    catalogOf = [
      {
        name: "own-sync",
        description: long,
        url: "github.com/a/own-sync",
        installed: false,
        verdict: "",
        source_trust: "agent-created",
      },
    ]
    await mk("/skills?tab=catalog")
    const row = rowOf("own-sync")
    expect(tone(within(row).getByText("local"))).toBe("info")
    expect(row.textContent).not.toContain("agent-created")
    expect(within(row).getByTitle(long).classList.contains("line-clamp-2")).toBe(true)
  })

  it("shows the error text of a failed task in a result note", async () => {
    const { user } = await mk("/skills?tab=catalog")
    await user.click(btns("Install")[0])
    await flushPromises()
    await end("t1", {
      state: "error",
      subject: "changelog-check",
      text: "The guard found a problem.",
    })
    expect(note()).toMatchObject({
      title: "/changelog-check",
      kind: "error",
      text: "The guard found a problem.",
    })
    expect(toast).not.toHaveBeenCalledWith("The guard found a problem.", "error")
  })

  it("reloads the lists only at the end of a skills task", async () => {
    await mk()
    expect(h.live!.opts!.events).toEqual(["task"])
    const f = h.live!.opts!.filter!
    expect(f("task", task({ state: "done" }))).toBe(true)
    expect(f("task", task({ state: "error" }))).toBe(true)
    expect(f("task", task({ state: "running" }))).toBe(false)
    expect(f("task", task({ kind: "doctor", state: "done" }))).toBe(false)
  })

  it("asks one confirm before an uninstall and says Not removed. after No on the card", async () => {
    const { user } = await mk()
    await user.click(btns("Uninstall")[1])
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Remove /pr-review?",
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/pr-review/uninstall")
    await end("t1", {
      state: "error",
      subject: "pr-review",
      text: "Remove /data/skills/pr-review?\nCancelled.\ncancelled",
    })
    expect(toast).toHaveBeenCalledWith("Not removed.", "info")
    expect(note()).toBeNull()
  })

  it("offers Install anyway after a caution refusal and posts force", async () => {
    const { user } = await mk("/skills?tab=catalog")
    await user.click(btns("Install")[0])
    await flushPromises()
    await end("t1", {
      state: "error",
      code: "need_force",
      subject: "changelog-check",
      text: "caution: 1 finding",
    })
    expect(note()).toMatchObject({ kind: "warn", text: "caution: 1 finding" })
    await user.click(btns("Install anyway…")[0])
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Install /changelog-check anyway?",
      body: "The skill guard rated it caution. A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Install anyway",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/changelog-check/install", {
      force: true,
    })
  })

  it("says that Install anyway replaces a skill of the same name from another source", async () => {
    catalogOf = [{ ...catalog[0], name: "release-notes", url: "github.com/b/release-notes" }]
    const { user } = await mk("/skills?tab=catalog")
    await user.click(btns("Install")[0])
    await flushPromises()
    await end("t1", { state: "error", code: "need_force", subject: "release-notes" })
    await user.click(btns("Install anyway…")[0])
    await flushPromises()
    expect(vi.mocked(confirm).mock.calls[0][0].body).toBe(
      "The skill guard rated it caution. A confirm card follows in ⏳ Waiting. It replaces the installed /release-notes from src.",
    )
  })

  it("opens the terminal panel after a dangerous refusal", async () => {
    const { user } = await mk()
    await user.click(btns("Update")[0])
    await flushPromises()
    await end("t1", {
      state: "error",
      code: "need_override",
      subject: "release-notes",
      text: "dangerous: 2 findings",
    })
    const text = document.body.textContent!
    expect(text).toContain("tether skills update release-notes")
    expect(text).toContain("The terminal asks you to type the name of the skill.")
    expect(text).toContain("dangerous: 2 findings")
  })

  it("tells the owner to answer the card while the task waits", async () => {
    const { user } = await mk()
    await user.click(btns("Update")[0])
    await flushPromises()
    await end("t1", { waiting: true, subject: "release-notes" })
    expect(screen.getByText("Answer the card in ⏳ Waiting.")).toBeTruthy()
    await user.click(btns("Open Waiting")[0])
    expect(ui.trayOpen).toBe(true)
  })

  it("disables each skill action while a task runs and marks the running skill", async () => {
    const { user } = await mk()
    await user.click(btns("Update")[0])
    await flushPromises()
    for (const t of ["Update", "Reset", "Uninstall", "Check for updates"])
      for (const b of btns(t)) expect((b as HTMLButtonElement).disabled, t).toBe(true)
    expect(within(rowOf("release-notes")).getByText("Running…")).toBeTruthy()
    await end("t1", { state: "done", subject: "release-notes", text: "ok" })
    expect((btns("Update")[0] as HTMLButtonElement).disabled).toBe(false)
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.", "ok")
  })

  it("shows the lost text when the gateway restarted", async () => {
    const { user } = await mk()
    await user.click(btns("Update")[0])
    await flushPromises()
    await end("t1", { state: "lost", subject: "release-notes" })
    expect(note()).toMatchObject({
      kind: "error",
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows the newest skills task again on return", async () => {
    tasks = [task({ id: "t9", state: "done", subject: "release-notes", text: "Updated." })]
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks?kind=skills")
    expect(note()).toMatchObject({ title: "/release-notes", kind: "info", text: "Updated." })
  })

  it("follows a skills task that still runs on return", async () => {
    tasks = [task({ id: "t8", subject: "pr-review" })]
    await mk()
    expect(h.follow.has("t8")).toBe(true)
    expect((btns("Uninstall")[0] as HTMLButtonElement).disabled).toBe(true)
  })

  it("stops following a task when the page unmounts", async () => {
    tasks = [task({ id: "t8", subject: "pr-review" })]
    const { unmount } = await mk()
    expect(h.follow.has("t8")).toBe(true)
    unmount()
    expect(h.follow.has("t8")).toBe(false)
  })

  it("keeps the list and shows a toast when a live reload fails", async () => {
    await mk()
    expect(screen.getByText("/pr-review")).toBeTruthy()
    vi.mocked(api).mockImplementation(async () => {
      throw new Error("boom")
    })
    await h.live!.load()
    await flushPromises()
    expect(screen.getByText("/pr-review")).toBeTruthy()
    expect(toast).toHaveBeenCalledWith("boom", "error")
  })

  it("switches to the catalog with the tabs", async () => {
    const { user, router } = await mk()
    await user.click(screen.getByRole("tab", { name: "Catalog" }))
    await flushPromises()
    expect(router.currentRoute.value.query.tab).toBe("catalog")
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/catalog")
  })

  it("shows the empty state with Open the catalog", async () => {
    rowsOf = []
    const { user, router } = await mk()
    expect(screen.getByText("No skills installed.")).toBeTruthy()
    await user.click(btns("Open the catalog")[0])
    await flushPromises()
    expect(router.currentRoute.value.query.tab).toBe("catalog")
  })

  it("pages the catalog 20 at a time and filters by trust and by install", async () => {
    catalogOf = Array.from({ length: 25 }, (_, i) => ({
      name: `s${String(i).padStart(2, "0")}`,
      description: "d",
      url: `github.com/a/s${i}`,
      installed: i % 2 === 0,
      verdict: "safe",
      source_trust: i < 3 ? "builtin" : "community",
    }))
    const { user, router } = await mk("/skills?tab=catalog")
    expect(bodyRows()).toHaveLength(20)
    expect(screen.getByText("1–20 of 25")).toBeTruthy()
    const nav = screen.getByRole("navigation", { name: "Catalog pages" })
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    await flushPromises()
    expect(bodyRows()).toHaveLength(5)
    expect(router.currentRoute.value.query).toEqual({ tab: "catalog", page: "2" })
    await user.selectOptions(screen.getByRole("combobox", { name: "Trust" }), "local")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ tab: "catalog" })
    expect(bodyRows().map((r) => r.querySelector("b")!.textContent)).toEqual([
      "/s00",
      "/s01",
      "/s02",
    ])
    await user.selectOptions(screen.getByRole("combobox", { name: "Installed" }), "no")
    expect(bodyRows().map((r) => r.querySelector("b")!.textContent)).toEqual(["/s01"])
  })

  it("searches the installed list", async () => {
    const { user } = await mk()
    await user.type(
      screen.getByRole("searchbox", { name: "Search the installed skills" }),
      "review",
    )
    expect(screen.queryByText("/pr-review")).not.toBeNull()
    expect(screen.queryByText("/release-notes")).toBeNull()
  })

  it("shows the verdict and the source trust in the catalog", async () => {
    await mk("/skills?tab=catalog")
    expect(tone(within(rowOf("old-one")).getByText("caution"))).toBe("warn")
    expect(tone(within(rowOf("old-one")).getByText("trusted"))).toBe("ok")
    expect(tone(within(rowOf("repo-sync")).getByText("dangerous"))).toBe("bad")
  })

  it("opens the terminal command instead of a dangerous install", async () => {
    const { user } = await mk("/skills?tab=catalog")
    vi.mocked(api).mockClear()
    await user.click(btns("Install")[1])
    await flushPromises()
    expect(api).not.toHaveBeenCalledWith("POST", "/api/skills/repo-sync/install")
    expect(document.body.textContent).toContain("tether skills install repo-sync")
    expect(document.body.textContent).toContain(
      "The terminal asks you to type the name of the skill.",
    )
  })

  it("copies the terminal command of a dangerous skill", async () => {
    const { user } = await mk("/skills?tab=catalog")
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue()
    await user.click(btns("Install")[1])
    await flushPromises()
    await user.click(btns("Copy command")[0])
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith("tether skills install repo-sync")
    expect(toast).toHaveBeenCalledWith("Copied.")
  })

  it("shows the HTTPS note in place of Copy on a page that is not secure", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    try {
      const { user } = await mk("/skills?tab=catalog")
      await user.click(btns("Install")[1])
      await flushPromises()
      expect(btns("Copy command")).toHaveLength(0)
      expect(screen.getByText("Needs HTTPS or localhost")).toBeTruthy()
    } finally {
      vi.mocked(isSecure).mockReturnValue(true)
    }
  })

  it("shows Update, Reset, and Uninstall only where they apply", async () => {
    const { user } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/skills")
    expect(btns("Update")).toHaveLength(1)
    expect(btns("Reset")).toHaveLength(1)
    expect(btns("Uninstall")).toHaveLength(2)
    expect(screen.getByText("update available")).toBeTruthy()
    expect(screen.getByText("modified locally")).toBeTruthy()
    await user.click(btns("Update")[0])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/release-notes/update")
    expect(toast).toHaveBeenCalledWith("Updating /release-notes…", "info")
  })

  it("does not post when the owner declines", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const { user } = await mk()
    await user.click(btns("Reset")[0])
    await flushPromises()
    expect(api).not.toHaveBeenCalledWith("POST", "/api/skills/tether-skills/reset")
  })

  it("installs a catalog skill", async () => {
    const { user } = await mk("/skills?tab=catalog")
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/catalog")
    expect(btns("Install")).toHaveLength(2)
    expect(screen.getByText("installed")).toBeTruthy()
    await user.click(btns("Install")[0])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/skills/changelog-check/install")
    expect(toast).toHaveBeenCalledWith("Installing /changelog-check…", "info")
  })

  it("filters the catalog by the search text", async () => {
    const { user } = await mk("/skills?tab=catalog")
    await user.type(screen.getByRole("searchbox", { name: "Search 3 skills" }), "fork")
    expect(screen.queryByText("/repo-sync")).not.toBeNull()
    expect(screen.queryByText("/changelog-check")).toBeNull()
  })

  it("uses no legacy class", async () => {
    const { container } = await mk("/skills?tab=catalog")
    expect(
      container.querySelector(
        ".page-h, .li, .btn, .chip, .search, .pager, .pill-note, .clamp-3, .mono, .input",
      ),
    ).toBeNull()
  })

  it("pages the installed list 20 at a time and keeps the page in the URL", async () => {
    rowsOf = many(25)
    const { user, router } = await mk()
    expect(bodyRows()).toHaveLength(20)
    expect(screen.getByText("1–20 of 25")).toBeTruthy()
    const nav = screen.getByRole("navigation", { name: "Installed skills pages" })
    await user.click(within(nav).getByRole("button", { name: "Page 2" }))
    await flushPromises()
    expect(bodyRows()).toHaveLength(5)
    expect(router.currentRoute.value.query).toEqual({ page: "2" })
    expect(listLink("skills")).toEqual({ name: "skills", query: { page: "2" } })
  })

  it("opens the page of the URL", async () => {
    rowsOf = many(25)
    await mk("/skills?page=2")
    expect(bodyRows()).toHaveLength(5)
    expect(screen.getByText("/k20")).toBeTruthy()
  })

  it("goes to page 1 when the installed search changes", async () => {
    rowsOf = many(25)
    const { user, router } = await mk("/skills?page=2")
    await user.type(screen.getByRole("searchbox", { name: "Search the installed skills" }), "k2")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    // k20 to k24 match "k2".
    expect(bodyRows()).toHaveLength(5)
  })

  it("starts the other tab at page 1", async () => {
    rowsOf = many(25)
    const { user, router } = await mk("/skills?page=2")
    await user.click(screen.getByRole("tab", { name: "Catalog" }))
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ tab: "catalog" })
    expect(listLink("skills")).toEqual({ name: "skills", query: { tab: "catalog" } })
  })

  it("wraps the name cell and gives the status and the buttons room", async () => {
    const { container } = await mk()
    const row = rowOf("release-notes")
    const nameCell = within(row).getByText("/release-notes").closest("td")!
    expect(nameCell.classList.contains("whitespace-normal")).toBe(true)
    const statusCell = within(row).getByText("update available").closest("td")!
    expect(statusCell.classList.contains("whitespace-normal")).toBe(true)
    expect(statusCell.classList.contains("overflow-hidden")).toBe(false)
    const cols = container.querySelectorAll("colgroup col")
    expect((cols[cols.length - 1] as HTMLElement).style.width).toBe("16rem")
  })

  it("keeps the name and the description in one box of the cell", async () => {
    await mk()
    const row = rowOf("release-notes")
    const name = within(row).getByText("/release-notes")
    const nameCell = name.closest("td")!
    // A phone card lays out the children of a cell in one row. One child keeps the name wide.
    expect(nameCell.children).toHaveLength(1)
    expect(nameCell.children[0].contains(name)).toBe(true)
  })
})

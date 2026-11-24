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
import { confirm, isSecure, toast, ui } from "../stores/ui"
import ResultNote from "../components/ResultNote.vue"
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

async function mk(path = "/skills") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/skills", name: "skills", component: SkillsPage },
      { path: "/skills/:name", name: "skill", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(SkillsPage, { global: { plugins: [router], stubs: { LiveHeader: true } } })
  await flushPromises()
  return Object.assign(w, { router })
}
const btns = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)
const note = (w: any) => w.findComponent(ResultNote).props("note")
const end = async (id: string, t: object) => {
  h.follow.get(id)!(task(t))
  await flushPromises()
}

beforeEach(() => {
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
  it("shows the error text of a failed task in a result note", async () => {
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[0].trigger("click")
    await flushPromises()
    await end("t1", {
      state: "error",
      subject: "changelog-check",
      text: "The guard found a problem.",
    })
    expect(note(w)).toMatchObject({
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
    const w = await mk()
    await btns(w, "Uninstall")[1].trigger("click")
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
    expect(note(w)).toBeNull()
  })

  it("offers Install anyway after a caution refusal and posts force", async () => {
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[0].trigger("click")
    await flushPromises()
    await end("t1", {
      state: "error",
      code: "need_force",
      subject: "changelog-check",
      text: "caution: 1 finding",
    })
    expect(note(w)).toMatchObject({ kind: "warn", text: "caution: 1 finding" })
    await btns(w, "Install anyway…")[0].trigger("click")
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
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[0].trigger("click")
    await flushPromises()
    await end("t1", { state: "error", code: "need_force", subject: "release-notes" })
    await btns(w, "Install anyway…")[0].trigger("click")
    await flushPromises()
    expect(vi.mocked(confirm).mock.calls[0][0].body).toBe(
      "The skill guard rated it caution. A confirm card follows in ⏳ Waiting. It replaces the installed /release-notes from src.",
    )
  })

  it("opens the terminal panel after a dangerous refusal", async () => {
    const w = await mk()
    await btns(w, "Update")[0].trigger("click")
    await flushPromises()
    await end("t1", {
      state: "error",
      code: "need_override",
      subject: "release-notes",
      text: "dangerous: 2 findings",
    })
    expect(w.text()).toContain("tether skills update release-notes")
    expect(w.text()).toContain("The terminal asks you to type the name of the skill.")
    expect(w.text()).toContain("dangerous: 2 findings")
  })

  it("tells the owner to answer the card while the task waits", async () => {
    const w = await mk()
    await btns(w, "Update")[0].trigger("click")
    await flushPromises()
    await end("t1", { waiting: true, subject: "release-notes" })
    expect(w.text()).toContain("Answer the card in ⏳ Waiting.")
    await btns(w, "Open Waiting")[0].trigger("click")
    expect(ui.trayOpen).toBe(true)
  })

  it("disables each skill action while a task runs and marks the running skill", async () => {
    const w = await mk()
    await btns(w, "Update")[0].trigger("click")
    await flushPromises()
    for (const t of ["Update", "Reset", "Uninstall", "Check for updates"])
      for (const b of btns(w, t)) expect(b.attributes("disabled"), t).toBeDefined()
    const row = w.findAll("tr").find((r: any) => r.text().includes("release-notes"))!
    expect(row.text()).toContain("Running…")
    await end("t1", { state: "done", subject: "release-notes", text: "ok" })
    expect(btns(w, "Update")[0].attributes("disabled")).toBeUndefined()
    expect(toast).toHaveBeenCalledWith("Updated /release-notes. It loads in new sessions.", "ok")
  })

  it("shows the lost text when the gateway restarted", async () => {
    const w = await mk()
    await btns(w, "Update")[0].trigger("click")
    await flushPromises()
    await end("t1", { state: "lost", subject: "release-notes" })
    expect(note(w)).toMatchObject({
      kind: "error",
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows the newest skills task again on return", async () => {
    tasks = [task({ id: "t9", state: "done", subject: "release-notes", text: "Updated." })]
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks?kind=skills")
    expect(note(w)).toMatchObject({ title: "/release-notes", kind: "info", text: "Updated." })
  })

  it("follows a skills task that still runs on return", async () => {
    tasks = [task({ id: "t8", subject: "pr-review" })]
    const w = await mk()
    expect(h.follow.has("t8")).toBe(true)
    expect(btns(w, "Uninstall")[0].attributes("disabled")).toBeDefined()
  })

  it("stops following a task when the page unmounts", async () => {
    tasks = [task({ id: "t8", subject: "pr-review" })]
    const w = await mk()
    expect(h.follow.has("t8")).toBe(true)
    w.unmount()
    expect(h.follow.has("t8")).toBe(false)
  })

  it("keeps the list and shows a toast when a live reload fails", async () => {
    const w = await mk()
    expect(w.text()).toContain("pr-review")
    vi.mocked(api).mockImplementation(async () => {
      throw new Error("boom")
    })
    await h.live!.load()
    await flushPromises()
    expect(w.text()).toContain("pr-review")
    expect(toast).toHaveBeenCalledWith("boom", "error")
  })

  it("switches to the catalog with the tabs", async () => {
    const w = await mk()
    const tab = w.findAll('[role="tab"]').find((t: any) => t.text() === "Catalog")!
    await tab.trigger("click")
    await flushPromises()
    expect(w.router.currentRoute.value.query.tab).toBe("catalog")
    expect(api).toHaveBeenCalledWith("GET", "/api/skills/catalog")
  })

  it("shows the empty state with Open the catalog", async () => {
    rowsOf = []
    const w = await mk()
    expect(w.text()).toContain("No skills installed.")
    await btns(w, "Open the catalog")[0].trigger("click")
    await flushPromises()
    expect(w.router.currentRoute.value.query.tab).toBe("catalog")
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
    const w = await mk("/skills?tab=catalog")
    expect(w.findAll("tbody tr")).toHaveLength(20)
    expect(w.text()).toContain("Page 1 of 2")
    await btns(w, "Next ▶")[0].trigger("click")
    expect(w.findAll("tbody tr")).toHaveLength(5)
    await w.find('select[aria-label="Trust"]').setValue("local")
    expect(w.findAll("tbody tr").map((r: any) => r.find("b").text())).toEqual([
      "/s00",
      "/s01",
      "/s02",
    ])
    await w.find('select[aria-label="Installed"]').setValue("no")
    expect(w.findAll("tbody tr").map((r: any) => r.find("b").text())).toEqual(["/s01"])
  })

  it("searches the installed list", async () => {
    const w = await mk()
    await w.find('input[aria-label="Search the installed skills"]').setValue("review")
    expect(w.text()).toContain("pr-review")
    expect(w.text()).not.toContain("release-notes")
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
    expect(w.text()).toContain("tether skills install repo-sync")
    expect(w.text()).toContain("The terminal asks you to type the name of the skill.")
  })

  it("copies the terminal command of a dangerous skill", async () => {
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    const w = await mk("/skills?tab=catalog")
    await btns(w, "Install")[1].trigger("click")
    await flushPromises()
    await btns(w, "Copy command")[0].trigger("click")
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith("tether skills install repo-sync")
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

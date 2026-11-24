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
import { confirm, toast } from "../stores/ui"
import { api, runCommand } from "../api/client"
import { session } from "../stores/session"
import SessionsPage from "./SessionsPage.vue"
import StateBlock from "../components/StateBlock.vue"
import LiveHeader from "../components/LiveHeader.vue"
import { fullTime, shortTime } from "../lib/time"

const Blank = { render: () => null }
const row = (o: object) => ({
  id: "",
  title: "",
  kind: "chat",
  source: "dashboard",
  source_label: "Dashboard",
  last_active: "2026-11-20T09:00:00Z",
  started: "2026-11-19T09:00:00Z",
  messages: 4,
  cost_usd: 0.0123,
  archived: false,
  read_only: false,
  children: [],
  ...o,
})
const page = {
  total: 4,
  rows: [
    row({ id: "s1", title: "Release notes" }),
    row({
      id: "s2",
      title: "Photos",
      source: "telegram",
      source_label: "Telegram › DM",
      read_only: true,
      children: [row({ id: "c1", title: "subagent run", kind: "subagent", read_only: true })],
    }),
    row({ id: "s3", title: "Old chat" }),
    { ...row({ id: "s4", title: "Archived chat" }), archived: true },
  ],
}

const hit = (o: object) => ({
  session_id: "s1",
  title: "Release notes",
  snippet: "the [flaky] test in [cron]",
  message_id: 11,
  date: "2026-11-20T09:00:00Z",
  source: "dashboard",
  kind: "chat",
  ...o,
})
const hits20 = Array.from({ length: 20 }, (_, i) => hit({ message_id: 100 + i }))
function deferred<T>() {
  let resolve!: (v: T) => void
  const p = new Promise<T>((r) => (resolve = r))
  return { p, resolve }
}

async function mk(path = "/sessions") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/sessions", name: "sessions", component: SessionsPage },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(SessionsPage, { global: { plugins: [router], stubs: { teleport: true } } })
  await flushPromises()
  return { w, router }
}
const btn = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)
const menu = async (w: any, rowTitle: string, label: string) => {
  const tr = w.findAll("tr").find((r: any) => r.text().includes(rowTitle))!
  await tr.find("button[aria-label='More actions']").trigger("click")
  await tr
    .findAll(".menu-item")
    .find((b: any) => b.text() === label)!
    .trigger("click")
  await flushPromises()
}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(page)
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "✅ Done.", buttons: [], edit: false })
  vi.mocked(toast).mockReset()
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  session.id = "s1"
})

describe("SessionsPage", () => {
  it("loads the first page with all filter keys", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=50&offset=0",
    )
    expect(w.text()).toContain("Release notes")
    expect(w.text()).toContain("Telegram › DM")
    expect(w.find(".pager").text()).toContain("1–4 of 4")
  })

  it("folds the child rows behind a toggle that stays open across a reload", async () => {
    const { w } = await mk()
    expect(w.find(".cellrow.is-child").exists()).toBe(false)
    const t = w.find("button.kids-toggle")
    expect(t.text()).toBe("▸ 1 child session")
    expect(t.attributes("aria-expanded")).toBe("false")
    await t.trigger("click")
    const child = w.find(".cellrow.is-child")
    expect(child.text()).toContain("↳")
    expect(child.find(".kid-label").text()).toBe("subagent run")
    expect(child.find(".chip").text()).toBe("subagent")
    expect(w.find("button.kids-toggle").text()).toBe("▾ 1 child session")
    expect(w.find("button.kids-toggle").attributes("aria-expanded")).toBe("true")
    w.findComponent(LiveHeader).vm.$emit("refresh")
    await flushPromises()
    expect(w.find(".cellrow.is-child").exists()).toBe(true)
    await w.find("button.kids-toggle").trigger("click")
    expect(w.find(".cellrow.is-child").exists()).toBe(false)
  })

  it("shows New session in the muted color for an untitled row", async () => {
    vi.mocked(api).mockResolvedValue({ total: 1, rows: [row({ id: "s9", title: "" })] })
    const { w } = await mk()
    const a = w.find("a[href='/sessions/s9']")
    expect(a.text()).toBe("New session")
    expect(a.classes()).toContain("c-3")
  })

  it("names a child once when its title equals its kind", async () => {
    vi.mocked(api).mockResolvedValue({
      total: 1,
      rows: [
        row({
          id: "p1",
          title: "Plan",
          children: [
            row({ id: "r1", title: "review", kind: "review", read_only: true }),
            row({ id: "k1", title: "subagent", kind: "subagent", read_only: true }),
          ],
        }),
      ],
    })
    const { w } = await mk()
    expect(w.find("button.kids-toggle").text()).toBe("▸ 2 child sessions")
    await w.find("button.kids-toggle").trigger("click")
    const kids = w.findAll(".cellrow.is-child")
    expect(kids.map((k) => k.find(".kid-label").text())).toEqual(["Background review", "subagent"])
    expect(kids.map((k) => k.find(".chip").exists())).toEqual([false, false])
  })

  it("gives the toggle row no dates, no cost, and no buttons", async () => {
    const { w } = await mk()
    const tr = w.find("button.kids-toggle").element.closest("tr")!
    const buttons = Array.from(tr.querySelectorAll("button")).map((b) => b.textContent?.trim())
    expect(buttons).toEqual(["▸ 1 child session"])
    expect(tr.textContent).not.toContain("—")
    expect(tr.textContent).not.toContain("$")
  })

  it("opens the active session and resumes another one", async () => {
    const { w, router } = await mk()
    expect(btn(w, "Open")).toHaveLength(1)
    await btn(w, "Resume")[0].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/resume s3")
    expect(router.currentRoute.value.name).toBe("chat")
  })

  it("forks a read-only session into the dashboard", async () => {
    const { w } = await mk()
    await menu(w, "Photos", "Fork into dashboard")
    expect(runCommand).toHaveBeenCalledWith("/fork from s2")
  })

  it("shows the error state and retries", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const { w } = await mk()
    expect(w.findComponent(StateBlock).props("state")).toBe("error")
    w.findComponent(StateBlock).vm.$emit("action")
    await flushPromises()
    // The failed load made one call. The reload makes two: the list and the other total.
    expect(api).toHaveBeenCalledTimes(3)
    expect(w.text()).toContain("Release notes")
  })

  it("keeps the list when the request for the other total fails", async () => {
    vi.mocked(api).mockResolvedValueOnce(page).mockRejectedValueOnce(new Error("HTTP 500"))
    const { w } = await mk()
    expect(
      w.findComponent(StateBlock).exists() && w.findComponent(StateBlock).props("state"),
    ).not.toBe("error")
    expect(w.text()).toContain("Release notes")
    expect(w.find(".seg").text()).toContain("Active · 4")
  })

  it("shows a Busy refusal as a warn toast with the Stop button", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/archive` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    const { w } = await mk()
    await menu(w, "Old chat", "Archive")
    expect(runCommand).toHaveBeenCalledWith("/archive s3")
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining("⏳ **Busy**"),
      "warn",
      expect.objectContaining({ actions: [{ label: "⏹ Stop the task", data: "/stop" }] }),
    )
  })

  it("archives by id with an Undo that unarchives", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: '🗄 **Archived "Old chat"**\nThe session list does not show it now.',
      buttons: [[{ text: "↩️ Undo", data: "/unarchive s3" }]],
      edit: false,
    })
    const { w } = await mk()
    vi.mocked(api).mockClear()
    await menu(w, "Old chat", "Archive")
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining("Archived"),
      "ok",
      expect.objectContaining({ actions: [{ label: "↩️ Undo", data: "/unarchive s3" }] }),
    )
    // The page loads again after the command.
    expect(api).toHaveBeenCalled()
  })

  it("offers Unarchive and Resume on an archived row", async () => {
    const { w } = await mk()
    const tr = w.findAll("tr").find((r) => r.text().includes("Archived chat"))!
    expect(tr.findAll("button").map((b) => b.text())).toEqual(
      expect.arrayContaining(["Unarchive", "Resume"]),
    )
    await tr
      .findAll("button")
      .find((b) => b.text() === "Unarchive")!
      .trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/unarchive s4")
  })

  it("does not go to Chat when the resume is refused", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/resume` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    const { w, router } = await mk()
    await btn(w, "Resume")[0].trigger("click")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("sessions")
  })

  it("goes to Chat after a fork from the list", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "🍴 **Copied to a new dashboard session**",
      buttons: [],
      edit: false,
    })
    const { w, router } = await mk()
    await menu(w, "Photos", "Fork into dashboard")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
  })

  it("renames inline with Save, Enter, Esc, and blur", async () => {
    const { w } = await mk()
    await menu(w, "Release notes", "Rename")
    const input = w.find("input[aria-label='New title']")
    expect(input.attributes("autofocus")).toBeDefined()
    await input.setValue("Release notes v2")
    await btn(w, "Save")[0].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/rename s1 Release notes v2")
    await menu(w, "Release notes", "Rename")
    await w.find("input[aria-label='New title']").trigger("keydown", { key: "Escape" })
    expect(w.find("input[aria-label='New title']").exists()).toBe(false)
    await menu(w, "Release notes", "Rename")
    await w.find("input[aria-label='New title']").trigger("blur")
    expect(w.find("input[aria-label='New title']").exists()).toBe(false)
  })

  it("links each title to its transcript and marks the active session", async () => {
    const { w } = await mk()
    expect(w.find("a[href='/sessions/s1']").text()).toBe("Release notes")
    const active = w.findAll("tr").find((r) => r.text().includes("Release notes"))!
    expect(active.find(".chip").text()).toBe("active")
  })

  it("hides the Kind column for Chats, shows it for all kinds, and shows the counts", async () => {
    const { w } = await mk()
    expect(w.findAll("th").map((t) => t.text())).toEqual([
      "Session",
      "Source",
      "Started",
      "Last active",
      "Messages",
      "Cost",
      "",
    ])
    expect(w.find(".seg").text()).toContain("Active · 4")
    expect(w.find(".seg").text()).toContain("Archived · 4")
    expect(w.find(".pager").text()).toContain("Page 1 of 1")
    const { w: all } = await mk("/sessions?kind=")
    expect(all.findAll("th").map((t) => t.text())).toContain("Kind")
  })

  it("gives a read-only row one View button and the fork and export in the menu", async () => {
    const { w } = await mk()
    const tr = w.findAll("tr").find((r) => r.text().includes("Photos"))!
    const plain = tr.findAll("button").filter((b) => b.attributes("aria-label") !== "More actions")
    expect(plain.map((b) => b.text())).toEqual(["View"])
    await tr.find("button[aria-label='More actions']").trigger("click")
    expect(tr.findAll(".menu-item").map((b) => b.text())).toEqual(["Fork into dashboard", "Export"])
  })

  it("cuts a long title with the full title in the tooltip", async () => {
    const { w } = await mk()
    const a = w.find("a[href='/sessions/s1']")
    expect(a.classes()).toContain("cell-title")
    expect(a.attributes("title")).toBe("Release notes")
  })

  it("styles the filter selects as inputs", async () => {
    const { w } = await mk()
    expect(w.find("select[aria-label='Source']").classes()).toContain("input")
    expect(w.find("select[aria-label='Kind']").classes()).toContain("input")
  })

  it("shows short dates on one line with the full date in the tooltip", async () => {
    const { w } = await mk()
    const tds = w.findAll("tbody tr")[0].findAll("td")
    const started = tds[2].find("span")
    expect(started.text()).toBe(shortTime("2026-11-19T09:00:00Z"))
    expect(started.attributes("title")).toBe(fullTime("2026-11-19T09:00:00Z"))
    expect(started.attributes("style")).toContain("white-space: nowrap")
    expect(tds[3].find("span").text()).toBe(shortTime("2026-11-20T09:00:00Z"))
  })

  it("isolates the source label and keeps its full text in the tooltip", async () => {
    const { w } = await mk()
    const tr = w.findAll("tr").find((r) => r.text().includes("Photos"))!
    const bdi = tr.find("bdi")
    expect(bdi.text()).toBe("Telegram › DM")
    expect(bdi.attributes("title")).toBe("Telegram › DM")
    expect(bdi.classes()).toContain("cell-title")
  })

  it("asks the confirm of /delete before it deletes", async () => {
    vi.mocked(runCommand)
      .mockResolvedValueOnce({
        request_id: "r",
        text: "Delete “Old chat”?\nThe messages go too.",
        buttons: [[{ text: "🗑 Delete", data: "/delete s3 confirm" }]],
        edit: false,
      })
      .mockResolvedValueOnce({ request_id: "r2", text: "🗑 Deleted.", buttons: [], edit: false })
    const { w } = await mk()
    await menu(w, "Old chat", "Delete…")
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ confirmLabel: "🗑 Delete" }))
    expect(runCommand).toHaveBeenLastCalledWith("/delete s3 confirm")
  })

  it("revokes the export URL after a delay, not at once", async () => {
    vi.useFakeTimers()
    const createURL = URL.createObjectURL
    const revokeURL = URL.revokeObjectURL
    try {
      const revoke = vi.fn()
      URL.createObjectURL = vi.fn(() => "blob:x")
      URL.revokeObjectURL = revoke
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, status: 200, blob: async () => new Blob(["x"]) })),
      )
      const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
      const { w } = await mk()
      const tr = w.findAll("tr").find((r: any) => r.text().includes("Photos"))!
      await tr.find("button[aria-label='More actions']").trigger("click")
      await tr
        .findAll(".menu-item")
        .find((b: any) => b.text() === "Export")!
        .trigger("click")
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      click.mockRestore()
      w.unmount()
    } finally {
      URL.createObjectURL = createURL
      URL.revokeObjectURL = revokeURL
      vi.unstubAllGlobals()
      vi.useRealTimers()
    }
  })

  it("pages the full-text hits with the filters and hides the table", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/search")
          ? p.includes("offset=0")
            ? hits20
            : [hit({ message_id: 7 })]
          : page) as any,
    )
    vi.useFakeTimers()
    try {
      const { w } = await mk()
      await w.find("input[type='checkbox']").setValue(true)
      await w.find("input[type='search']").setValue("flaky")
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(api).toHaveBeenCalledWith(
        "GET",
        "/api/search?q=flaky&limit=20&offset=0&source=&kind=chat&archived=0",
      )
      expect(w.find("table").exists()).toBe(false)
      expect(w.text()).toContain("20 hits")
      await btn(w, "Load more")[0].trigger("click")
      await flushPromises()
      expect(api).toHaveBeenCalledWith(
        "GET",
        "/api/search?q=flaky&limit=20&offset=20&source=&kind=chat&archived=0",
      )
      expect(w.text()).toContain("21 hits")
      expect(btn(w, "Load more")).toHaveLength(0)
    } finally {
      vi.useRealTimers()
    }
  })

  it("renders the match markers as mark elements", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/search") ? [hit({ snippet: "<b>x</b> [flaky] test" })] : page) as any,
    )
    const { w } = await mk("/sessions?q=flaky")
    await w.find("input[type='checkbox']").setValue(true)
    await flushPromises()
    const marks = w.findAll(".hits mark").map((m) => m.text())
    expect(marks).toEqual(["flaky"])
    // The snippet is text, so a tag in it shows as typed.
    expect(w.find(".hits").text()).toContain("<b>x</b>")
  })

  it("puts a space between the title and the snippet of a hit", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p.startsWith("/api/search") ? [hit({})] : page) as any,
    )
    const { w } = await mk("/sessions?q=flaky")
    await w.find("input[type='checkbox']").setValue(true)
    await flushPromises()
    expect(w.find(".hits .li .grow").text().replace(/\s+/g, " ")).toBe(
      "Release notes the flaky test in cron",
    )
  })

  it("shows a search error under the box and keeps the list", async () => {
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (p.startsWith("/api/search")) throw new TypeError("Failed to fetch")
      return page as any
    })
    const { w } = await mk("/sessions?q=flaky")
    await w.find("input[type='checkbox']").setValue(true)
    await flushPromises()
    expect(w.find(".search-err").text()).toBe(
      "Search failed: Cannot reach the gateway. Check the network, then try again.",
    )
    expect(
      w.findComponent(StateBlock).exists() && w.findComponent(StateBlock).props("state"),
    ).not.toBe("error")
  })

  it("reads the filters from the route query and writes them back", async () => {
    const { w, router } = await mk("/sessions?q=flaky&src=telegram&kind=&arch=1&off=50")
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=telegram&kind=&search=flaky&archived=1&limit=50&offset=50",
    )
    await w.find("select[aria-label='Source']").setValue("cli")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ q: "flaky", src: "cli", kind: "", arch: "1" })
  })

  it("sets Kind to all when the source is Cron", async () => {
    const { w } = await mk()
    await w.find("select[aria-label='Source']").setValue("cron")
    await flushPromises()
    expect((w.find("select[aria-label='Kind']").element as HTMLSelectElement).value).toBe("")
    expect(api).toHaveBeenCalledWith(
      "GET",
      expect.stringContaining("/api/sessions?source=cron&kind=&"),
    )
  })

  it("drops a reply that comes after a newer one", async () => {
    const first = deferred<unknown>()
    let n = 0
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (!p.includes("limit=50")) return page as any
      n++
      if (n === 1) return first.p as any
      return { total: 1, rows: [row({ id: "t1", title: "From Telegram" })] } as any
    })
    const { w } = await mk()
    await w.find("select[aria-label='Source']").setValue("telegram")
    await flushPromises()
    first.resolve(page)
    await flushPromises()
    expect(w.text()).toContain("From Telegram")
    expect(w.text()).not.toContain("Old chat")
  })

  it("offers Clear filters when the filters match nothing", async () => {
    vi.mocked(api).mockResolvedValue({ total: 0, rows: [] })
    const { w, router } = await mk("/sessions?src=cli")
    expect(w.text()).toContain("No sessions match the filters.")
    w.findAllComponents(StateBlock)
      .find((s) => s.props("action") === "Clear filters")!
      .vm.$emit("action")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(api).toHaveBeenCalledWith(
      "GET",
      expect.stringContaining(
        "/api/sessions?source=&kind=chat&search=&archived=0&limit=50&offset=0",
      ),
    )
  })
})

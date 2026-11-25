import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within, fireEvent, cleanup, waitFor } from "@testing-library/vue"
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
const box = vi.hoisted(() => ({ width: 2000 }))
vi.mock("@vueuse/core", async (importOriginal) => {
  const { ref } = await import("vue")
  return {
    ...(await importOriginal<typeof import("@vueuse/core")>()),
    useElementSize: () => ({ width: ref(box.width), height: ref(0), stop: () => {} }),
  }
})
import { confirm, listLink, toast } from "../stores/ui"
import { api, runCommand } from "../api/client"
import { session } from "../stores/session"
import SessionsPage from "./SessionsPage.vue"
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
  const r = render(SessionsPage, { global: { plugins: [router] } })
  await flushPromises()
  return { ...r, router }
}
const rowOf = (text: string) =>
  screen.getAllByRole("row").find((r) => r.textContent!.includes(text))!
async function menu(user: ReturnType<typeof userEvent.setup>, rowText: string, label: string) {
  await user.click(within(rowOf(rowText)).getByRole("button", { name: "More actions" }))
  await user.click(await screen.findByRole("menuitem", { name: label }))
  await flushPromises()
}
const names = (els: HTMLElement[]) => els.map((b) => b.textContent!.trim())
const prevPage = () =>
  within(screen.getByRole("navigation", { name: "Sessions pages" })).getByRole("button", {
    name: "Previous Page",
  }) as HTMLButtonElement
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
// The header texts of the columns that show. A hidden optional column has the class hidden.
const shownHeads = () =>
  screen
    .getAllByRole("columnheader")
    .filter((h) => !h.classList.contains("hidden"))
    .map((h) => h.textContent!.trim())
const actCol = (c: Element) => {
  const cols = c.querySelectorAll("colgroup col")
  return (cols[cols.length - 1] as HTMLElement).style.width
}

beforeEach(() => {
  box.width = 2000
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

const kidRows = () =>
  screen.queryAllByRole("row").filter((r) => r.querySelector("svg.lucide-corner-down-right"))

describe("SessionsPage", () => {
  it("loads the first page with all filter keys", async () => {
    await mk()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=0",
    )
    expect(screen.getByText("Release notes")).toBeTruthy()
    expect(screen.getByText("Telegram › DM")).toBeTruthy()
    // The pager hides at 4 rows.
    expect(screen.queryByRole("navigation", { name: "Sessions pages" })).toBeNull()
  })

  it("folds the child rows behind a toggle that stays open across a reload", async () => {
    const user = setup()
    await mk()
    expect(screen.queryByText("subagent run")).toBeNull()
    const t = screen.getByRole("button", { name: "1 child session" })
    expect(t.getAttribute("aria-expanded")).toBe("false")
    expect(t.querySelector("svg.lucide-chevron-right")).not.toBeNull()
    await user.click(t)
    const child = rowOf("subagent run")
    expect(child.querySelector("svg.lucide-corner-down-right")).not.toBeNull()
    expect(within(child).getByText("subagent").getAttribute("data-slot")).toBe("badge")
    const t2 = screen.getByRole("button", { name: "1 child session" })
    expect(t2.getAttribute("aria-expanded")).toBe("true")
    expect(t2.querySelector("svg.lucide-chevron-down")).not.toBeNull()
    await user.click(screen.getByRole("button", { name: "Refresh" }))
    await flushPromises()
    expect(screen.getByText("subagent run")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "1 child session" }))
    expect(screen.queryByText("subagent run")).toBeNull()
  })

  it("shows New session in the muted color for an untitled row", async () => {
    vi.mocked(api).mockResolvedValue({ total: 1, rows: [row({ id: "s9", title: "" })] })
    await mk()
    const a = screen.getByRole("link", { name: "New session" })
    expect(a.getAttribute("href")).toBe("/sessions/s9")
    expect(a.classList.contains("text-muted-foreground")).toBe(true)
  })

  it("names a child once when its title equals its kind", async () => {
    const user = setup()
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
    await mk()
    await user.click(screen.getByRole("button", { name: "2 child sessions" }))
    const kids = kidRows()
    expect(kids).toHaveLength(2)
    expect(within(kids[0]).getByText("Background review")).toBeTruthy()
    expect(within(kids[1]).getByText("subagent")).toBeTruthy()
    for (const k of kids) expect(k.querySelector('[data-slot="badge"]')).toBeNull()
  })

  it("gives the toggle row no dates, no cost, and no buttons", async () => {
    await mk()
    const toggleRow = rowOf("1 child session")
    expect(names(within(toggleRow).getAllByRole("button"))).toEqual(["1 child session"])
    expect(toggleRow.textContent).not.toContain("—")
    expect(toggleRow.textContent).not.toContain("$")
  })

  it("opens the active session and resumes another one", async () => {
    const user = setup()
    const { router } = await mk()
    expect(screen.getAllByRole("button", { name: "Open" })).toHaveLength(1)
    await user.click(screen.getAllByRole("button", { name: "Resume" })[0])
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/resume s3")
    expect(router.currentRoute.value.name).toBe("chat")
  })

  it("forks a read-only session into the dashboard", async () => {
    const user = setup()
    await mk()
    await menu(user, "Photos", "Fork into dashboard")
    expect(runCommand).toHaveBeenCalledWith("/fork from s2")
  })

  it("shows the error state and retries", async () => {
    const user = setup()
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    await mk()
    expect(screen.getByRole("alert")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Retry" }))
    await flushPromises()
    // The failed load made one call. The reload makes two: the list and the other total.
    expect(api).toHaveBeenCalledTimes(3)
    expect(screen.getByText("Release notes")).toBeTruthy()
  })

  it("keeps the list when the request for the other total fails", async () => {
    vi.mocked(api).mockResolvedValueOnce(page).mockRejectedValueOnce(new Error("HTTP 500"))
    await mk()
    expect(screen.queryByRole("alert")).toBeNull()
    expect(screen.getByText("Release notes")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Active · 4" })).toBeTruthy()
  })

  it("shows a Busy refusal as a warn toast with the Stop button", async () => {
    const user = setup()
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/archive` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    await mk()
    await menu(user, "Old chat", "Archive")
    expect(runCommand).toHaveBeenCalledWith("/archive s3")
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining("⏳ **Busy**"),
      "warn",
      expect.objectContaining({ actions: [{ label: "⏹ Stop the task", data: "/stop" }] }),
    )
  })

  it("archives by id with an Undo that unarchives", async () => {
    const user = setup()
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: '🗄 **Archived "Old chat"**\nThe session list does not show it now.',
      buttons: [[{ text: "↩️ Undo", data: "/unarchive s3" }]],
      edit: false,
    })
    await mk()
    vi.mocked(api).mockClear()
    await menu(user, "Old chat", "Archive")
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining("Archived"),
      "ok",
      expect.objectContaining({ actions: [{ label: "↩️ Undo", data: "/unarchive s3" }] }),
    )
    // The page loads again after the command.
    expect(api).toHaveBeenCalled()
  })

  it("offers Unarchive and Resume on an archived row", async () => {
    const user = setup()
    await mk()
    const tr = rowOf("Archived chat")
    expect(names(within(tr).getAllByRole("button"))).toEqual(
      expect.arrayContaining(["Unarchive", "Resume"]),
    )
    await user.click(within(tr).getByRole("button", { name: "Unarchive" }))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/unarchive s4")
  })

  it("does not go to Chat when the resume is refused", async () => {
    const user = setup()
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "⏳ **Busy**\n`/resume` cannot run during a task. Wait for the answer, or stop the task.",
      buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
      edit: false,
    })
    const { router } = await mk()
    await user.click(screen.getAllByRole("button", { name: "Resume" })[0])
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("sessions")
  })

  it("goes to Chat after a fork from the list", async () => {
    const user = setup()
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "🍴 **Copied to a new dashboard session**",
      buttons: [],
      edit: false,
    })
    const { router } = await mk()
    await menu(user, "Photos", "Fork into dashboard")
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("chat")
  })

  it("renames inline with Save, Enter, Esc, and blur", async () => {
    const user = setup()
    await mk()
    await menu(user, "Release notes", "Rename")
    const input = screen.getByRole("textbox", { name: "New title" })
    expect(input.hasAttribute("autofocus")).toBe(true)
    await waitFor(() => expect(document.activeElement).toBe(input))
    await user.clear(input)
    await user.type(input, "Release notes v2")
    await user.click(screen.getByRole("button", { name: "Save" }))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/rename s1 Release notes v2")
    await menu(user, "Release notes", "Rename")
    // The menu gives the focus back to its trigger after a tick, and the field then takes it
    // again. Wait until the focus stays on the field, so that Escape goes to the field.
    await new Promise((r) => setTimeout(r, 20))
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "New title" })),
    )
    await user.keyboard("{Escape}")
    expect(screen.queryByRole("textbox", { name: "New title" })).toBeNull()
    await menu(user, "Release notes", "Rename")
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "New title" })),
    )
    await fireEvent.blur(screen.getByRole("textbox", { name: "New title" }))
    expect(screen.queryByRole("textbox", { name: "New title" })).toBeNull()
  })

  it("links each title to its transcript and marks the active session", async () => {
    await mk()
    expect(screen.getByRole("link", { name: "Release notes" }).getAttribute("href")).toBe(
      "/sessions/s1",
    )
    expect(within(rowOf("Release notes")).getByText("active").getAttribute("data-slot")).toBe(
      "badge",
    )
  })

  it("hides the Kind column for Chats, shows it for all kinds, and shows the counts", async () => {
    const first = await mk()
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent!.trim())).toEqual([
      "Session",
      "Source",
      "Started",
      "Last active",
      "Messages",
      "Cost",
      "",
    ])
    expect(screen.getByRole("button", { name: "Active · 4" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "Archived · 4" })).toBeTruthy()
    first.unmount()
    cleanup()
    await mk("/sessions?kind=")
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent!.trim())).toContain("Kind")
  })

  it("gives a read-only row one View button and the fork and export in the menu", async () => {
    const user = setup()
    await mk()
    const tr = rowOf("Photos")
    const plain = within(tr)
      .getAllByRole("button")
      .filter((b) => b.getAttribute("aria-label") !== "More actions")
    expect(names(plain)).toEqual(["View"])
    await user.click(within(tr).getByRole("button", { name: "More actions" }))
    expect(names(await screen.findAllByRole("menuitem"))).toEqual(["Fork into dashboard", "Export"])
  })

  it("cuts a long title with the full title in the tooltip", async () => {
    await mk()
    const a = screen.getByRole("link", { name: "Release notes" })
    expect(a.classList.contains("block")).toBe(true)
    expect(a.classList.contains("truncate")).toBe(true)
    expect(a.getAttribute("title")).toBe("Release notes")
    const wrap = a.parentElement!
    expect(wrap.classList.contains("flex")).toBe(true)
    expect(wrap.classList.contains("min-w-0")).toBe(true)
    expect(within(wrap).getByText("active").getAttribute("data-slot")).toBe("badge")
  })

  it("styles the filter selects as inputs", async () => {
    await mk()
    for (const name of ["Source", "Kind"])
      expect(screen.getByRole("combobox", { name }).getAttribute("data-slot")).toBe("native-select")
  })

  it("shows short dates on one line with the full date in the tooltip", async () => {
    await mk()
    const row1 = screen.getAllByRole("row")[1]
    const started = within(row1).getByTitle(fullTime("2026-11-19T09:00:00Z"))
    expect(started.textContent).toBe(shortTime("2026-11-19T09:00:00Z"))
    expect(started.classList.contains("whitespace-nowrap")).toBe(true)
    expect(within(row1).getByTitle(fullTime("2026-11-20T09:00:00Z")).textContent).toBe(
      shortTime("2026-11-20T09:00:00Z"),
    )
  })

  it("isolates the source label and keeps its full text in the tooltip", async () => {
    await mk()
    const bdi = within(rowOf("Photos")).getByText("Telegram › DM")
    expect(bdi.tagName).toBe("BDI")
    expect(bdi.getAttribute("title")).toBe("Telegram › DM")
    expect(bdi.classList.contains("block")).toBe(true)
    expect(bdi.classList.contains("truncate")).toBe(true)
  })

  it("asks the confirm of /delete before it deletes", async () => {
    const user = setup()
    vi.mocked(runCommand)
      .mockResolvedValueOnce({
        request_id: "r",
        text: "Delete “Old chat”?\nThe messages go too.",
        buttons: [[{ text: "🗑 Delete", data: "/delete s3 confirm" }]],
        edit: false,
      })
      .mockResolvedValueOnce({ request_id: "r2", text: "🗑 Deleted.", buttons: [], edit: false })
    await mk()
    await menu(user, "Old chat", "Delete…")
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ confirmLabel: "🗑 Delete" }))
    expect(runCommand).toHaveBeenLastCalledWith("/delete s3 confirm")
  })

  it("revokes the export URL after a delay, not at once", async () => {
    vi.useFakeTimers()
    const createURL = URL.createObjectURL
    const revokeURL = URL.revokeObjectURL
    try {
      const user = userEvent.setup({
        advanceTimers: vi.advanceTimersByTime,
        pointerEventsCheck: 0,
      })
      const revoke = vi.fn()
      URL.createObjectURL = vi.fn(() => "blob:x")
      URL.revokeObjectURL = revoke
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, status: 200, blob: async () => new Blob(["x"]) })),
      )
      const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
      await mk()
      await menu(user, "Photos", "Export")
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      click.mockRestore()
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
      const user = userEvent.setup({
        advanceTimers: vi.advanceTimersByTime,
        pointerEventsCheck: 0,
      })
      await mk()
      await user.click(screen.getByRole("checkbox", { name: "Full text" }))
      await user.type(screen.getByRole("searchbox", { name: "Search all messages" }), "flaky")
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(api).toHaveBeenCalledWith(
        "GET",
        "/api/search?q=flaky&limit=20&offset=0&source=&kind=chat&archived=0",
      )
      expect(screen.queryByRole("table")).toBeNull()
      expect(screen.getByText("20 hits")).toBeTruthy()
      await user.click(screen.getByRole("button", { name: "Load more" }))
      await flushPromises()
      expect(api).toHaveBeenCalledWith(
        "GET",
        "/api/search?q=flaky&limit=20&offset=20&source=&kind=chat&archived=0",
      )
      expect(screen.getByText("21 hits")).toBeTruthy()
      expect(screen.queryByRole("button", { name: "Load more" })).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it("renders the match markers as mark elements", async () => {
    const user = setup()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.startsWith("/api/search") ? [hit({ snippet: "<b>x</b> [flaky] test" })] : page) as any,
    )
    const { container } = await mk("/sessions?q=flaky")
    await user.click(screen.getByRole("checkbox", { name: "Full text" }))
    await flushPromises()
    expect(Array.from(container.querySelectorAll("mark")).map((m) => m.textContent)).toEqual([
      "flaky",
    ])
    // The snippet is text, so a tag in it shows as typed.
    expect(screen.getByRole("link", { name: /Release notes/ }).textContent).toContain("<b>x</b>")
  })

  it("puts a space between the title and the snippet of a hit", async () => {
    const user = setup()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) => (p.startsWith("/api/search") ? [hit({})] : page) as any,
    )
    await mk("/sessions?q=flaky")
    await user.click(screen.getByRole("checkbox", { name: "Full text" }))
    await flushPromises()
    expect(
      screen.getByRole("link", { name: /Release notes/ }).textContent!.replace(/\s+/g, " "),
    ).toContain("Release notes the flaky test in cron")
  })

  it("shows a search error under the box and keeps the list", async () => {
    const user = setup()
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (p.startsWith("/api/search")) throw new TypeError("Failed to fetch")
      return page as any
    })
    await mk("/sessions?q=flaky")
    await user.click(screen.getByRole("checkbox", { name: "Full text" }))
    await flushPromises()
    const alerts = screen.getAllByRole("alert")
    expect(alerts).toHaveLength(1)
    expect(alerts[0].textContent).toBe(
      "Search failed: Cannot reach the gateway. Check the network, then try again.",
    )
  })

  it("reads the filters from the route query and writes them back", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue({ ...page, total: 86 })
    const { router } = await mk("/sessions?q=flaky&src=telegram&kind=&arch=1&off=50")
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=telegram&kind=&search=flaky&archived=1&limit=20&offset=40",
    )
    expect(router.currentRoute.value.query).toEqual({
      q: "flaky",
      src: "telegram",
      kind: "",
      arch: "1",
      page: "3",
    })
    await user.selectOptions(screen.getByRole("combobox", { name: "Source" }), "cli")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({ q: "flaky", src: "cli", kind: "", arch: "1" })
  })

  it("sets Kind to all when the source is Cron", async () => {
    const user = setup()
    await mk()
    await user.selectOptions(screen.getByRole("combobox", { name: "Source" }), "cron")
    await flushPromises()
    expect((screen.getByRole("combobox", { name: "Kind" }) as HTMLSelectElement).value).toBe("")
    expect(api).toHaveBeenCalledWith(
      "GET",
      expect.stringContaining("/api/sessions?source=cron&kind=&"),
    )
  })

  it("drops a reply that comes after a newer one", async () => {
    const user = setup()
    const first = deferred<unknown>()
    let n = 0
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (!p.includes("limit=20")) return page as any
      n++
      if (n === 1) return first.p as any
      return { total: 1, rows: [row({ id: "t1", title: "From Telegram" })] } as any
    })
    await mk()
    await user.selectOptions(screen.getByRole("combobox", { name: "Source" }), "telegram")
    await flushPromises()
    first.resolve(page)
    await flushPromises()
    expect(screen.getByText("From Telegram")).toBeTruthy()
    expect(screen.queryByText("Old chat")).toBeNull()
  })

  it("offers Clear filters when the filters match nothing", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue({ total: 0, rows: [] })
    const { router } = await mk("/sessions?src=cli")
    expect(screen.getByText("No sessions match the filters.")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Clear filters" }))
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(api).toHaveBeenCalledWith(
      "GET",
      expect.stringContaining(
        "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=0",
      ),
    )
  })

  it("pages on the server and keeps the size in the browser", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue({ ...page, total: 86 })
    const { router } = await mk()
    expect(screen.getByText("1–20 of 86")).toBeTruthy()
    const nav = screen.getByRole("navigation", { name: "Sessions pages" })
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=20",
    )
    expect(router.currentRoute.value.query).toEqual({ page: "2" })
    expect(screen.getByText("21–40 of 86")).toBeTruthy()
    expect(listLink("sessions")).toEqual({ name: "sessions", query: { page: "2" } })
    await user.selectOptions(screen.getByRole("combobox", { name: "Rows per page" }), "50")
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=50&offset=0",
    )
    expect(localStorage.getItem("tether.rows.sessions")).toBe("50")
    expect(router.currentRoute.value.query).toEqual({})
  })

  it("reads the stored size before the first load", async () => {
    localStorage.setItem("tether.rows.sessions", "50")
    await mk()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=50&offset=0",
    )
  })

  it("disables the pager while a page loads", async () => {
    const user = setup()
    const next = deferred<unknown>()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.includes("limit=20&offset=20") ? next.p : { ...page, total: 86 }) as any,
    )
    await mk()
    const nav = screen.getByRole("navigation", { name: "Sessions pages" })
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    await flushPromises()
    expect(prevPage().disabled).toBe(true)
    next.resolve({ ...page, total: 86 })
    await flushPromises()
    expect(prevPage().disabled).toBe(false)
  })

  it("loads the last page for a page beyond the end", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.includes("offset=1960")
          ? { total: 3, rows: [] }
          : { total: 3, rows: page.rows.slice(0, 3) }) as any,
    )
    const { router } = await mk("/sessions?page=99")
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=1960",
    )
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=0",
    )
    expect(screen.getByText("Release notes")).toBeTruthy()
    expect(screen.queryByText("No sessions match the filters.")).toBeNull()
    expect(router.currentRoute.value.query).toEqual({})
  })

  it("drops the page from the address when the list is empty", async () => {
    vi.mocked(api).mockImplementation(async () => ({ total: 0, rows: [] }) as any)
    const { router } = await mk("/sessions?page=3")
    expect(router.currentRoute.value.query).toEqual({})
  })

  it("goes to the page before when the last row of the last page goes away", async () => {
    const user = setup()
    let count = 21
    vi.mocked(api).mockImplementation(async (_m: string, p: string) => {
      if (p.includes("limit=1&")) return { total: 0, rows: [] } as any
      const off = Number(new URLSearchParams(p.split("?")[1]).get("offset"))
      const all = Array.from({ length: count }, (_, i) =>
        row({ id: `r${i + 1}`, title: `Chat ${i + 1}` }),
      )
      return { total: count, rows: all.slice(off, off + 20) } as any
    })
    vi.mocked(runCommand).mockImplementation(async () => {
      count = 20
      return { request_id: "r", text: "✅ Done.", buttons: [], edit: false }
    })
    const { router } = await mk("/sessions?page=2")
    expect(screen.getByText("Chat 21")).toBeTruthy()
    await menu(user, "Chat 21", "Archive")
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
    expect(screen.getByText("Chat 1")).toBeTruthy()
    expect(screen.queryByText("No sessions match the filters.")).toBeNull()
  })

  it("goes to page 1 when a filter changes", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValue({ ...page, total: 86 })
    const { router } = await mk("/sessions?page=3")
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=40",
    )
    await user.selectOptions(screen.getByRole("combobox", { name: "Source" }), "cli")
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=cli&kind=chat&search=&archived=0&limit=20&offset=0",
    )
    expect(router.currentRoute.value.query).toEqual({ src: "cli" })
  })

  it("loads page 1 when the route drops the page key", async () => {
    vi.mocked(api).mockResolvedValue({ ...page, total: 86 })
    const { router } = await mk("/sessions?page=3")
    vi.mocked(api).mockClear()
    await router.push("/sessions")
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=20&offset=0",
    )
  })

  it("hides Started and Messages in a 1024 px window and shows all at 1280 px", async () => {
    box.width = 704
    const first = await mk()
    expect(shownHeads()).toEqual(["Session", "Source", "Last active", "Cost", ""])
    first.unmount()
    cleanup()
    box.width = 960
    await mk()
    expect(shownHeads()).toEqual([
      "Session",
      "Source",
      "Started",
      "Last active",
      "Messages",
      "Cost",
      "",
    ])
  })

  it("makes Kind optional when the filter shows it", async () => {
    box.width = 704
    await mk("/sessions?kind=")
    expect(shownHeads()).not.toContain("Kind")
  })

  it("gives the actions column a rem width that fits the buttons of the filter", async () => {
    const first = await mk()
    expect(actCol(first.container)).toBe("8.5rem")
    first.unmount()
    cleanup()
    const second = await mk("/sessions?arch=1")
    expect(actCol(second.container)).toBe("14.5rem")
  })

  it("keeps Save and Cancel in one wrapping box with the title field", async () => {
    const user = setup()
    await mk()
    await menu(user, "Release notes", "Rename")
    const wrap = screen.getByRole("textbox", { name: "New title" }).parentElement!
    expect(wrap.classList.contains("flex")).toBe(true)
    expect(wrap.classList.contains("min-w-0")).toBe(true)
    expect(wrap.classList.contains("flex-wrap")).toBe(true)
    expect(within(wrap).getByRole("button", { name: "Save" })).toBeTruthy()
    expect(within(wrap).getByRole("button", { name: "Cancel" })).toBeTruthy()
  })

  it("cuts a long child label", async () => {
    const user = setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "1 child session" }))
    const label = within(rowOf("subagent run")).getByText("subagent run")
    expect(label.classList.contains("truncate")).toBe(true)
    expect(label.classList.contains("min-w-0")).toBe(true)
    expect(label.parentElement!.classList.contains("min-w-0")).toBe(true)
  })
})

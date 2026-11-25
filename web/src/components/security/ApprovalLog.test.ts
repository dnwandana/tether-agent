import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
}))
import { api } from "../../api/client"
import ApprovalLog from "./ApprovalLog.vue"
import { fullTime, shortTime } from "../../lib/time"

const Blank = { render: () => null }
const entry = (o: object) => ({
  at: "2026-11-20T14:05:00Z",
  scope: "dashboard",
  scope_label: "Dashboard",
  session_id: "",
  kind: "chat",
  tool: "terminal",
  server: "",
  tool_name: "",
  rule: "rm_recursive",
  rule_description: "recursive delete",
  class: "",
  decision: "allowed",
  source: "owner",
  answer: "once",
  preview: "rm -rf build/",
  ...o,
})
const logPage = {
  total: 412,
  offset: 0,
  limit: 20,
  rows: [
    entry({ session_id: "0123456789abcdef012" }),
    entry({
      scope: "telegram:-100:7",
      scope_label: "Telegram › Ops › topic 7",
      tool: "mcp_gmail.send_message",
      server: "gmail",
      tool_name: "send_message",
      decision: "denied",
      answer: "deny",
      preview: "to: x",
    }),
  ],
}
const base = "/api/security/log?"

async function mk(path = "/security?tab=log", user = userEvent.setup({ pointerEventsCheck: 0 })) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/security", name: "security", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push(path)
  const r = render(ApprovalLog, {
    props: {
      scopes: ["dashboard", "telegram:-100:7"],
      scopeLabels: { dashboard: "Dashboard", "telegram:-100:7": "Telegram › Ops › topic 7" },
    },
    global: { plugins: [router], stubs: { teleport: true } },
  })
  await flushPromises()
  return { r, user }
}
const scopeSel = () => screen.getByRole("combobox", { name: "Scope" }) as HTMLSelectElement
const decisionSel = () => screen.getByRole("combobox", { name: "Decision" }) as HTMLSelectElement
const search = () => screen.getByRole("searchbox", { name: "Search the previews" })
// Returns the body rows. Row 0 of the table is the header row.
const bodyRows = () => screen.getAllByRole("row").slice(1)
const cells = (i: number) => within(bodyRows()[i]).getAllByRole("cell")
// Returns the pager of the approval log.
const pager = () => within(screen.getByRole("navigation", { name: "Approval log pages" }))

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(logPage)
})

describe("ApprovalLog", () => {
  it("reads the first page with all filter keys and pages it", async () => {
    const { user } = await mk()
    expect(api).toHaveBeenCalledWith("GET", base + "scope=&decision=&source=&q=&offset=0&limit=20")
    expect(screen.getByText("1–20 of 412")).toBeTruthy()
    expect(screen.queryByText("1–2 of 412")).toBeNull()
    await user.click(pager().getByRole("button", { name: "Next Page" }))
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=20&limit=20",
    )
  })

  it("filters by scope, decision, and No answer on the server", async () => {
    const { user } = await mk()
    await user.selectOptions(scopeSel(), "dashboard")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=&source=&q=&offset=0&limit=20",
    )
    await user.selectOptions(decisionSel(), "denied")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=denied&source=&q=&offset=0&limit=20",
    )
    const noAnswer = screen.getByRole("checkbox", { name: "No answer" })
    expect(noAnswer.getAttribute("data-slot")).toBe("checkbox")
    await user.click(noAnswer)
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=denied&source=timeout&q=&offset=0&limit=20",
    )
  })

  it("searches the preview after a short delay", async () => {
    vi.useFakeTimers()
    try {
      const { user } = await mk(
        "/security?tab=log",
        userEvent.setup({ advanceTimers: vi.advanceTimersByTime, pointerEventsCheck: 0 }),
      )
      await user.type(search(), "rm -rf")
      await vi.advanceTimersByTimeAsync(300)
      expect(api).toHaveBeenLastCalledWith(
        "GET",
        base + "scope=&decision=&source=&q=rm+-rf&offset=0&limit=20",
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it("reads the first scope from the route query", async () => {
    await mk("/security?tab=log&scope=dashboard")
    expect(api).toHaveBeenCalledWith(
      "GET",
      base + "scope=dashboard&decision=&source=&q=&offset=0&limit=20",
    )
  })

  it("adds an option for a route scope that is not in the list", async () => {
    await mk("/security?tab=log&scope=cli")
    expect(scopeSel().value).toBe("cli")
    expect(scopeSel().getAttribute("data-slot")).toBe("native-select")
    expect(
      within(scopeSel())
        .getAllByRole("option")
        .map((o) => o.getAttribute("value")),
    ).toEqual(["", "cli", "dashboard", "telegram:-100:7"])
  })

  it("shows the scope labels in the select and keeps the keys as the values", async () => {
    await mk("/security?tab=log&scope=cli")
    const opts = within(scopeSel()).getAllByRole("option")
    expect(opts.map((o) => o.textContent!.trim())).toEqual([
      "All scopes",
      "cli",
      "Dashboard",
      "Telegram › Ops › topic 7",
    ])
    expect(opts.map((o) => o.getAttribute("value"))).toEqual([
      "",
      "cli",
      "dashboard",
      "telegram:-100:7",
    ])
  })

  it("shows the main columns and moves the session and the MCP tool to the details", async () => {
    const { user } = await mk()
    expect(screen.getAllByRole("columnheader").map((t) => t.textContent!.trim())).toEqual([
      "Time",
      "Scope",
      "Tool",
      "Rule",
      "Decision",
      "Answer",
      "Preview",
    ])
    expect(screen.queryByRole("link", { name: "0123456789abcdef012" })).toBeNull()
    await user.click(bodyRows()[0])
    expect(screen.getByRole("link", { name: "0123456789abcdef012" }).getAttribute("href")).toBe(
      "/sessions/0123456789abcdef012",
    )
    await user.click(bodyRows()[1])
    expect(screen.getByText("gmail")).toBeTruthy()
    expect(screen.getByText("send_message")).toBeTruthy()
  })

  it("shows the rule description with the rule id in the tooltip", async () => {
    await mk()
    const rule = within(cells(0)[3]).getByText("recursive delete")
    expect(rule.getAttribute("title")).toBe("rm_recursive")
  })

  it("shows a short time on one line and the scope label in bdi", async () => {
    await mk()
    const at = within(cells(1)[0]).getByText(shortTime("2026-11-20T14:05:00Z"))
    expect(at.getAttribute("title")).toBe(fullTime("2026-11-20T14:05:00Z"))
    expect(at.classList.contains("whitespace-nowrap")).toBe(true)
    const bdi = cells(1)[1].querySelector("bdi")!
    expect(bdi.textContent).toBe("Telegram › Ops › topic 7")
    expect(bdi.getAttribute("title")).toBe("Telegram › Ops › topic 7")
  })

  it("shows a dash for a time that is not known", async () => {
    vi.mocked(api).mockResolvedValue({ ...logPage, rows: [entry({ at: "" })] })
    await mk()
    expect(cells(0)[0].textContent!.trim()).toBe("—")
  })

  it("lets the search box grow in the toolbar", async () => {
    await mk()
    const q = search()
    expect(q.getAttribute("data-slot")).toBe("input")
    expect(q.classList.contains("flex-1")).toBe(true)
    expect(within(q.parentElement!).getByRole("combobox", { name: "Scope" })).toBeTruthy()
  })

  it("opens the detail of a row", async () => {
    const { user } = await mk()
    await user.click(bodyRows()[1])
    expect(screen.getAllByText("Approval detail").length).toBeGreaterThan(0)
    expect(screen.getAllByText("to: x")).toHaveLength(2)
    expect(screen.getByText("send_message")).toBeTruthy()
  })

  it("disables the pager while a page loads, drops an old reply, and goes to page 1 on a filter", async () => {
    let release!: (v: unknown) => void
    const { user, r } = await mk()
    vi.mocked(api).mockImplementationOnce(() => new Promise((res) => (release = res)) as any)
    await user.click(pager().getByRole("button", { name: "Next Page" }))
    expect(pager().getByRole("button", { name: "Next Page" }).hasAttribute("disabled")).toBe(true)
    expect(pager().getByRole("button", { name: "Previous Page" }).hasAttribute("disabled")).toBe(
      true,
    )
    vi.mocked(api).mockResolvedValueOnce({ ...logPage, rows: [entry({ preview: "newest" })] })
    await user.selectOptions(decisionSel(), "blocked")
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=blocked&source=&q=&offset=0&limit=20",
    )
    release({ ...logPage, offset: 20, rows: [entry({ preview: "old page" })] })
    await flushPromises()
    expect(r.container.textContent).toContain("newest")
    expect(r.container.textContent).not.toContain("old page")
    expect(pager().getByRole("button", { name: "Page 1" }).getAttribute("aria-current")).toBe(
      "page",
    )
  })

  it("shows the empty text with Show all scopes", async () => {
    vi.mocked(api).mockResolvedValue({ total: 0, offset: 0, limit: 20, rows: [] })
    const { user } = await mk("/security?tab=log&scope=dashboard")
    expect(screen.getByText("No approvals in this scope.")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Show all scopes" }))
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=0&limit=20",
    )
  })

  it("reads the stored size and keeps a new size", async () => {
    localStorage.setItem("tether.rows.approval-log", "50")
    const { user } = await mk()
    expect(api).toHaveBeenCalledWith("GET", base + "scope=&decision=&source=&q=&offset=0&limit=50")
    await user.selectOptions(screen.getByRole("combobox", { name: "Rows per page" }), "100")
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=0&limit=100",
    )
    expect(localStorage.getItem("tether.rows.approval-log")).toBe("100")
  })

  it("moves to the last page when the total shrinks", async () => {
    const { user } = await mk()
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p.includes("offset=20")
          ? { total: 15, offset: 20, limit: 20, rows: [] }
          : { ...logPage, total: 15 }) as any,
    )
    await user.click(pager().getByRole("button", { name: "Next Page" }))
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=0&limit=20",
    )
    expect(screen.queryByText("No approvals in this scope.")).toBeNull()
    expect(screen.getByText("1–15 of 15")).toBeTruthy()
  })

  it("lets the preview wrap in its cell", async () => {
    await mk()
    const preview = screen.getByText("rm -rf build/")
    expect(preview.closest("td")!.classList.contains("whitespace-normal")).toBe(true)
  })
})

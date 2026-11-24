import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
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

let router: ReturnType<typeof createRouter>
async function mk(path = "/security?tab=log") {
  router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/security", name: "security", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push(path)
  const w = mount(ApprovalLog, {
    props: {
      scopes: ["dashboard", "telegram:-100:7"],
      scopeLabels: { dashboard: "Dashboard", "telegram:-100:7": "Telegram › Ops › topic 7" },
    },
    global: { plugins: [router], stubs: { teleport: true } },
  })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

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
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", base + "scope=&decision=&source=&q=&offset=0&limit=20")
    expect(w.find(".pager").text()).toContain("1–2 of 412")
    await btn(w, "Next ▶").trigger("click")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=20&limit=20",
    )
  })

  it("filters by scope, decision, and No answer on the server", async () => {
    const w = await mk()
    await w.find("select[aria-label='Scope']").setValue("dashboard")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=&source=&q=&offset=0&limit=20",
    )
    await w.find("select[aria-label='Decision']").setValue("denied")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=denied&source=&q=&offset=0&limit=20",
    )
    await w.find("input[type='checkbox']").setValue(true)
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=dashboard&decision=denied&source=timeout&q=&offset=0&limit=20",
    )
  })

  it("searches the preview after a short delay", async () => {
    vi.useFakeTimers()
    try {
      const w = await mk()
      await w.find("input[type='search']").setValue("rm -rf")
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
    const w = await mk("/security?tab=log&scope=cli")
    const sel = w.find("select[aria-label='Scope']")
    expect((sel.element as HTMLSelectElement).value).toBe("cli")
    expect(sel.findAll("option").map((o) => o.attributes("value"))).toEqual([
      "",
      "cli",
      "dashboard",
      "telegram:-100:7",
    ])
  })

  it("shows the scope labels in the select and keeps the keys as the values", async () => {
    const w = await mk("/security?tab=log&scope=cli")
    const opts = w.find("select[aria-label='Scope']").findAll("option")
    expect(opts.map((o) => o.text())).toEqual([
      "All scopes",
      "cli",
      "Dashboard",
      "Telegram › Ops › topic 7",
    ])
    expect(opts.map((o) => o.attributes("value"))).toEqual([
      "",
      "cli",
      "dashboard",
      "telegram:-100:7",
    ])
  })

  it("shows the main columns and moves the session and the MCP tool to the details", async () => {
    const w = await mk()
    expect(w.findAll("th").map((t) => t.text())).toEqual([
      "Time",
      "Scope",
      "Tool",
      "Rule",
      "Decision",
      "Answer",
      "Preview",
    ])
    expect(w.find("table a[href='/sessions/0123456789abcdef012']").exists()).toBe(false)
    await w.findAll("tbody tr")[0].trigger("click")
    expect(w.find(".log-detail a[href='/sessions/0123456789abcdef012']").exists()).toBe(true)
    await w.findAll("tbody tr")[1].trigger("click")
    expect(w.find(".log-detail").text()).toContain("gmail")
    expect(w.find(".log-detail").text()).toContain("send_message")
  })

  it("shows the rule description with the rule id in the tooltip", async () => {
    const w = await mk()
    const rule = w.findAll("tbody tr")[0].findAll("td")[3].find("span")
    expect(rule.text()).toBe("recursive delete")
    expect(rule.attributes("title")).toBe("rm_recursive")
  })

  it("shows a short time on one line and the scope label in bdi", async () => {
    const w = await mk()
    const tds = w.findAll("tbody tr")[1].findAll("td")
    const at = tds[0].find("span")
    expect(at.text()).toBe(shortTime("2026-11-20T14:05:00Z"))
    expect(at.attributes("title")).toBe(fullTime("2026-11-20T14:05:00Z"))
    expect(at.attributes("style")).toContain("white-space: nowrap")
    const bdi = tds[1].find("bdi")
    expect(bdi.text()).toBe("Telegram › Ops › topic 7")
    expect(bdi.attributes("title")).toBe("Telegram › Ops › topic 7")
  })

  it("shows a dash for a time that is not known", async () => {
    vi.mocked(api).mockResolvedValue({ ...logPage, rows: [entry({ at: "" })] })
    const w = await mk()
    expect(w.findAll("tbody tr")[0].findAll("td")[0].find("span").text()).toBe("—")
  })

  it("lets the search box grow in the toolbar", async () => {
    const w = await mk()
    const q = w.find("input[type='search']")
    expect(q.classes()).toEqual(expect.arrayContaining(["input", "grow-search"]))
    expect(q.element.parentElement!.classList.contains("toolbar")).toBe(true)
  })

  it("opens the detail of a row", async () => {
    const w = await mk()
    await w.findAll("tbody tr")[1].trigger("click")
    const panel = w.findComponent({ name: "SidePanel" })
    expect(panel.props("open")).toBe(true)
    expect(panel.props("title")).toBe("Approval detail")
    expect(w.find(".log-detail").text()).toContain("to: x")
    expect(w.find(".log-detail").text()).toContain("send_message")
  })

  it("disables the pager while a page loads and drops an old reply", async () => {
    let release!: (v: unknown) => void
    const w = await mk()
    vi.mocked(api).mockImplementationOnce(() => new Promise((r) => (release = r)) as any)
    await btn(w, "Next ▶").trigger("click")
    expect(btn(w, "Next ▶").attributes("disabled")).toBeDefined()
    expect(btn(w, "◀ Prev").attributes("disabled")).toBeDefined()
    vi.mocked(api).mockResolvedValueOnce({ ...logPage, rows: [entry({ preview: "newest" })] })
    await w.find("select[aria-label='Decision']").setValue("blocked")
    await flushPromises()
    release({ ...logPage, offset: 20, rows: [entry({ preview: "old page" })] })
    await flushPromises()
    expect(w.text()).toContain("newest")
    expect(w.text()).not.toContain("old page")
  })

  it("shows the empty text with Show all scopes", async () => {
    vi.mocked(api).mockResolvedValue({ total: 0, offset: 0, limit: 20, rows: [] })
    const w = await mk("/security?tab=log&scope=dashboard")
    expect(w.text()).toContain("No approvals in this scope.")
    w.findComponent({ name: "StateBlock" }).vm.$emit("action")
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      base + "scope=&decision=&source=&q=&offset=0&limit=20",
    )
  })
})

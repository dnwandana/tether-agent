import { describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h as hv } from "vue"
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
import { api, runCommand } from "../api/client"
import { confirm, ui } from "../stores/ui"
import { emitEvents } from "../lib/bus"
import SecurityPage from "./SecurityPage.vue"

const view = {
  mode: "guarded",
  modes: [
    {
      scope: "dashboard",
      mode: "guarded",
      set_at: "0001-01-01T00:00:00Z",
      scope_label: "Dashboard",
    },
    {
      scope: "telegram:-100:7",
      mode: "read-only",
      set_at: "2026-11-12T10:00:00Z",
      scope_label: "Telegram › Ops › topic 7",
    },
  ],
  log_scopes: ["dashboard", "telegram:-100:7"],
  log_scope_labels: { dashboard: "Dashboard", "telegram:-100:7": "Telegram › Ops › topic 7" },
  grants: [
    {
      rule: "git push",
      description: "Push to a remote",
      preview: "git push origin main",
      workdir: "~/projects/ledger",
      via: "make",
      programs: ["git"],
      server: "",
      tool_name: "",
      created: "2026-11-15T10:00:00Z",
      remove: "/approvals remove 1 a1b2c3d4",
    },
  ],
  remove_all: "/approvals remove all f00dbabe",
  grants_off: true,
  grants_off_cause: "bad row 7",
  secrets: [
    {
      name: "GITHUB_TOKEN",
      state: "set",
      commands: ["gh"],
      failed_at: "0001-01-01T00:00:00Z",
      pins: [{ path: "/usr/bin/gh", sha256: "ab12" }],
      set: "/secrets set GITHUB_TOKEN",
      unpin: "/secrets unpin GITHUB_TOKEN",
    },
    {
      name: "PYPI_TOKEN",
      state: "not set",
      commands: ["twine upload"],
      failed_at: "0001-01-01T00:00:00Z",
      pins: [],
      set: "/secrets set PYPI_TOKEN",
      unpin: "/secrets unpin PYPI_TOKEN",
    },
  ],
  process_mode: "smart",
  cron_mode: "deny",
  fixed: {
    protected_paths: ["~/.ssh/", "~/.tether/state.db"],
    human_only: ["password change"],
    mcp_classes: { gmail: { send: 2, read: 5 }, slack: { send: 1 } },
    acp_presets: [],
    acp_workdirs: [],
    toolsets: { telegram: ["all"] },
  },
}
const logPage = {
  total: 412,
  rows: [
    {
      at: "2026-11-20T14:05:00Z",
      scope: "Dashboard",
      kind: "chat",
      tool: "terminal",
      rule: "recursive delete",
      class: "",
      decision: "allowed",
      source: "owner",
      answer: "once",
      preview: "rm -rf build/",
    },
    {
      at: "2026-11-20T13:21:00Z",
      scope: "Telegram › DM",
      kind: "chat",
      tool: "mcp_gmail.send_message",
      rule: "mcp send",
      class: "send",
      decision: "denied",
      source: "owner",
      answer: "deny",
      preview: "to: x",
    },
  ],
}

const card = (kind: string) => ({
  id: "c",
  kind,
  header: "",
  preview: "",
  session_id: "",
  at: "",
})

// Keeps the last props of the stubbed children, so the tests can check them.
const seen: { note: any; log: any; rules: any } = { note: null, log: null, rules: null }
const NoteStub = defineComponent({
  name: "ResultNote",
  props: ["note"],
  setup(p) {
    return () => {
      seen.note = p.note
      return p.note ? hv("div", p.note.text) : null
    }
  },
})
const LogStub = defineComponent({
  name: "ApprovalLog",
  props: ["scopes", "scopeLabels"],
  setup(p) {
    return () => ((seen.log = { ...p }), hv("div", "approval log"))
  },
})
const RulesStub = defineComponent({
  name: "RulesTab",
  props: ["fixed", "processMode", "cronMode"],
  setup(p) {
    return () => ((seen.rules = { ...p }), hv("div", "rules tab"))
  },
})

async function mk(path = "/security", stubs: Record<string, unknown> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/security", name: "security", component: SecurityPage }],
  })
  await router.push(path)
  const r = render(SecurityPage, {
    global: { plugins: [router], stubs: { DevicesTable: true, ResultNote: NoteStub, ...stubs } },
  })
  await flushPromises()
  return { ...r, router, user: userEvent.setup() }
}
const tab = async (user: any, label: string) => {
  await user.click(screen.getAllByRole("tab").find((t) => t.textContent!.startsWith(label))!)
  await flushPromises()
}
const btn = (name: string) => screen.getByRole("button", { name })
// The body rows of all tables: a header row has no cell.
const bodyRows = () =>
  screen.getAllByRole("row").filter((r) => within(r).queryAllByRole("cell").length > 0)
const reply = (text: string, buttons: any[][] = []) => ({
  request_id: "r",
  text,
  buttons,
  edit: false,
})

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
      async (_m: string, p: string) => (p === "/api/security" ? view : logPage) as any,
    )
  vi.mocked(runCommand).mockReset().mockResolvedValue(reply("Done."))
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  ui.trayOpen = false
  seen.note = seen.log = seen.rules = null
})

// Finds a paragraph by its text. The text can have a space at the edges.
const line = (t: string) =>
  screen.getByText((_, el) => el?.tagName === "P" && el.textContent?.trim() === t)
// The rows of the grant list that the paging test made.
const ruleRows = () => bodyRows().filter((r) => r.textContent!.includes("rule "))

describe("SecurityPage", () => {
  it("shows one line for each fixed mode and links to the Rules tab", async () => {
    const { user } = await mk("/security", { RulesTab: RulesStub })
    const first = line("Process mode: smart")
    line("Cron mode: deny")
    // The modes table and the fixed-modes card share one parent element.
    expect(first.closest("[data-slot=card]")!.parentElement!.querySelector("table")).not.toBeNull()
    await user.click(btn("What each mode does"))
    await flushPromises()
    expect(screen.getByRole("tab", { name: "Rules", selected: true })).toBeTruthy()
    expect(screen.getByText("rules tab")).toBeTruthy()
  })

  it("isolates the scope label of a mode in bdi", async () => {
    await mk()
    const bdi = screen.getByText("Telegram › Ops › topic 7")
    expect(bdi.tagName).toBe("BDI")
    expect(bdi.getAttribute("title")).toBe("Telegram › Ops › topic 7")
  })

  it("draws one row for each server and class", async () => {
    await mk("/security?tab=rules")
    expect(document.body.textContent).toContain("gmail")
    expect(document.body.textContent).toContain("slack")
    const rows = bodyRows().filter(
      (r) => /gmail|slack/.test(r.textContent!) && /send|read/.test(r.textContent!),
    )
    expect(rows).toHaveLength(3)
  })

  it("sets Full through the confirm of the page and sends /mode full confirm", async () => {
    const { user } = await mk()
    expect(document.body.textContent).toContain("View only. Send /mode in that chat.")
    expect(document.body.textContent).toContain("Telegram › Ops › topic 7")
    const group = screen.getByRole("group", { name: "Mode of the Dashboard" })
    expect(within(group).getAllByRole("button")).toHaveLength(3)
    await user.click(btn("⚡ Full"))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({
        title: "Set ⚡ Full for the Dashboard?",
        body:
          "Now: 🛡 Guarded.\n" +
          "Dangerous commands run with no question in this scope. Human-only rules still ask.\n" +
          "A script can then add a dashboard session.",
        danger: true,
      }),
    )
    expect(runCommand).toHaveBeenCalledTimes(1)
    expect(runCommand).toHaveBeenCalledWith("/mode full confirm")
  })

  it("sends nothing when the owner cancels the Full confirm", async () => {
    vi.mocked(confirm).mockResolvedValue(false)
    const { user } = await mk()
    await user.click(btn("⚡ Full"))
    await flushPromises()
    expect(runCommand).not.toHaveBeenCalled()
    expect(btn("⚡ Full").getAttribute("aria-pressed")).toBe("false")
  })

  it("removes grants with the codes of the list and shows the damaged store", async () => {
    const { user } = await mk("/security?tab=grants")
    expect(screen.getByRole("alert").textContent!.replace(/\s+/g, " ").trim()).toBe(
      "Saved answers are off, because the answer store is damaged: bad row 7. Restart the gateway after you repair it.",
    )
    expect(document.body.textContent).toContain("Push to a remote")
    await user.click(btn("Remove"))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/approvals remove 1 a1b2c3d4")
    await user.click(btn("Remove all…"))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/approvals remove all f00dbabe")
  })

  it("mounts the approval log with the log scopes", async () => {
    await mk("/security?tab=log", { ApprovalLog: LogStub })
    expect(screen.getByText("approval log")).toBeTruthy()
    expect(seen.log.scopes).toEqual(["dashboard", "telegram:-100:7"])
    expect(seen.log.scopeLabels).toEqual({
      dashboard: "Dashboard",
      "telegram:-100:7": "Telegram › Ops › topic 7",
    })
  })

  it("unpins one program by its path from the Secrets tab", async () => {
    const { user } = await mk("/security?tab=secrets")
    const unpin = screen.getAllByRole("button", { name: "Unpin" })
    expect(unpin).toHaveLength(1)
    await user.click(unpin[0])
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/secrets unpin GITHUB_TOKEN /usr/bin/gh")
    expect(api).toHaveBeenLastCalledWith("GET", "/api/security")
  })

  it("passes the modes and the fixed rules to the Rules tab", async () => {
    await mk("/security?tab=rules", { RulesTab: RulesStub })
    expect(seen.rules.processMode).toBe("smart")
    expect(seen.rules.cronMode).toBe("deny")
  })

  it("shows the mode reply in a result note", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("🧭 **Mode of this topic: read-only**"))
    const { user } = await mk()
    await user.click(btn("👁 Read-only"))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/mode read-only")
    expect(seen.note.text).toBe("🧭 **Mode of this topic: read-only**")
    expect(seen.note.kind).toBe("info")
  })

  it("names the tab Always answers and pages the grants", async () => {
    const many = Array.from({ length: 25 }, (_, i) => ({
      ...view.grants[0],
      rule: "rule " + i,
      remove: `/approvals remove ${i + 1} a1b2c3d4`,
    }))
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/security" ? { ...view, grants: many } : logPage) as any,
    )
    const { user } = await mk()
    await tab(user, "Always answers")
    expect(ruleRows()).toHaveLength(20)
    await user.click(btn("Next ▶"))
    await flushPromises()
    expect(ruleRows()).toHaveLength(5)
  })

  it("shows the count of the grants on the tab", async () => {
    await mk()
    expect(within(screen.getAllByRole("tab")[1]).getByText(String(view.grants.length))).toBeTruthy()
  })

  it("goes back to the last page when a Remove empties the page", async () => {
    let rows = Array.from({ length: 21 }, (_, i) => ({
      ...view.grants[0],
      rule: "rule " + i,
      remove: `/approvals remove ${i + 1} a1b2c3d4`,
    }))
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/security" ? { ...view, grants: rows } : logPage) as any,
    )
    vi.mocked(runCommand).mockImplementationOnce(async () => {
      rows = rows.slice(0, 20)
      return reply("✅ Removed 1 Always answer.")
    })
    const { user } = await mk("/security?tab=grants")
    await user.click(btn("Next ▶"))
    await user.click(btn("Remove"))
    await flushPromises()
    expect(document.body.textContent).toContain("1–20 of 20")
    expect(document.body.textContent).not.toContain("21–20")
  })

  it("shows the empty text of the Always answers", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/security" ? { ...view, grants: [] } : logPage) as any,
    )
    await mk("/security?tab=grants")
    expect(screen.getByText("An ✅ Always answer shows here.")).toBeTruthy()
  })

  it("shows the Remove reply in a result note", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("✅ Removed 1 Always answer."))
    const { user } = await mk("/security?tab=grants")
    await user.click(btn("Remove"))
    await flushPromises()
    expect(seen.note.text).toBe("✅ Removed 1 Always answer.")
  })

  it("opens the tray after Set value", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("🔑 Answer the card."))
    const { user } = await mk("/security?tab=secrets")
    await user.click(screen.getAllByRole("button", { name: "Set value…" })[0])
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets set GITHUB_TOKEN")
    expect(ui.trayOpen).toBe(true)
    expect(seen.note.text).toBe("Type the value in the 🔑 card in ⏳ Waiting.")
  })

  it("loads again when a secret or an approval card closes", async () => {
    await mk()
    vi.mocked(api).mockClear()
    emitEvents([{ name: "card.done", data: card("clarify") }])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
    emitEvents([{ name: "card.done", data: card("secret") }])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/security")
  })

  it("uses no legacy class", async () => {
    const { container } = await mk("/security?tab=grants")
    expect(
      container.querySelector(
        ".page-h, .page-stack, .chip, .seg, .btn, .bad-banner, .pager, .fixed-modes, .c-3, .t-12",
      ),
    ).toBeNull()
  })
})

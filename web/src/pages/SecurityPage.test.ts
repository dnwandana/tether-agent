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
import { api, runCommand } from "../api/client"
import { confirm, ui } from "../stores/ui"
import { emitEvents } from "../lib/bus"
import ResultNote from "../components/ResultNote.vue"
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

async function mk(path = "/security") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/security", name: "security", component: SecurityPage }],
  })
  await router.push(path)
  const w = mount(SecurityPage, { global: { plugins: [router], stubs: { DevicesTable: true } } })
  await flushPromises()
  return w
}
const tab = async (w: any, label: string) => {
  await w
    .findAll('[role="tab"]')
    .find((t: any) => t.text().startsWith(label))!
    .trigger("click")
  await flushPromises()
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!
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
})

describe("SecurityPage", () => {
  it("shows the process mode and the cron mode with the config keys", async () => {
    const w = await mk()
    const cards = w.findAll(".card-pad")
    const proc = cards.find((c) => c.text().includes("Process mode"))!
    const cron = cards.find((c) => c.text().includes("Cron mode"))!
    expect(proc.text()).toContain("smart")
    expect(proc.text()).toContain("approvals.mode")
    expect(cron.text()).toContain("deny")
    expect(cron.text()).toContain("approvals.cron_mode")
  })

  it("draws one row for each server and class", async () => {
    const w = await mk("/security?tab=rules")
    expect(w.text()).toContain("gmail")
    expect(w.text()).toContain("slack")
    const rows = w
      .findAll("tr")
      .filter((r) => /gmail|slack/.test(r.text()) && /send|read/.test(r.text()))
    expect(rows).toHaveLength(3)
  })

  it("sets Full through the confirm of the page and sends /mode full confirm", async () => {
    const w = await mk()
    expect(w.text()).toContain("View only. Send /mode in that chat.")
    expect(w.text()).toContain("Telegram › Ops › topic 7")
    await btn(w, "⚡ Full").trigger("click")
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
    const w = await mk()
    await btn(w, "⚡ Full").trigger("click")
    await flushPromises()
    expect(runCommand).not.toHaveBeenCalled()
  })

  it("removes grants with the codes of the list and shows the damaged store", async () => {
    const w = await mk("/security?tab=grants")
    expect(w.find(".bad-banner").text()).toBe(
      "Saved answers are off, because the answer store is damaged: bad row 7. Restart the gateway after you repair it.",
    )
    expect(w.text()).toContain("Push to a remote")
    await btn(w, "Remove").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/approvals remove 1 a1b2c3d4")
    await btn(w, "Remove all…").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/approvals remove all f00dbabe")
  })

  it("mounts the approval log with the log scopes", async () => {
    const w = await mk("/security?tab=log")
    const log = w.findComponent({ name: "ApprovalLog" })
    expect(log.exists()).toBe(true)
    expect(log.props("scopes")).toEqual(["dashboard", "telegram:-100:7"])
  })

  it("unpins one program by its path from the Secrets tab", async () => {
    const w = await mk("/security?tab=secrets")
    expect(w.findAll("button").filter((b) => b.text() === "Unpin")).toHaveLength(1)
    await btn(w, "Unpin").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/secrets unpin GITHUB_TOKEN /usr/bin/gh")
    expect(api).toHaveBeenLastCalledWith("GET", "/api/security")
  })

  it("passes the modes and the fixed rules to the Rules tab", async () => {
    const w = await mk("/security?tab=rules")
    const rules = w.findComponent({ name: "RulesTab" })
    expect(rules.props("processMode")).toBe("smart")
    expect(rules.props("cronMode")).toBe("deny")
  })

  it("shows the mode reply in a result note", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("🧭 **Mode of this topic: read-only**"))
    const w = await mk()
    await btn(w, "👁 Read-only").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/mode read-only")
    const note = w.findComponent(ResultNote).props("note")
    expect(note.text).toBe("🧭 **Mode of this topic: read-only**")
    expect(note.kind).toBe("info")
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
    const w = await mk()
    await tab(w, "Always answers")
    expect(w.findAll("tr").filter((r) => r.text().includes("rule "))).toHaveLength(20)
    await btn(w, "Next ▶").trigger("click")
    expect(w.findAll("tr").filter((r) => r.text().includes("rule "))).toHaveLength(5)
  })

  it("shows the count of the grants on the tab", async () => {
    const w = await mk()
    expect(w.findAll('[role="tab"]')[1].find(".badge").text()).toBe(String(view.grants.length))
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
    const w = await mk("/security?tab=grants")
    await btn(w, "Next ▶").trigger("click")
    await btn(w, "Remove").trigger("click")
    await flushPromises()
    expect(w.text()).toContain("1–20 of 20")
    expect(w.text()).not.toContain("21–20")
  })

  it("shows the empty text of the Always answers", async () => {
    vi.mocked(api).mockImplementation(
      async (_m: string, p: string) =>
        (p === "/api/security" ? { ...view, grants: [] } : logPage) as any,
    )
    const w = await mk("/security?tab=grants")
    expect(w.text()).toContain("An ✅ Always answer shows here.")
  })

  it("shows the Remove reply in a result note", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("✅ Removed 1 Always answer."))
    const w = await mk("/security?tab=grants")
    await btn(w, "Remove").trigger("click")
    await flushPromises()
    expect(w.findComponent(ResultNote).props("note").text).toBe("✅ Removed 1 Always answer.")
  })

  it("opens the tray after Set value", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(reply("🔑 Answer the card."))
    const w = await mk("/security?tab=secrets")
    await w
      .findAll("button")
      .filter((b) => b.text() === "Set value…")[0]
      .trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets set GITHUB_TOKEN")
    expect(ui.trayOpen).toBe(true)
    expect(w.findComponent(ResultNote).props("note").text).toBe(
      "Type the value in the 🔑 card in ⏳ Waiting.",
    )
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
})

import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  setSessionHeader: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, runCommand } from "../api/client"
import { confirm } from "../stores/ui"
import SecurityPage from "./SecurityPage.vue"

const view = {
  mode: "guarded",
  modes: [
    { scope: "dashboard", mode: "guarded", set_at: "0001-01-01T00:00:00Z" },
    { scope: "telegram:-100:7", mode: "read-only", set_at: "2026-11-12T10:00:00Z" },
  ],
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
      programs: ["/usr/bin/gh"],
      set: "/secrets set GITHUB_TOKEN",
      unpin: "/secrets unpin GITHUB_TOKEN",
    },
    {
      name: "PYPI_TOKEN",
      state: "not set",
      commands: ["twine upload"],
      failed_at: "0001-01-01T00:00:00Z",
      programs: [],
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

  it("sets Full in the dashboard scope through the confirm of /mode", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("Turn on ⚡ Full in this scope?", [
        [{ text: "❌ Cancel", data: "/mode" }],
        [{ text: "⚡ Confirm full", data: "/mode full confirm" }],
      ]),
    )
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/security")
    expect(w.text()).toContain("read-only · change it in that chat")
    await btn(w, "⚡ Full…").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenNthCalledWith(1, "/mode full")
    expect(confirm).toHaveBeenCalledWith({
      title: "Turn on ⚡ Full in this scope?",
      body: "",
      confirmLabel: "⚡ Confirm full",
      danger: true,
    })
    expect(runCommand).toHaveBeenNthCalledWith(2, "/mode full confirm")
  })

  it("removes grants with the codes of the list and shows the damaged store", async () => {
    const w = await mk("/security?tab=grants")
    expect(w.find(".bad-banner").text()).toBe(
      "Saved answers are off, because the answer store is damaged: bad row 7. Restart the gateway after you repair it.",
    )
    await btn(w, "Remove").trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/approvals remove 1 a1b2c3d4")
    await btn(w, "Remove all…").trigger("click")
    expect(runCommand).toHaveBeenLastCalledWith("/approvals remove all f00dbabe")
  })

  it("pages the approval log", async () => {
    const w = await mk("/security?tab=log")
    expect(api).toHaveBeenCalledWith("GET", "/api/security/log?scope=&offset=0&limit=20")
    expect(w.find(".pager").text()).toContain("1–2 of 412")
    await btn(w, "Next ▶").trigger("click")
    expect(api).toHaveBeenLastCalledWith("GET", "/api/security/log?scope=&offset=20&limit=20")
    await w.find("select").setValue("dashboard")
    expect(api).toHaveBeenLastCalledWith(
      "GET",
      "/api/security/log?scope=dashboard&offset=0&limit=20",
    )
  })

  it("sets and unpins secrets with /secrets", async () => {
    const w = await mk("/security?tab=secrets")
    expect(w.findAll("button").filter((b) => b.text() === "Unpin")).toHaveLength(1)
    await w
      .findAll("button")
      .filter((b) => b.text() === "Set value…")[1]
      .trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/secrets set PYPI_TOKEN")
    await btn(w, "Unpin").trigger("click")
    expect(runCommand).toHaveBeenLastCalledWith("/secrets unpin GITHUB_TOKEN")
  })
})

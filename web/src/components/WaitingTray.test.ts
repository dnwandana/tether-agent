import { flushPromises, mount } from "@vue/test-utils"
import { beforeEach, describe, expect, it, vi } from "vitest"

const push = vi.fn()
vi.mock("vue-router", () => ({ useRouter: () => ({ push }) }))
vi.mock("../api/client", () => ({
  api: vi.fn(async () => undefined),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ confirm: vi.fn(async () => true), toast: vi.fn() }))

import WaitingTray from "./WaitingTray.vue"
import { cards } from "../stores/cards"
import { api } from "../api/client"
import { confirm, toast } from "../stores/ui"

const future = new Date(Date.now() + 120_000).toISOString()
const base = {
  summary: "",
  rule: "",
  description: "",
  workdir: "",
  via: "",
  child: "",
  diff: "",
  secrets: [],
  findings: [],
  choices: [],
  multi: false,
  secret_name: "",
  reason: "",
  device: "",
  ip: "",
  code: "",
  deadline: future,
}
const approval = (id: string, command: string, human = false) => ({
  ...base,
  id,
  kind: "approval",
  header: human ? "🔒 Human-only action" : "🛡 Approval needed",
  command,
  human_only: human,
  answers: human ? ["deny", "once"] : ["deny", "once", "session", "always"],
})
const signin = {
  ...base,
  id: "s1",
  kind: "signin",
  header: "🔑 New dashboard sign-in",
  command: "",
  human_only: true,
  answers: ["deny", "allow"],
  device: "Chrome on Android",
  ip: "10.8.0.5",
  code: "4821",
}

beforeEach(() => {
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (_m: string, p: string, body?: any) =>
      p === "/api/cards/bulk" ? ({ answered: body.ids.length } as any) : undefined,
    )
  vi.mocked(confirm).mockClear()
  vi.mocked(toast).mockClear()
  push.mockClear()
  cards.open = [
    approval("a1", "rm -rf build/"),
    approval("a2", "sudo reboot", true),
    signin,
    approval("a3", "go test ./..."),
  ] as never
})

describe("WaitingTray", () => {
  it("lists the open cards with their icons", () => {
    const w = mount(WaitingTray, { props: { open: true } })
    const rows = w.findAll(".tray-item")
    expect(rows).toHaveLength(4)
    expect(rows.map((r) => r.find(".k").text())).toEqual(["🛡", "🔒", "🔑", "🛡"])
    expect(rows[0].text()).toContain("Approval needed")
    expect(rows[0].text()).toContain("rm -rf build/")
    expect(rows[2].text()).toContain("Chrome on Android · 10.8.0.5 · check code 4821")
    expect(w.find(".tray-h .badge").text()).toBe("4")
  })

  it("approves all after a confirm that lists each command", async () => {
    const w = mount(WaitingTray, { props: { open: true } })
    await w.find('[data-act="approve-all"]').trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Approve 2 commands once?",
      body: "rm -rf build/\ngo test ./...\n\nHuman-only actions stay open. Each answer is “once”.",
      confirmLabel: "✅ Approve all once",
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "once",
      ids: ["a1", "a3"],
    })
    expect(toast).toHaveBeenCalledWith("Approved 2 commands once.", "ok")
  })

  it("posts only the ids that the confirm showed", async () => {
    let done!: (v: boolean) => void
    vi.mocked(confirm).mockReturnValueOnce(
      new Promise((r) => {
        done = r
      }),
    )
    const w = mount(WaitingTray, { props: { open: true } })
    await w.find('[data-act="approve-all"]').trigger("click")
    cards.open = [...cards.open, approval("a4", "curl -X POST https://example.com")] as never
    done(true)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "once",
      ids: ["a1", "a3"],
    })
  })

  it("denies all except human-only and sign-in cards", async () => {
    const w = mount(WaitingTray, { props: { open: true } })
    await w.find('[data-act="deny-all"]').trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "deny",
      ids: ["a1", "a3"],
    })
  })

  it("shows the count that the server answered", async () => {
    vi.mocked(api).mockResolvedValueOnce({ answered: 1 } as any)
    const w = mount(WaitingTray, { props: { open: true } })
    await w.find('[data-act="deny-all"]').trigger("click")
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("Denied 1 approval. Questions stay open.")
  })

  it("sends one answer for a double tap", async () => {
    let done!: () => void
    vi.mocked(api).mockReturnValueOnce(
      new Promise((r) => {
        done = () => r(undefined)
      }),
    )
    const w = mount(WaitingTray, { props: { open: true } })
    const deny = () =>
      w
        .findAll(".tray-item")[0]
        .findAll("button")
        .find((b) => b.text() === "❌ Deny")!
    await deny().trigger("click")
    expect(deny().attributes("disabled")).toBeDefined()
    await deny().trigger("click")
    expect(api).toHaveBeenCalledTimes(1)
    done()
    await flushPromises()
    expect(deny().attributes("disabled")).toBeUndefined()
  })

  it("answers a sign-in card", async () => {
    const w = mount(WaitingTray, { props: { open: true } })
    const allow = w
      .findAll(".tray-item")[2]
      .findAll("button")
      .find((b) => b.text() === "✅ Allow")!
    await allow.trigger("click")
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/s1", {
      answer: "allow",
      choices: [],
      text: "",
      value: "",
    })
  })

  it("opens a card in the chat and closes on the scrim", async () => {
    const w = mount(WaitingTray, { props: { open: true } })
    await w.findAll(".tray-item")[0].find('[data-act="open"]').trigger("click")
    expect(push).toHaveBeenCalledWith({ name: "chat", hash: "#card-a1" })
    expect(w.emitted("close")).toHaveLength(1)
    await w.find(".ov").trigger("click")
    expect(w.emitted("close")).toHaveLength(2)
  })

  it("shows the empty hint", () => {
    cards.open = []
    const w = mount(WaitingTray, { props: { open: true } })
    expect(w.text()).toContain("Nothing waits.")
    expect(w.find('[data-act="approve-all"]').exists()).toBe(false)
  })
})

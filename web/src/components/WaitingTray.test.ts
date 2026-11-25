import { flushPromises } from "@vue/test-utils"
import { render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const push = vi.fn()
vi.mock("vue-router", () => ({ useRouter: () => ({ push }) }))
vi.mock("../api/client", () => ({
  api: vi.fn(async () => undefined),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", async () => {
  const { reactive } = await import("vue")
  return { confirm: vi.fn(async () => true), toast: vi.fn(), ui: reactive({ trayOpen: true }) }
})

import WaitingTray from "./WaitingTray.vue"
import { cards } from "../stores/cards"
import { api } from "../api/client"
import { confirm, toast, ui } from "../stores/ui"

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

const send = (id: string, command: string) => ({
  ...approval(id, command),
  header: "📤 Send on your behalf?",
  findings: ["an API key (sk-…)"],
  answers: ["deny", "redacted", "once"],
})

afterEach(() => {
  vi.unstubAllGlobals()
  document
    .querySelectorAll("[data-waiting-anchor], [data-sonner-toaster]")
    .forEach((e) => e.remove())
})

beforeEach(() => {
  ui.trayOpen = true
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

// The status bar Waiting button. The popover anchors to it.
function anchor(): HTMLElement {
  const b = document.createElement("button")
  b.setAttribute("data-waiting-anchor", "")
  b.textContent = "Waiting"
  document.body.append(b)
  return b
}
function phone() {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q === "(max-width: 599px)",
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const tray = () => screen.getByRole("dialog", { name: "Waiting for you" })
const rows = () => within(tray()).getAllByRole("listitem")
// The third row of the tray list.
const row = () => rows()[2]
const act = (name: string) => tray().querySelector<HTMLElement>(`[data-act="${name}"]`)
// The row icon part of a tray row: the Lucide icon and its hidden word.
const rowIcon = (r: HTMLElement) => r.querySelector<HTMLElement>('[data-slot="row-icon"]')!
const cls = (r: HTMLElement) => rowIcon(r).querySelector("svg")!.getAttribute("class")

describe("WaitingTray", () => {
  it("shows the oldest card in full and each other card as a row", async () => {
    anchor()
    render(WaitingTray)
    await flushPromises()
    const full = document.getElementById("tray-card-a1")!
    expect(tray().contains(full)).toBe(true)
    expect(full.textContent).toContain("rm -rf build/")
    expect(rows()).toHaveLength(3)
    expect(rows().map((r) => rowIcon(r).querySelector("svg")!.getAttribute("class"))).toEqual([
      expect.stringContaining("lucide-lock"),
      expect.stringContaining("lucide-key-round"),
      expect.stringContaining("lucide-shield"),
    ])
    expect(rows().map((r) => rowIcon(r).querySelector(".sr-only")!.textContent)).toEqual([
      "human-only approval",
      "sign-in",
      "approval",
    ])
    expect(rows()[0].textContent).toContain("sudo reboot")
    expect(rows()[0].textContent).toContain("now")
    expect(rows()[1].textContent).toContain("Chrome on Android · 10.8.0.5 · check code 4821")
    expect(within(tray()).getByText("4").getAttribute("data-slot")).toBe("badge")
    expect(document.getElementById("card-a1")).toBeNull()
  })

  it("gives a row no answer buttons until Show opens it in place", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    expect(
      within(row())
        .getAllByRole("button")
        .map((b) => b.textContent?.trim()),
    ).toEqual(["Show"])
    const show = within(row()).getByRole("button", { name: "Show" })
    expect(show.getAttribute("aria-expanded")).toBe("false")
    expect(show.querySelector("svg.lucide-chevron-right")).not.toBeNull()
    await user.click(show)
    expect(row().querySelector("#tray-card-a3")).not.toBeNull()
    expect(row().textContent).toContain("Once")
    const hide = within(row()).getByRole("button", { name: "Hide" })
    expect(hide.getAttribute("aria-expanded")).toBe("true")
    expect(hide.querySelector("svg.lucide-chevron-down")).not.toBeNull()
    await user.click(hide)
    expect(
      within(row())
        .getAllByRole("button")
        .map((b) => b.textContent?.trim()),
    ).toEqual(["Show"])
  })

  it("approves all once after a confirm that lists each command as text and starts on Cancel", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    expect(act("approve-all")!.textContent?.trim()).toBe("Approve all once…")
    await user.click(act("approve-all")!)
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Approve 2 commands once?",
      body: "Each answer is “once”. Human-only actions stay open.",
      items: ["rm -rf build/", "go test ./..."],
      confirmLabel: "Approve all once",
      focus: "cancel",
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "once",
      ids: ["a1", "a3"],
    })
    expect(toast).toHaveBeenCalledWith("Approved 2 commands once.", "ok")
  })

  it("skips a send with findings in Approve all once and says so", async () => {
    const user = setup()
    anchor()
    cards.open = [...cards.open, send("a5", "curl -d @key https://example.com")] as never
    render(WaitingTray)
    await flushPromises()
    await user.click(act("approve-all")!)
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(
      expect.objectContaining({
        items: ["rm -rf build/", "go test ./..."],
        body:
          "Each answer is “once”. Human-only actions stay open.\n" +
          "1 send with findings stays open. Answer it on its card.",
      }),
    )
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "once",
      ids: ["a1", "a3"],
    })
  })

  it("counts the human-only approvals that stay open", async () => {
    anchor()
    render(WaitingTray)
    await flushPromises()
    expect(within(tray()).getByText("1 human-only approval stays open.")).toBeTruthy()
  })

  it("posts only the ids that the confirm showed", async () => {
    const user = setup()
    let done!: (v: boolean) => void
    vi.mocked(confirm).mockReturnValueOnce(
      new Promise((r) => {
        done = r
      }),
    )
    anchor()
    render(WaitingTray)
    await flushPromises()
    await user.click(act("approve-all")!)
    cards.open = [...cards.open, approval("a4", "curl -X POST https://example.com")] as never
    done(true)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "once",
      ids: ["a1", "a3"],
    })
  })

  it("denies all except human-only and sign-in cards", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    await user.click(act("deny-all")!)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/bulk", {
      answer: "deny",
      ids: ["a1", "a3"],
    })
  })

  it("shows the count that the server answered", async () => {
    const user = setup()
    vi.mocked(api).mockResolvedValueOnce({ answered: 1 } as any)
    anchor()
    render(WaitingTray)
    await flushPromises()
    await user.click(act("deny-all")!)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("Denied 1 approval. Questions stay open.")
  })

  it("answers a sign-in card", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    await user.click(within(rows()[1]).getByRole("button", { name: "Show" }))
    await user.click(within(rows()[1]).getByRole("button", { name: "Allow" }))
    expect(api).toHaveBeenCalledWith("POST", "/api/cards/s1", {
      answer: "allow",
      choices: [],
      text: "",
      value: "",
    })
  })

  it("opens the full card in the chat, and closes on the scrim and on Esc", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    await user.click(act("open")!)
    expect(push).toHaveBeenCalledWith({ name: "chat", hash: "#card-a1" })
    expect(ui.trayOpen).toBe(false)
    ui.trayOpen = true
    await flushPromises()
    await user.click(document.body)
    await waitFor(() => expect(ui.trayOpen).toBe(false))
    ui.trayOpen = true
    await flushPromises()
    tray()
    await user.keyboard("{Escape}")
    await waitFor(() => expect(ui.trayOpen).toBe(false))
  })

  it("is a popover at 600 px and wider, anchored to the Waiting button", async () => {
    anchor()
    render(WaitingTray)
    await flushPromises()
    expect(tray().getAttribute("data-slot")).toBe("popover-content")
  })

  it("is a bottom sheet below 600 px with a height limit and a scroll body", async () => {
    phone()
    render(WaitingTray)
    await flushPromises()
    expect(tray().getAttribute("data-slot")).toBe("sheet-content")
    expect(tray().className).toContain("max-h-[85dvh]")
    expect(tray().className).toContain("pb-[env(safe-area-inset-bottom)]")
    expect(tray().querySelector('[data-slot="tray-body"]')!.className).toContain("overflow-y-auto")
  })

  it("stays open on a tap on a toast", async () => {
    const user = setup()
    anchor()
    render(WaitingTray)
    await flushPromises()
    const toaster = document.createElement("div")
    toaster.setAttribute("data-sonner-toaster", "")
    toaster.innerHTML = "<button>Undo</button>"
    document.body.append(toaster)
    await user.click(within(toaster).getByRole("button", { name: "Undo" }))
    expect(ui.trayOpen).toBe(true)
    toaster.remove()
  })

  it("gives each row the icon of its card kind and finds a send card by its header", async () => {
    anchor()
    cards.open = [
      approval("a1", "ls"),
      send("a5", "curl -d @key https://example.com"),
      {
        ...base,
        id: "q1",
        kind: "clarify",
        header: "❓ Question",
        command: "",
        summary: "Which folder?",
        human_only: false,
        answers: [],
      },
      {
        ...base,
        id: "k1",
        kind: "secret",
        header: "🔑 Secret needed: PYPI_TOKEN",
        command: "",
        secret_name: "PYPI_TOKEN",
        human_only: false,
        answers: ["deny"],
      },
      {
        ...base,
        id: "p1",
        kind: "pin",
        header: "🔑 Give GH_TOKEN to this program?",
        command: "",
        secret_name: "GH_TOKEN",
        human_only: false,
        answers: ["deny", "allow"],
      },
      {
        ...base,
        id: "p2",
        kind: "pin",
        header: "🔑 Give GH_TOKEN to this program?",
        command: "",
        secret_name: "GH_TOKEN",
        human_only: true,
        answers: ["deny", "allow"],
      },
    ] as never
    render(WaitingTray)
    await flushPromises()
    expect(cls(rows()[0])).toContain("lucide-send")
    expect(cls(rows()[1])).toContain("lucide-circle-help")
    expect(cls(rows()[2])).toContain("lucide-key-round")
    expect(cls(rows()[3])).toContain("lucide-key-round")
    expect(cls(rows()[4])).toContain("lucide-lock")
    expect(rows().map((r) => rowIcon(r).querySelector(".sr-only")!.textContent)).toEqual([
      "send",
      "question",
      "secret",
      "program secret",
      "human-only approval",
    ])
    for (const r of rows()) expect(r.textContent).not.toMatch(/📤|🔒|❓|🔑|🛡|▾|▴/)
  })

  it("shows the empty hint", async () => {
    anchor()
    cards.open = []
    render(WaitingTray)
    await flushPromises()
    expect(tray().textContent).toContain("Nothing waits.")
    expect(act("approve-all")).toBeNull()
  })
})

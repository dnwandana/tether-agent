import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import type { Card } from "../../api/types.gen"

vi.mock("../../stores/cards", () => ({
  cards: { open: [] },
  answerCard: vi.fn(() => Promise.resolve()),
}))
vi.mock("../../api/client", () => ({
  ApiError: class ApiError extends Error {
    status: number
    constructor(status: number, msg: string) {
      super(msg)
      this.status = status
    }
  },
}))

import { answerCard } from "../../stores/cards"
import { ApiError } from "../../api/client"
import ApprovalCard from "./ApprovalCard.vue"

function card(over: Partial<Card>): Card {
  return {
    id: "c1",
    kind: "approval",
    header: "🛡 Approval needed",
    summary: "",
    command: "",
    rule: "",
    description: "",
    workdir: "",
    via: "",
    child: "",
    diff: "",
    secrets: [],
    findings: [],
    human_only: false,
    answers: [],
    choices: [],
    multi: false,
    deadline: "",
    secret_name: "",
    reason: "",
    device: "",
    ip: "",
    code: "",
    ...over,
  } as Card
}
const none = { answer: "", choices: [], text: "", value: "" }
const mk = (over: Partial<Card>) => render(ApprovalCard, { props: { card: card(over) } })
// Returns the button labels of each row, top row first.
const rows = (r: ReturnType<typeof mk>) =>
  Array.from(r.container.querySelectorAll("[data-row]")).map((row) =>
    within(row as HTMLElement)
      .getAllByRole("button")
      .map((b) => b.textContent!.trim()),
  )
const variant = (r: ReturnType<typeof mk>, name: string) =>
  within(r.container).getByRole("button", { name }).getAttribute("data-variant")

beforeEach(() => {
  vi.mocked(answerCard).mockReset().mockResolvedValue(undefined)
})
afterEach(() => vi.useRealTimers())
const timer = () => screen.getByRole("timer")

describe("ApprovalCard", () => {
  it("shows Deny alone on row 1 and the allowed answers on row 2", () => {
    const r = mk({ command: "rm -rf build/", answers: ["deny", "once", "session", "always"] })
    const header = r.container.querySelector('[data-slot="item-header"]')!
    expect(header.textContent).toContain("Approval needed")
    expect(header.textContent).not.toContain("🛡")
    expect(header.querySelector("svg.lucide-shield")!.getAttribute("class")).toContain("text-info")
    expect(r.container.querySelector("code")!.textContent).toBe("rm -rf build/")
    expect(rows(r)).toEqual([["Deny"], ["Once", "Session", "Always"]])
  })

  it("follows the answer list: no session, no always, and Send redacted with findings", () => {
    expect(rows(mk({ answers: ["deny", "once"] }))).toEqual([["Deny"], ["Once"]])
    const send = mk({
      header: "📤 Send on your behalf?",
      findings: ["an API key (sk-…)"],
      answers: ["deny", "redacted", "once"],
    })
    expect(rows(send)).toEqual([["Deny"], ["Send redacted", "Once"]])
    const finding = within(send.container).getByText("an API key (sk-…)")
    expect(finding.parentElement!.querySelector("svg.lucide-triangle-alert")).not.toBeNull()
    expect(send.container.textContent).not.toContain("⚠")
  })

  it("marks a human-only card", () => {
    mk({ header: "🔒 Human-only action", human_only: true, answers: ["deny", "once"] })
    const root = screen.getByRole("group", { name: "Human-only action" })
    expect(root.querySelector('[data-slot="item-header"] svg.lucide-lock')).not.toBeNull()
    expect(root.getAttribute("data-human-only")).toBe("true")
    expect(
      screen.getByText(
        "A human-only action needs your answer each time. “Approve all” never answers it.",
      ),
    ).toBeTruthy()
  })

  it("shows the pin card with Allow this program", () => {
    const r = mk({
      kind: "pin",
      header: "🔑 Give GITHUB_TOKEN to this program?",
      description: "/usr/bin/gh",
      rule: "sha256 3f1a9c",
      command: "gh pr list",
      answers: ["deny", "allow"],
    })
    expect(rows(r)).toEqual([["Deny"], ["Allow this program"]])
    expect(
      r.container.querySelector('[data-slot="item-header"] svg.lucide-key-round'),
    ).not.toBeNull()
    const dd = screen.getAllByRole("definition").map((d) => d.textContent!.trim())
    expect(dd).toContain("/usr/bin/gh")
    expect(dd).toContain("sha256 3f1a9c")
  })

  it("shows each secret line with the key icon and no mark", () => {
    mk({
      command: "gh pr list",
      secrets: ["🔑 Gets: GH_TOKEN", "This turn read: web_fetch"],
      answers: ["deny", "once"],
    })
    const dd = screen.getAllByRole("definition").at(-1)!
    expect(dd.textContent).toContain("Gets: GH_TOKEN")
    expect(dd.textContent).toContain("This turn read: web_fetch")
    expect(dd.textContent).not.toContain("🔑")
    expect(dd.querySelectorAll("svg.lucide-key-round")).toHaveLength(1)
  })

  it("posts the answer", async () => {
    mk({ answers: ["deny", "once"] })
    await userEvent.setup().click(screen.getByRole("button", { name: "Once" }))
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, answer: "once" })
  })

  it("shows the 409 text of a closed card", async () => {
    vi.mocked(answerCard).mockRejectedValue(
      new (ApiError as any)(409, "This card is already answered."),
    )
    mk({ answers: ["deny", "once"] })
    await userEvent.setup().click(screen.getByRole("button", { name: "Deny" }))
    await flushPromises()
    expect(screen.getByRole("alert").textContent!.trim()).toBe("This card is already answered.")
  })

  it("counts down to the deadline", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    const r = mk({ answers: ["deny", "once"], deadline: "2026-11-20T10:01:30Z" })
    expect(screen.getByRole("timer").textContent!.trim()).toBe("1:30 left")
    await vi.advanceTimersByTimeAsync(1000)
    expect(screen.getByRole("timer").textContent!.trim()).toBe("1:29 left")
    r.unmount()
  })

  it("fills the countdown bar with the time that is left", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    const r = mk({ answers: ["deny", "once"], deadline: "2026-11-20T10:01:30Z" })
    const bar = screen.getByRole("progressbar")
    expect(bar.getAttribute("data-slot")).toBe("progress")
    expect(bar.getAttribute("aria-valuenow")).toBe("100")
    await vi.advanceTimersByTimeAsync(45_000)
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("50")
    r.unmount()
  })

  it("gives Deny the outline weight, the allow answers secondary, and Always the ghost weight", () => {
    const r = mk({ command: "rm -rf build/", answers: ["deny", "once", "session", "always"] })
    expect(variant(r, "Deny")).toBe("outline")
    expect(variant(r, "Once")).toBe("secondary")
    expect(variant(r, "Session")).toBe("secondary")
    expect(variant(r, "Always")).toBe("ghost")
    expect(r.container.querySelector('[data-variant="default"]')).toBeNull()
    const send = mk({ header: "📤 Send on your behalf?", answers: ["deny", "redacted", "once"] })
    expect(variant(send, "Send redacted")).toBe("secondary")
    const pin = mk({ kind: "pin", answers: ["deny", "allow"] })
    expect(variant(pin, "Allow this program")).toBe("secondary")
  })

  it("makes each button 44 px high on a phone", () => {
    const r = mk({ answers: ["deny", "once", "always"] })
    for (const b of within(r.container).getAllByRole("button"))
      expect(b.classList.contains("max-tab:h-11")).toBe(true)
  })

  it("wraps a long command, so the page does not scroll to the side", () => {
    const r = mk({ command: "x".repeat(400), answers: ["deny"] })
    const code = r.container.querySelector("code")!
    expect(code.classList.contains("whitespace-pre-wrap")).toBe(true)
    expect(code.classList.contains("break-all")).toBe(true)
  })

  it("shows the scope of Always for a shell card and for an MCP card", () => {
    const hint = (r: ReturnType<typeof mk>) => r.container.querySelector("[data-always-hint]")
    const shell = mk({
      command: "make test",
      workdir: "/home/me/app",
      answers: ["deny", "once", "always"],
    })
    expect(hint(shell)!.textContent!.replace(/\s+/g, " ").trim()).toBe(
      "Always: this exact command in /home/me/app, from now on.",
    )
    expect(hint(shell)!.querySelector("code")!.textContent).toBe("/home/me/app")
    const mcp = mk({ command: "github.create_issue", answers: ["deny", "once", "always"] })
    expect(hint(mcp)!.textContent!.trim()).toBe(
      "Always: this tool, with any arguments, from now on.",
    )
    expect(hint(mk({ answers: ["deny", "once"] }))).toBeNull()
  })

  it("names the card root as a group with its header", () => {
    mk({ answers: ["deny"] })
    const root = screen.getByRole("group", { name: "Approval needed" })
    expect(root.getAttribute("data-slot")).toBe("item")
    expect(root.getAttribute("data-variant")).toBe("outline")
  })

  it("gives the countdown a timer label that changes every 15 s", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    const r = mk({ answers: ["deny", "once"], deadline: "2026-11-20T10:01:30Z" })
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:30")
    await vi.advanceTimersByTimeAsync(1000)
    expect(timer().textContent!.trim()).toBe("1:29 left")
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:30")
    await vi.advanceTimersByTimeAsync(14_000)
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:15")
    r.unmount()
  })
})

describe("approval card fields and deadline", () => {
  it("shows the pin card fields from description, rule, and command", () => {
    mk({
      kind: "pin",
      summary: "wrong",
      description: "/usr/bin/gh",
      rule: "sha256 3f1a9c",
      command: "gh pr list",
      answers: ["deny", "allow"],
    })
    expect(screen.getAllByRole("definition").map((d) => d.textContent!.trim())).toEqual([
      "/usr/bin/gh",
      "sha256 3f1a9c",
      "gh pr list",
    ])
  })

  it("treats the zero time as no deadline", () => {
    mk({ deadline: "0001-01-01T00:00:00Z", answers: ["deny"] })
    expect(screen.queryByRole("timer")).toBeNull()
    expect(screen.queryByRole("progressbar")).toBeNull()
  })

  it("sets the id card-<id> and uses no legacy class", () => {
    const r = mk({ id: "x9", child: "sub", human_only: true })
    expect(screen.getByRole("group").getAttribute("id")).toBe("card-x9")
    expect(r.container.innerHTML).not.toContain("c-faint")
    expect(r.container.innerHTML).not.toContain("icard")
  })
})

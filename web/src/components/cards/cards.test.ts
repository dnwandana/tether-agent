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
import ClarifyCard from "./ClarifyCard.vue"
import SecretCard from "./SecretCard.vue"

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

beforeEach(() => {
  vi.mocked(answerCard).mockReset().mockResolvedValue(undefined)
})
afterEach(() => vi.useRealTimers())

const done = () => screen.getByRole("button", { name: "Done" })
const timer = () => screen.getByRole("timer")

describe("ClarifyCard", () => {
  const q = { kind: "clarify", header: "❓ Question", summary: "Which folder?" } as const

  it("sends one choice", async () => {
    render(ClarifyCard, { props: { card: card({ ...q, choices: ["main", "dev"] }) } })
    await userEvent.setup().click(screen.getByRole("button", { name: "main" }))
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, choices: ["main"] })
  })

  it("sends the selection of a multi card with Done", async () => {
    render(ClarifyCard, { props: { card: card({ ...q, choices: ["a", "b", "c"], multi: true }) } })
    const user = userEvent.setup()
    expect(done().hasAttribute("disabled")).toBe(true)
    await user.click(screen.getByRole("button", { name: "☐ a" }))
    await user.click(screen.getByRole("button", { name: "☐ b" }))
    expect(screen.getByRole("button", { name: "✓ a" }).getAttribute("aria-pressed")).toBe("true")
    expect(screen.getByRole("button", { name: "☐ c" }).getAttribute("aria-pressed")).toBe("false")
    await user.click(done())
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, choices: ["a", "b"] })
  })

  it("sends free text when the card has no choices", async () => {
    render(ClarifyCard, { props: { card: card({ ...q }) } })
    const user = userEvent.setup()
    await user.type(screen.getByRole("textbox", { name: "Your answer" }), "  feature/x  ")
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, text: "feature/x" })
  })

  it("shows the question as the body text under the header", () => {
    const r = render(ClarifyCard, { props: { card: card({ ...q, choices: ["main", "dev"] }) } })
    expect(r.container.querySelector('[data-slot="item-header"]')!.textContent).toContain(
      "❓ Question",
    )
    const question = screen.getByText("Which folder?")
    expect(question.closest('[data-slot="item-content"]')).not.toBeNull()
    expect(question.classList.contains("whitespace-pre-wrap")).toBe(true)
    const root = screen.getByRole("group", { name: "❓ Question" })
    expect(root.getAttribute("data-slot")).toBe("item")
  })

  it("labels the free-text input Your answer", () => {
    const r = render(ClarifyCard, { props: { card: card({ ...q }) } })
    expect(r.container.textContent).not.toContain("(Reply with a message.)")
    const input = screen.getByRole("textbox", { name: "Your answer" })
    expect(input.getAttribute("data-slot")).toBe("input")
  })

  it("gives the countdown a timer label that changes every 15 s", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    const r = render(ClarifyCard, {
      props: { card: card({ ...q, deadline: "2026-11-20T10:01:30Z" }) },
    })
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:30")
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("100")
    await vi.advanceTimersByTimeAsync(1000)
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:30")
    await vi.advanceTimersByTimeAsync(14_000)
    expect(timer().getAttribute("aria-label")).toBe("Time left: 1:15")
    r.unmount()
  })
})

describe("SecretCard", () => {
  it("posts the value and keeps no copy", async () => {
    render(SecretCard, {
      props: {
        card: card({
          kind: "secret",
          header: "🔑 Secret needed: PYPI_TOKEN",
          secret_name: "PYPI_TOKEN",
          description: "twine upload",
          reason: "publish the package",
        }),
      },
    })
    const user = userEvent.setup()
    const input = screen.getByPlaceholderText("Paste the value") as HTMLInputElement
    expect(input.getAttribute("type")).toBe("password")
    expect(input.getAttribute("autocomplete")).toBe("off")
    expect(input.getAttribute("data-slot")).toBe("input")
    expect(screen.getAllByRole("definition").map((d) => d.textContent)).toContain(
      "publish the package",
    )
    await user.type(input, "s3cr3t-value")
    await user.click(screen.getByRole("button", { name: "Save" }))
    await flushPromises()
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, value: "s3cr3t-value" })
    expect(input.value).toBe("")
    expect(JSON.stringify({ ...localStorage })).not.toContain("s3cr3t-value")
  })

  it("gives Deny the outline weight and keeps Save primary", () => {
    render(SecretCard, {
      props: {
        card: card({ kind: "secret", header: "🔑 Secret needed: PYPI_TOKEN", answers: ["deny"] }),
      },
    })
    expect(screen.getByRole("button", { name: "❌ Deny" }).getAttribute("data-variant")).toBe(
      "outline",
    )
    expect(screen.getByRole("button", { name: "Save" }).getAttribute("data-variant")).toBe(
      "default",
    )
    const root = screen.getByRole("group", { name: "🔑 Secret needed: PYPI_TOKEN" })
    expect(root.getAttribute("data-slot")).toBe("item")
    const rows = Array.from(root.querySelectorAll("[data-row]")).map((row) =>
      within(row as HTMLElement)
        .getAllByRole("button")
        .map((b) => b.textContent!.trim()),
    )
    expect(rows).toEqual([["❌ Deny"], ["Save"]])
    for (const b of within(root).getAllByRole("button"))
      expect(b.classList.contains("max-tab:h-11")).toBe(true)
  })
})

describe("card fields and deadline", () => {
  it("shows the commands of a secret card in For", () => {
    render(SecretCard, {
      props: {
        card: card({
          kind: "secret",
          command: "twine upload\ntwine check",
          description: "x",
          reason: "r",
        }),
      },
    })
    const forValue = screen.getAllByRole("definition")[0]
    expect(forValue.textContent).toContain("twine upload")
    expect(forValue.textContent).toContain("twine check")
    expect(forValue.textContent).not.toBe("x")
    expect(forValue.classList.contains("whitespace-pre-wrap")).toBe(true)
  })

  it("treats the zero time as no deadline", () => {
    const zero = "0001-01-01T00:00:00Z"
    for (const [C, k] of [
      [ClarifyCard, "clarify"],
      [SecretCard, "secret"],
    ] as const) {
      const r = render(C, { props: { card: card({ kind: k, deadline: zero, answers: ["deny"] }) } })
      expect(within(r.container).queryByRole("timer")).toBeNull()
      expect(within(r.container).queryByRole("progressbar")).toBeNull()
      r.unmount()
    }
  })

  it("sets the id card-<id> and uses no c-faint", () => {
    for (const [C, k] of [
      [ClarifyCard, "clarify"],
      [SecretCard, "secret"],
    ] as const) {
      const r = render(C, {
        props: { card: card({ id: "x9", kind: k, child: "sub", human_only: true }) },
      })
      expect(within(r.container).getByRole("group").getAttribute("id")).toBe("card-x9")
      expect(r.container.innerHTML).not.toContain("c-faint")
      expect(r.container.innerHTML).not.toContain("icard")
      r.unmount()
    }
  })

  it("denies a secret card", async () => {
    render(SecretCard, { props: { card: card({ kind: "secret", answers: ["deny"] }) } })
    await userEvent.setup().click(screen.getByRole("button", { name: "❌ Deny" }))
    await flushPromises()
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, answer: "deny" })
  })
})

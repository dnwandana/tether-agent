import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
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
const rows = (w: ReturnType<typeof mount>) =>
  w.findAll(".icard-row").map((r) => r.findAll("button").map((b) => b.text()))
const none = { answer: "", choices: [], text: "", value: "" }

beforeEach(() => {
  vi.mocked(answerCard).mockReset().mockResolvedValue(undefined)
})
afterEach(() => vi.useRealTimers())

describe("ApprovalCard", () => {
  it("shows Deny alone on row 1 and the allowed answers on row 2", () => {
    const w = mount(ApprovalCard, {
      props: {
        card: card({
          command: "rm -rf build/",
          answers: ["deny", "once", "session", "always"],
        }),
      },
    })
    expect(w.find(".icard-h").text()).toContain("🛡 Approval needed")
    expect(w.find(".cmd").text()).toBe("rm -rf build/")
    expect(rows(w)).toEqual([["❌ Deny"], ["✅ Once", "✅ Session", "✅ Always"]])
  })

  it("follows the answer list: no session, no always, and Send redacted with findings", () => {
    const noSession = mount(ApprovalCard, {
      props: { card: card({ answers: ["deny", "once"] }) },
    })
    expect(rows(noSession)).toEqual([["❌ Deny"], ["✅ Once"]])
    const send = mount(ApprovalCard, {
      props: {
        card: card({
          header: "📤 Send on your behalf?",
          findings: ["an API key (sk-…)"],
          answers: ["deny", "redacted", "once"],
        }),
      },
    })
    expect(rows(send)).toEqual([["❌ Deny"], ["✅ Send redacted", "✅ Once"]])
    expect(send.find(".finding").text()).toContain("an API key (sk-…)")
  })

  it("marks a human-only card", () => {
    const w = mount(ApprovalCard, {
      props: {
        card: card({
          header: "🔒 Human-only action",
          human_only: true,
          answers: ["deny", "once"],
        }),
      },
    })
    expect(w.find(".icard").classes()).toContain("icard-human")
    expect(w.text()).toContain(
      "A human-only action needs your answer each time. “Approve all” never answers it.",
    )
  })

  it("shows the pin card with Allow this program", () => {
    const w = mount(ApprovalCard, {
      props: {
        card: card({
          kind: "pin",
          header: "🔑 Give GITHUB_TOKEN to this program?",
          description: "/usr/bin/gh",
          rule: "sha256 3f1a9c",
          command: "gh pr list",
          answers: ["deny", "allow"],
        }),
      },
    })
    expect(rows(w)).toEqual([["❌ Deny"], ["✅ Allow this program"]])
    expect(w.find("dl.ikv").text()).toContain("/usr/bin/gh")
    expect(w.find("dl.ikv").text()).toContain("sha256 3f1a9c")
  })

  it("posts the answer", async () => {
    const w = mount(ApprovalCard, {
      props: { card: card({ answers: ["deny", "once"] }) },
    })
    await w
      .findAll("button")
      .find((b) => b.text() === "✅ Once")!
      .trigger("click")
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, answer: "once" })
  })

  it("shows the 409 text of a closed card", async () => {
    vi.mocked(answerCard).mockRejectedValue(
      new (ApiError as any)(409, "This card is already answered."),
    )
    const w = mount(ApprovalCard, {
      props: { card: card({ answers: ["deny", "once"] }) },
    })
    await w
      .findAll("button")
      .find((b) => b.text() === "❌ Deny")!
      .trigger("click")
    await flushPromises()
    expect(w.find(".c-bad").text()).toBe("This card is already answered.")
  })

  it("counts down to the deadline", async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date("2026-11-20T10:00:00Z"))
    const w = mount(ApprovalCard, {
      props: {
        card: card({
          answers: ["deny", "once"],
          deadline: "2026-11-20T10:01:30Z",
        }),
      },
    })
    expect(w.find(".cd").text()).toBe("⌛ 1:30")
    await vi.advanceTimersByTimeAsync(1000)
    expect(w.find(".cd").text()).toBe("⌛ 1:29")
    w.unmount()
  })
})

describe("ClarifyCard", () => {
  const q = { kind: "clarify", header: "❓ Which branch?" } as const

  it("sends one choice", async () => {
    const w = mount(ClarifyCard, {
      props: { card: card({ ...q, choices: ["main", "dev"] }) },
    })
    await w.findAll(".choice")[0].trigger("click")
    expect(answerCard).toHaveBeenCalledWith("c1", {
      ...none,
      choices: ["main"],
    })
  })

  it("sends the selection of a multi card with Done", async () => {
    const w = mount(ClarifyCard, {
      props: { card: card({ ...q, choices: ["a", "b", "c"], multi: true }) },
    })
    const done = () => w.findAll("button").find((b) => b.text() === "Done")!
    expect(done().attributes("disabled")).toBeDefined()
    await w.findAll(".choice")[0].trigger("click")
    await w.findAll(".choice")[1].trigger("click")
    expect(w.findAll(".choice")[0].text()).toBe("✓ a")
    expect(w.findAll(".choice")[2].text()).toBe("☐ c")
    await done().trigger("click")
    expect(answerCard).toHaveBeenCalledWith("c1", {
      ...none,
      choices: ["a", "b"],
    })
  })

  it("sends free text when the card has no choices", async () => {
    const w = mount(ClarifyCard, { props: { card: card({ ...q }) } })
    await w.find("input").setValue("  feature/x  ")
    await w
      .findAll("button")
      .find((b) => b.text() === "Send")!
      .trigger("click")
    expect(answerCard).toHaveBeenCalledWith("c1", {
      ...none,
      text: "feature/x",
    })
  })
})

describe("SecretCard", () => {
  it("posts the value and keeps no copy", async () => {
    const w = mount(SecretCard, {
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
    const input = w.find("input")
    expect(input.attributes("type")).toBe("password")
    expect(input.attributes("autocomplete")).toBe("off")
    expect(w.find("dl.ikv").text()).toContain("publish the package")
    await input.setValue("s3cr3t-value")
    await w
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click")
    await flushPromises()
    expect(answerCard).toHaveBeenCalledWith("c1", {
      ...none,
      value: "s3cr3t-value",
    })
    expect((w.find("input").element as HTMLInputElement).value).toBe("")
    expect(JSON.stringify({ ...localStorage })).not.toContain("s3cr3t-value")
  })
})

describe("card fields and deadline", () => {
  it("shows the pin card fields from description, rule, and command", () => {
    const w = mount(ApprovalCard, {
      props: {
        card: card({
          kind: "pin",
          summary: "wrong",
          description: "/usr/bin/gh",
          rule: "sha256 3f1a9c",
          command: "gh pr list",
          answers: ["deny", "allow"],
        }),
      },
    })
    const dd = w.findAll("dl.ikv dd").map((d) => d.text())
    expect(dd).toEqual(["/usr/bin/gh", "sha256 3f1a9c", "gh pr list"])
  })

  it("shows the commands of a secret card in For", () => {
    const w = mount(SecretCard, {
      props: {
        card: card({
          kind: "secret",
          command: "twine upload\ntwine check",
          description: "x",
          reason: "r",
        }),
      },
    })
    expect(w.find("dl.ikv dd").text()).toContain("twine upload")
    expect(w.find("dl.ikv dd").text()).toContain("twine check")
    expect(w.find("dl.ikv dd").text()).not.toBe("x")
  })

  it("treats the zero time as no deadline", () => {
    const zero = "0001-01-01T00:00:00Z"
    for (const [C, k] of [
      [ApprovalCard, "approval"],
      [ClarifyCard, "clarify"],
      [SecretCard, "secret"],
    ] as const) {
      const w = mount(C, { props: { card: card({ kind: k, deadline: zero, answers: ["deny"] }) } })
      expect(w.find(".cd").exists()).toBe(false)
      expect(w.find(".countdown").exists()).toBe(false)
    }
  })

  it("sets the id card-<id> and uses no c-faint", () => {
    for (const [C, k] of [
      [ApprovalCard, "approval"],
      [ClarifyCard, "clarify"],
      [SecretCard, "secret"],
    ] as const) {
      const w = mount(C, {
        props: { card: card({ id: "x9", kind: k, child: "sub", human_only: true }) },
      })
      expect(w.find(".icard").attributes("id")).toBe("card-x9")
      expect(w.html()).not.toContain("c-faint")
    }
  })

  it("denies a secret card", async () => {
    const w = mount(SecretCard, { props: { card: card({ kind: "secret", answers: ["deny"] }) } })
    await w
      .findAll("button")
      .find((b) => b.text() === "❌ Deny")!
      .trigger("click")
    await flushPromises()
    expect(answerCard).toHaveBeenCalledWith("c1", { ...none, answer: "deny" })
  })
})

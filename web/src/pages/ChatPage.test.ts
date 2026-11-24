import { afterEach, describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { cleanup, render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h } from "vue"

vi.mock("../api/client", () => ({
  api: vi.fn(() => Promise.resolve([])),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { runCommand } from "../api/client"
import { session } from "../stores/session"
import { turn } from "../stores/turn"
import { cards } from "../stores/cards"
import { status } from "../stores/status"
import { notices } from "../stores/notices"
import ChatPage from "./ChatPage.vue"

const t1 = {
  call: "c1",
  name: "read_file",
  emoji: "📖",
  preview: "a.go",
  args: "{}",
  state: "done",
  duration_ms: 5,
  text: "x",
  cut: false,
}
const t2 = { ...t1, call: "c2", name: "patch", emoji: "🔧" }
// The list stub emits tool with t1 or t2.
const MessageList = defineComponent({
  emits: ["tool"],
  setup:
    (_, { emit }) =>
    () =>
      h("div", [
        h("button", { onClick: () => emit("tool", t1) }, "pick c1"),
        h("button", { onClick: () => emit("tool", t2) }, "pick c2"),
      ]),
})
// The drawer stub shows its props as text.
const TurnDrawer = defineComponent({
  props: ["open", "tool"],
  setup: (p) => () =>
    h("div", { "data-testid": "drawer" }, p.open ? "open " + (p.tool?.call ?? "") : "closed"),
})
const stubs = {
  MessageList,
  TurnDrawer,
  ChatHeader: true,
  Composer: true,
  PlanCard: true,
  ReasoningBlock: true,
  CostFooter: true,
  ApprovalCard: true,
  ClarifyCard: true,
  SecretCard: true,
  RecordLine: true,
}
const drawer = () => screen.getByTestId("drawer").textContent
const mk = (extra = {}) => render(ChatPage, { global: { stubs: { ...stubs, ...extra } } })
const items = (c: Element) =>
  [
    ...c.querySelectorAll(
      '[data-slot="message-scroller-content"] > [data-slot="message-scroller-item"]',
    ),
  ] as HTMLElement[]

afterEach(() => cleanup())

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  Object.assign(session, {
    id: "s1",
    title: "T",
    model: "glm-4.6",
    effort: "",
    goalLine: "",
    context: null,
    messages: [],
    lastId: 0,
  })
  turn.view = null
  cards.open = []
  cards.answered = []
  notices.list = []
  Object.assign(status, { mode: "guarded", model: "glm-4.6" })
})

const r = (id: string, kind: string, session_id: string) => ({
  id,
  kind,
  header: "✅ Approval allowed · once",
  preview: "x",
  session_id,
  at: "2026-11-23T09:00:00Z",
})

describe("ChatPage", () => {
  it("shows a record for each answered card of this session, and for a dashboard question", () => {
    session.messages = [{ id: 1, role: "user", text: "hi" } as any]
    cards.answered = [
      r("a", "approval", "s1"),
      r("b", "approval", "s2"),
      r("q", "clarify", ""),
      r("k", "secret", ""),
      r("n", "signin", ""),
      r("i", "skill", ""),
    ] as any
    const { container } = mk()
    expect(container.querySelectorAll("record-line-stub")).toHaveLength(3)
    expect(items(container).map((e) => e.dataset.messageId)).toEqual(["rec-k", "rec-q", "rec-a"])
  })

  it("shows the empty state with four suggested commands", () => {
    session.id = ""
    const { container } = mk()
    const h1 = screen.getByRole("heading", { level: 1, name: "Chat" })
    expect(h1.classList.contains("sr-only")).toBe(true)
    expect(screen.getByRole("heading", { level: 2, name: "New session" })).toBeTruthy()
    expect(container.textContent).toContain(
      "🛡 Guarded · Dashboard · glm-4.6. Ask anything, or try one of these.",
    )
    const row = screen.getByRole("button", { name: "/help" }).parentElement!
    expect(row.classList.contains("scroll-fade-x")).toBe(true)
    expect(
      within(row)
        .getAllByRole("button")
        .map((b) => b.textContent!.trim()),
    ).toEqual(["/help", "/usage today", "/model", "/jobs"])
    expect(items(container).map((e) => e.dataset.messageId)).toEqual(["empty"])
  })

  it("opens the drawer on a tool line and closes it on a second tap of the same line", async () => {
    session.messages = [{ id: 1, role: "user", text: "hi" } as any]
    const user = userEvent.setup()
    mk()
    expect(drawer()).toBe("closed")
    await user.click(screen.getByRole("button", { name: "pick c1" }))
    expect(drawer()).toBe("open c1")
    await user.click(screen.getByRole("button", { name: "pick c2" }))
    expect(drawer()).toBe("open c2")
    await user.click(screen.getByRole("button", { name: "pick c2" }))
    expect(drawer()).toBe("closed")
  })

  it("shows each open card of the chat by kind and skips sign-in cards", () => {
    session.messages = [{ id: 1, role: "user", text: "hi" } as any]
    cards.open = [
      { id: "a", kind: "approval" },
      { id: "p", kind: "pin" },
      { id: "q", kind: "clarify" },
      { id: "s", kind: "secret" },
      { id: "n", kind: "signin" },
    ] as any
    const { container } = mk()
    expect(container.querySelectorAll("approval-card-stub")).toHaveLength(2)
    expect(container.querySelectorAll("clarify-card-stub")).toHaveLength(1)
    expect(container.querySelectorAll("secret-card-stub")).toHaveLength(1)
    expect(items(container).map((e) => e.dataset.messageId)).toEqual([
      "card-a",
      "card-p",
      "card-q",
      "card-s",
    ])
  })

  it("removes the pending bubble when the send fails", async () => {
    session.messages = [{ id: 1, role: "assistant", text: "hi" } as any]
    const Composer = defineComponent({
      emits: ["sent", "failed"],
      setup:
        (_, { emit }) =>
        () =>
          h("div", [
            h("button", { onClick: () => emit("sent", { text: "hello", files: [] }) }, "send"),
            h("button", { onClick: () => emit("failed", { text: "hello", files: [] }) }, "fail"),
          ]),
    })
    const user = userEvent.setup()
    const { container } = mk({ Composer })
    const pend = () => container.querySelectorAll<HTMLElement>('[data-message-id^="pending-"]')
    await user.click(screen.getByRole("button", { name: "send" }))
    expect(pend()).toHaveLength(1)
    expect(pend()[0].dataset.scrollAnchor).toBe("true")
    expect(pend()[0].textContent).toContain("hello")
    await user.click(screen.getByRole("button", { name: "fail" }))
    expect(pend()).toHaveLength(0)
    await user.click(screen.getByRole("button", { name: "send" }))
    expect(pend()).toHaveLength(1)
  })

  it("shows the buttons of a suggested command reply and runs one", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r",
      text: "Pick a provider.",
      buttons: [
        [
          { text: "primary", data: "/model primary" },
          { text: "fallback", data: "/model fallback" },
          { text: "local", data: "/model local" },
        ],
      ],
      edit: false,
    } as any)
    vi.mocked(runCommand).mockResolvedValueOnce({
      request_id: "r2",
      text: "Done.",
      buttons: [],
      edit: false,
    } as any)
    const user = userEvent.setup({ pointerEventsCheck: 0 })
    mk()
    await user.click(screen.getByRole("button", { name: "/model" }))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/model")
    const pop = screen.getByRole("dialog")
    expect(pop.textContent).toContain("Pick a provider.")
    await user.click(within(pop).getByRole("button", { name: "primary" }))
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/model primary")
    expect(screen.queryByRole("dialog")).toBeNull()
  })

  it("puts the notices, the plan, the records, and the cards in the scroller as items", () => {
    session.messages = [{ id: 1, role: "user", text: "hi" } as any]
    notices.list = [{ text: "🔔 The gateway started.", at: "2026-11-23T09:00:00Z" }] as any
    turn.view = {
      running: true,
      text: "",
      tools: [],
      plan: [{ text: "Read the code", state: "pending", parent: -1 }],
      queue: [],
    } as any
    cards.answered = [r("a", "approval", "s1")] as any
    cards.open = [{ id: "c", kind: "approval" }] as any
    const { container } = mk()
    const rows = items(container)
    expect(rows.map((e) => e.dataset.messageId)).toEqual(["notice-0", "plan", "rec-a", "card-c"])
    expect(rows.every((e) => e.dataset.scrollAnchor === "false")).toBe(true)
    expect(rows[0].textContent).toContain("🔔 The gateway started.")
  })
})

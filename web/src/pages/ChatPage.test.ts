import { describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises, mount } from "@vue/test-utils"
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
        h("button", { class: "pick1", onClick: () => emit("tool", t1) }),
        h("button", { class: "pick2", onClick: () => emit("tool", t2) }),
      ]),
})
const stubs = {
  MessageList,
  ChatHeader: true,
  Composer: true,
  QueueStrip: true,
  PlanCard: true,
  ToolResult: true,
  ReasoningBlock: true,
  CostFooter: true,
  ApprovalCard: true,
  ClarifyCard: true,
  SecretCard: true,
  RecordLine: true,
}
const mk = () => mount(ChatPage, { global: { stubs }, attachTo: document.body })

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
    const w = mk()
    expect(w.findAll("record-line-stub")).toHaveLength(3)
    w.unmount()
  })

  it("shows the empty state with four suggested commands", () => {
    session.id = ""
    const w = mk()
    expect(w.find("h1.sr").text()).toBe("Chat")
    expect(w.find(".empty-chat h2").text()).toBe("New session")
    expect(w.text()).toContain(
      "🛡 Guarded · Dashboard · glm-4.6. Ask anything, or try one of these.",
    )
    expect(w.findAll(".empty-chat .chip").map((b) => b.text())).toEqual([
      "/help",
      "/usage today",
      "/model",
      "/jobs",
    ])
    w.unmount()
  })

  it("opens the drawer on a tool line and closes it on a second tap of the same line", async () => {
    session.messages = [{ id: 1, role: "user", text: "hi" } as any]
    const w = mk()
    const aside = () => w.find("aside.aside")
    await w.find(".pick1").trigger("click")
    expect(aside().classes()).toContain("is-open")
    expect(aside().text()).toContain("c1")
    await w.find(".pick2").trigger("click")
    expect(aside().text()).toContain("c2")
    await w.find(".pick2").trigger("click")
    expect(aside().classes()).not.toContain("is-open")
    w.unmount()
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
    const w = mk()
    expect(w.findAll("approval-card-stub")).toHaveLength(2)
    expect(w.findAll("clarify-card-stub")).toHaveLength(1)
    expect(w.findAll("secret-card-stub")).toHaveLength(1)
    w.unmount()
  })
  it("removes the pending bubble when the send fails", async () => {
    session.messages = [{ id: 1, role: "assistant", text: "hi" } as any]
    const Composer = defineComponent({
      emits: ["sent", "failed"],
      setup:
        (_, { emit }) =>
        () =>
          h("div", [
            h("button", {
              class: "send",
              onClick: () => emit("sent", { text: "hello", files: [] }),
            }),
            h("button", {
              class: "fail",
              onClick: () => emit("failed", { text: "hello", files: [] }),
            }),
          ]),
    })
    const w = mount(ChatPage, {
      global: { stubs: { ...stubs, Composer } },
      attachTo: document.body,
    })
    await w.find(".send").trigger("click")
    expect(w.findAll(".is-pending")).toHaveLength(1)
    await w.find(".fail").trigger("click")
    expect(w.findAll(".is-pending")).toHaveLength(0)
    await w.find(".send").trigger("click")
    expect(w.findAll(".is-pending")).toHaveLength(1)
    w.unmount()
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
    const w = mk()
    await w.findAll(".empty-chat .chip")[2].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/model")
    const b = w.findAll(".reply-pop button").find((x) => x.text() === "primary")!
    await b.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/model primary")
    expect(w.find(".reply-pop").exists()).toBe(false)
    w.unmount()
  })
})

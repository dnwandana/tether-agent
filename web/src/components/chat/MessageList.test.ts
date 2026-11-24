import { beforeEach, describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { defineComponent, h, nextTick, type Slots } from "vue"

vi.mock("../../api/client", () => ({ api: vi.fn(async () => []) }))

import {
  MessageScroller,
  MessageScrollerContent,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from "@/components/ui/message-scroller"
import MessageList from "./MessageList.vue"
import { session } from "../../stores/session"
import { turn } from "../../stores/turn"
import { api } from "../../api/client"

const tool = {
  call: "c1",
  name: "patch",
  emoji: "🔧",
  preview: "internal/cron/jobs.go",
  args: "{}",
  state: "done",
  duration_ms: 100,
  text: "patched internal/cron/jobs.go (1 replacement)",
  cut: false,
}
const msg = (id: number, role: string, text: string, over = {}) => ({
  id,
  role,
  text,
  reasoning: "",
  reasoning_tokens: 0,
  model: "glm-4.6",
  provider: "zai",
  in: 0,
  out: 0,
  cached: 0,
  latency_ms: 0,
  first_ms: 0,
  cost_usd: 0,
  unpriced: false,
  answered_by: "",
  compacted: false,
  files: [],
  tools: [],
  ...over,
})

// Renders the list where the chat page puts it: inside the message scroller.
const Host = defineComponent({
  props: { selected: String },
  emits: ["tool"],
  setup(props, { emit, slots }) {
    return () =>
      h(MessageScrollerProvider, null, () =>
        h(MessageScroller, null, () =>
          h(MessageScrollerViewport, null, () =>
            h(MessageScrollerContent, null, () =>
              h(
                MessageList,
                { selected: props.selected, onTool: (t: unknown) => emit("tool", t) },
                slots as Slots,
              ),
            ),
          ),
        ),
      )
  },
})
const content = () => document.querySelector('[data-slot="message-scroller-content"]')!
const items = () =>
  Array.from(content().children).filter((c) => !c.hasAttribute("data-message-scroller-spacer"))
const userRows = () =>
  Array.from(document.querySelectorAll('[data-slot="message"][data-align="end"]'))

beforeEach(() => {
  vi.mocked(api).mockClear()
  session.id = "s1"
  session.hasOlder = false
  session.messages = [
    msg(1, "user", "Fix the test <b>now</b>"),
    msg(2, "tool", "raw tool row"),
    msg(3, "assistant", "Done **now**", {
      tools: [tool],
      answered_by: "backup",
      files: [
        { id: "f1", name: "shot.png", kind: "image" },
        { id: "f2", name: "x.html", kind: "blocked" },
      ],
    }),
  ] as never
  session.lastId = 3
  turn.view = null
})

describe("MessageList", () => {
  it("renders only message scroller items at its root", () => {
    render(Host)
    expect(items().map((i) => i.getAttribute("data-slot"))).toEqual([
      "message-scroller-item",
      "message-scroller-item",
    ])
    expect(items().map((i) => i.getAttribute("data-message-id"))).toEqual(["1", "3"])
    expect(items()[0].getAttribute("data-scroll-anchor")).toBe("true")
    expect(items()[1].getAttribute("data-scroll-anchor")).toBe("false")
  })

  it("anchors the message that starts a turn, but not a later message of the same turn", async () => {
    render(Host)
    // The turn runs before its own user message loads: the page loads the saved message at the
    // first live event of the turn.
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    await nextTick()
    session.messages = [...session.messages, msg(4, "user", "Fix the docs.")] as never
    await nextTick()
    const row = (id: string) => items().find((i) => i.getAttribute("data-message-id") === id)!
    expect(row("4").getAttribute("data-scroll-anchor")).toBe("true")
    session.messages = [...session.messages, msg(5, "user", "Also check the links.")] as never
    await nextTick()
    expect(row("5").getAttribute("data-scroll-anchor")).toBe("false")
    turn.view = null
    await nextTick()
    expect(row("5").getAttribute("data-scroll-anchor")).toBe("false")
    // The next turn has a new first message.
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    await nextTick()
    session.messages = [...session.messages, msg(6, "user", "Next task.")] as never
    await nextTick()
    expect(row("6").getAttribute("data-scroll-anchor")).toBe("true")
    expect(items()[0].getAttribute("data-scroll-anchor")).toBe("true")
  })

  it("anchors the first message of a new session that mounts the list at the turn start", async () => {
    session.messages = [] as never
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    render(Host)
    session.messages = [msg(1, "user", "Hello.")] as never
    await nextTick()
    const first = items().find((i) => i.getAttribute("data-message-id") === "1")!
    expect(first.getAttribute("data-scroll-anchor")).toBe("true")
  })

  it("does not anchor a new user message when the page opens during a turn", async () => {
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    render(Host)
    session.messages = [...session.messages, msg(4, "user", "Also check the docs.")] as never
    await nextTick()
    const steer = items().find((i) => i.getAttribute("data-message-id") === "4")!
    expect(steer.getAttribute("data-scroll-anchor")).toBe("false")
  })

  it("shows the saved messages", () => {
    const r = render(Host)
    const bubble = screen.getByText("Fix the test <b>now</b>")
    expect(bubble.closest('[data-slot="bubble"]')!.getAttribute("data-variant")).toBe("muted")
    expect(bubble.closest('[data-slot="message"]')!.getAttribute("data-align")).toBe("end")
    const answer = r.container.querySelector(".typeset.typeset-chat")!
    expect(answer.innerHTML).toContain("<strong>now</strong>")
    expect(answer.closest('[data-slot="message-content"]')).not.toBeNull()
    expect(r.container.textContent).not.toContain("raw tool row")
    expect(screen.getByText("answered by backup").getAttribute("data-slot")).toBe("badge")
    expect(screen.getAllByRole("button", { name: /patch/ })).toHaveLength(1)
    const img = screen.getByRole("img", { name: "shot.png" })
    expect(img.getAttribute("src")).toBe("/api/files/f1")
    expect(img.closest('[data-slot="attachment"]')).not.toBeNull()
    expect(
      screen
        .getByText("[blocked file]")
        .closest('[data-slot="attachment"]')!
        .getAttribute("data-state"),
    ).toBe("error")
  })

  it("emits tool and marks the selected line", async () => {
    const r = render(Host, { props: { selected: "c1" } })
    const line = screen.getByRole("button", { name: /patch/ })
    expect(line.getAttribute("aria-expanded")).toBe("true")
    await userEvent.setup().click(line)
    expect(r.emitted().tool).toEqual([[tool]])
  })

  it("fills the slots", () => {
    render(Host, {
      slots: {
        reasoning: ({ message }: { message: { id: number } }) =>
          h("i", { "data-testid": "rs" }, String(message.id)),
        footer: ({ message }: { message: { id: number } }) =>
          h("i", { "data-testid": "ft" }, String(message.id)),
      },
    })
    expect(screen.getByTestId("rs").textContent).toBe("3")
    expect(screen.getByTestId("ft").textContent).toBe("3")
  })

  it("shows the live turn", async () => {
    turn.view = {
      running: true,
      text: "Work**ing**",
      tools: [{ ...tool, call: "c9", state: "running", text: "" }],
      plan: [],
      queue: [],
    } as never
    render(Host, { slots: { live: () => h("p", { "data-testid": "live-slot" }, "plan") } })
    expect(screen.getAllByRole("button", { name: /patch/ })).toHaveLength(2)
    expect(screen.getByText("▉")).toBeTruthy()
    expect(screen.getByTestId("live-slot")).toBeTruthy()
    expect(items().at(-1)!.getAttribute("data-message-id")).toBe("live")
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    await nextTick()
    expect(screen.getByText("working")).toBeTruthy()
  })

  it("draws the newest 150 messages and loads older ones", async () => {
    session.messages = Array.from({ length: 160 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    ) as never
    session.hasOlder = true
    render(Host)
    const user = userEvent.setup()
    expect(userRows()).toHaveLength(75)
    expect(items()[0].getAttribute("data-message-id")).toBe("older")
    await user.click(screen.getByRole("button", { name: "Load older messages" }))
    expect(userRows()).toHaveLength(80)
    await user.click(screen.getByRole("button", { name: "Load older messages" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?before=1&limit=50")
  })

  it("loads older with the smallest positive id", async () => {
    session.messages = [
      msg(-1, "summary", "sum", { compacted: true }),
      ...Array.from({ length: 50 }, (_, i) =>
        msg(i + 10, i % 2 ? "assistant" : "user", "m" + (i + 10)),
      ),
    ] as never
    session.hasOlder = true
    render(Host)
    await userEvent.setup().click(screen.getByRole("button", { name: "Load older messages" }))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?before=10&limit=50")
  })

  // A page with tool calls merges to fewer than 50 rows. The server flag decides, not the count.
  it("offers older messages from the server flag, not from the row count", async () => {
    session.messages = Array.from({ length: 20 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    ) as never
    session.hasOlder = true
    render(Host)
    expect(screen.queryByRole("button", { name: "Load older messages" })).not.toBeNull()
    session.messages = Array.from({ length: 60 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    ) as never
    session.hasOlder = false
    await nextTick()
    expect(screen.queryByRole("button", { name: "Load older messages" })).toBeNull()
  })

  it("shows a closed summary block only for the summary role", async () => {
    session.messages = [
      msg(1, "user", "old question", { compacted: true }),
      msg(2, "assistant", "old answer", { compacted: true }),
      msg(-1, "summary", "The short summary", { compacted: true }),
      msg(3, "user", "new"),
    ] as never
    const r = render(Host)
    const folds = screen.getAllByRole("button", { name: "🗜 Compacted summary" })
    expect(folds).toHaveLength(1)
    expect(folds[0].getAttribute("aria-expanded")).toBe("false")
    expect(folds[0].closest('[data-slot="marker"]')!.getAttribute("data-variant")).toBe("separator")
    expect(screen.queryByText("The short summary")).toBeNull()
    await userEvent.setup().click(folds[0])
    expect(screen.getByText("The short summary")).toBeTruthy()
    expect(userRows().map((m) => m.textContent!.trim())).toEqual(["old question", "new"])
    expect(r.container.querySelector(".typeset")!.textContent).toContain("old answer")
  })

  it("dims the compacted rows", () => {
    session.messages = [
      msg(1, "user", "old question", { compacted: true }),
      msg(2, "assistant", "old answer", { compacted: true }),
      msg(3, "user", "new"),
    ] as never
    render(Host)
    const rows = Array.from(document.querySelectorAll('[data-slot="message"]'))
    expect(rows.map((m) => m.classList.contains("opacity-60"))).toEqual([true, true, false])
  })
})

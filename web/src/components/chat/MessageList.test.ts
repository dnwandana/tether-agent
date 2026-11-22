import { flushPromises, mount } from "@vue/test-utils"
import { beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("../../api/client", () => ({ api: vi.fn(async () => []) }))

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
  ]
  session.lastId = 3
  turn.view = null
})

describe("MessageList", () => {
  it("shows the saved messages", () => {
    const w = mount(MessageList)
    expect(w.find(".msg-user .bubble-user").text()).toBe("Fix the test <b>now</b>")
    expect(w.find(".answer").html()).toContain("<strong>now</strong>")
    expect(w.text()).not.toContain("raw tool row")
    expect(w.text()).toContain("answered by backup")
    expect(w.findAll(".toolline")).toHaveLength(1)
    expect(w.find("img.shot").attributes("src")).toBe("/api/files/f1")
    expect(w.text()).toContain("[blocked file]")
  })

  it("emits tool and marks the selected line", async () => {
    const w = mount(MessageList, { props: { selected: "c1" } })
    expect(w.find(".toolline").classes()).toContain("is-selected")
    await w.find(".toolline").trigger("click")
    expect(w.emitted("tool")).toEqual([[tool]])
  })

  it("fills the slots", () => {
    const w = mount(MessageList, {
      slots: {
        reasoning:
          '<template #reasoning="{ message }"><i class="rs">{{ message.id }}</i></template>',
        footer: '<template #footer="{ message }"><i class="ft">{{ message.id }}</i></template>',
      },
    })
    expect(w.find(".rs").text()).toBe("3")
    expect(w.find(".ft").text()).toBe("3")
  })

  it("shows the live turn", async () => {
    turn.view = {
      running: true,
      text: "Work**ing**",
      tools: [{ ...tool, call: "c9", state: "running", text: "" }],
      plan: [],
      queue: [],
    } as never
    const w = mount(MessageList, { slots: { live: '<p class="live-slot">plan</p>' } })
    expect(w.findAll(".toolline")).toHaveLength(2)
    expect(w.find(".cursor").exists()).toBe(true)
    expect(w.find(".live-slot").exists()).toBe(true)
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    await w.vm.$nextTick()
    expect(w.find(".working").text()).toContain("working")
  })

  it("draws the newest 150 messages and loads older ones", async () => {
    session.messages = Array.from({ length: 160 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    )
    session.hasOlder = true
    const w = mount(MessageList)
    expect(w.findAll(".msg-user")).toHaveLength(75)
    await w.find('[data-act="older"]').trigger("click")
    expect(w.findAll(".msg-user")).toHaveLength(80)
    await w.find('[data-act="older"]').trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?before=1&limit=50")
  })
  it("loads older with the smallest positive id", async () => {
    session.messages = [
      msg(-1, "summary", "sum", { compacted: true }),
      ...Array.from({ length: 50 }, (_, i) =>
        msg(i + 10, i % 2 ? "assistant" : "user", "m" + (i + 10)),
      ),
    ]
    session.hasOlder = true
    const w = mount(MessageList)
    await w.find('[data-act="older"]').trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s1/messages?before=10&limit=50")
  })

  // A page with tool calls merges to fewer than 50 rows. The server flag decides, not the count.
  it("offers older messages from the server flag, not from the row count", async () => {
    session.messages = Array.from({ length: 20 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    )
    session.hasOlder = true
    const w = mount(MessageList)
    expect(w.find('[data-act="older"]').exists()).toBe(true)
    session.messages = Array.from({ length: 60 }, (_, i) =>
      msg(i + 1, i % 2 ? "assistant" : "user", "m" + (i + 1)),
    )
    session.hasOlder = false
    await w.vm.$nextTick()
    expect(w.find('[data-act="older"]').exists()).toBe(false)
  })

  it("shows a closed summary block only for the summary role", () => {
    session.messages = [
      msg(1, "user", "old question", { compacted: true }),
      msg(2, "assistant", "old answer", { compacted: true }),
      msg(-1, "summary", "The short summary", { compacted: true }),
      msg(3, "user", "new"),
    ]
    const w = mount(MessageList)
    const d = w.findAll("details")
    expect(d).toHaveLength(1)
    expect(d[0].attributes("open")).toBeUndefined()
    expect(d[0].find("summary").text()).toBe("🗜 Compacted summary")
    expect(d[0].text()).toContain("The short summary")
    expect(w.findAll(".msg-user").map((m) => m.text())).toEqual(["old question", "new"])
    expect(w.find(".answer").text()).toContain("old answer")
  })
})

import { mount, RouterLinkStub } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import ToolResult from "./ToolResult.vue"
import DelegateCards from "./DelegateCards.vue"

const tool = (name: string, text: string, args = "{}") => ({
  call: "c1",
  name,
  emoji: "",
  preview: "",
  args,
  state: "done",
  duration_ms: 100,
  text,
  cut: false,
})
const opts = { global: { stubs: { RouterLink: RouterLinkStub } } }

describe("tool views", () => {
  it("shows read_file as code with line numbers", () => {
    const w = mount(ToolResult, {
      props: {
        tool: tool(
          "read_file",
          "   310\tfunc Runs() {\n   311\t\treturn nil\n[more lines follow; next_offset: 312]\n",
          '{"path":"internal/cron/jobs.go"}',
        ),
      },
    })
    expect(w.find(".result-h .path").text()).toBe("internal/cron/jobs.go")
    expect(w.find(".result-h").text()).toContain("lines 310–311")
    expect(w.find(".code .ln").text()).toBe("310\n311")
    expect(w.find(".code .src").text()).toContain("func Runs() {")
    expect(w.text()).toContain("More lines follow from line 312.")
  })

  it("shows web_search as a list with safe links", () => {
    const text =
      "1. Good\n   https://example.com/a\n   A snippet.\n\n2. Bad\n   javascript:alert(1)\n"
    const w = mount(ToolResult, {
      props: { tool: tool("web_search", text, '{"query":"changelog format"}') },
    })
    expect(w.find(".result-h").text()).toContain("changelog format")
    expect(w.find(".result-h").text()).toContain("2 results")
    const links = w.findAll(".list a")
    expect(links.map((a) => a.attributes("href"))).toEqual(["https://example.com/a"])
    expect(links[0].attributes("rel")).toBe("noopener noreferrer")
    expect(w.text()).toContain("javascript:alert(1)")
  })

  it("shows session_search hits with a link to the session", () => {
    const text =
      '1 hits for "flaky", newest first:\nsession s1 · Fix the cron test · Nov 19 · msg 42: the runs test is flaky'
    const w = mount(ToolResult, {
      props: { tool: tool("session_search", text, '{"query":"flaky"}') },
      ...opts,
    })
    const link = w.findComponent(RouterLinkStub)
    expect(link.props("to")).toEqual({
      name: "transcript",
      params: { id: "s1" },
      query: { message: "42" },
    })
    expect(w.text()).toContain("Fix the cron test")
    expect(w.text()).toContain("the runs test is flaky")
  })

  it("shows one card for each child", () => {
    const text =
      '{"results":[{"task":1,"status":"ok","summary":"Grouped 12 commits.","tool_calls":9,"duration_s":21},{"task":2,"status":"error","summary":"","tool_calls":2,"duration_s":3.5,"error":"timeout"}]}'
    const args =
      '{"tasks":[{"goal":"Group the commits by area"},{"goal":"Find the PR of each commit"}]}'
    const w = mount(ToolResult, { props: { tool: tool("delegate_task", text, args) } })
    const cards = w.findAll(".child-cards .child")
    expect(cards).toHaveLength(2)
    expect(cards[0].text()).toContain("Group the commits by area")
    expect(cards[0].text()).toContain("Grouped 12 commits.")
    expect(cards[0].find(".meta").text()).toBe("9 tool calls · 21 s")
    expect(cards[0].find(".chip-ok").text()).toBe("ok")
    expect(cards[1].find(".chip-bad").text()).toBe("error")
    expect(cards[1].text()).toContain("timeout")
  })

  it("rounds the duration of a child", () => {
    const text =
      '{"results":[{"task":1,"status":"ok","summary":"","tool_calls":1,"duration_s":125.4},{"task":2,"status":"ok","summary":"","tool_calls":2,"duration_s":3.5}]}'
    const w = mount(ToolResult, { props: { tool: tool("delegate_task", text, "{}") } })
    const meta = w.findAll(".child-cards .child .meta").map((m) => m.text())
    expect(meta).toEqual(["1 tool call · 2 m 5 s", "2 tool calls · 4 s"])
  })

  it("shows a background delegation", () => {
    const w = mount(DelegateCards, {
      props: { text: '{"batch_id":"b7","children":["changelog-diff","pr-links"]}', args: "{}" },
    })
    expect(w.text()).toContain("Started batch b7 with 2 children: changelog-diff, pr-links")
  })

  it("falls back to the plain text on a parse failure", () => {
    const w = mount(ToolResult, {
      props: { tool: tool("delegate_task", "cannot start the background delegation") },
    })
    expect(w.find("pre.code-block").text()).toBe("cannot start the background delegation")
  })
})

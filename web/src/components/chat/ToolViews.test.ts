import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import { flushPromises } from "@vue/test-utils"
import { createMemoryHistory, createRouter } from "vue-router"
import ToolResult from "./ToolResult.vue"

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
const Blank = { render: () => null }
async function mk(t: ReturnType<typeof tool>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/")
  const r = render(ToolResult, { props: { tool: t }, global: { plugins: [router] } })
  await flushPromises()
  return r
}

describe("tool views", () => {
  it("shows read_file as code with line numbers", async () => {
    const r = await mk(
      tool(
        "read_file",
        "   310\tfunc Runs() {\n   311\t\treturn nil\n[more lines follow; next_offset: 312]\n",
        '{"path":"internal/cron/jobs.go"}',
      ),
    )
    const head = r.container.querySelector('[data-slot="code-block-header"]')!
    expect(head.textContent).toContain("internal/cron/jobs.go")
    expect(head.textContent).toContain("lines 310–311")
    expect(r.container.querySelector("[data-numbered]")).not.toBeNull()
    expect(r.container.textContent).toContain("func Runs() {")
    expect(r.container.textContent).toContain("More lines follow from line 312.")
  })

  it("shows web_search as a list with safe links", async () => {
    const text =
      "1. Good\n   https://example.com/a\n   A snippet.\n\n2. Bad\n   javascript:alert(1)\n"
    const r = await mk(tool("web_search", text, '{"query":"changelog format"}'))
    expect(screen.getByText("changelog format")).toBeTruthy()
    expect(screen.getByText("2 results")).toBeTruthy()
    const links = screen.getAllByRole("link")
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://example.com/a"])
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer")
    expect(r.container.textContent).toContain("javascript:alert(1)")
  })

  it("shows session_search hits with a link to the session", async () => {
    const text =
      '1 hits for "flaky", newest first:\nsession s1 · Fix the cron test · Nov 19 · msg 42: the runs test is flaky'
    const r = await mk(tool("session_search", text, '{"query":"flaky"}'))
    const link = screen.getByRole("link", { name: /Fix the cron test/ })
    expect(link.getAttribute("href")).toBe("/sessions/s1?message=42")
    expect(r.container.textContent).toContain("Fix the cron test")
    expect(r.container.textContent).toContain("the runs test is flaky")
  })

  it("shows one card for each child", async () => {
    const text =
      '{"results":[{"task":1,"status":"ok","summary":"Grouped 12 changes.","tool_calls":9,"duration_s":21},{"task":2,"status":"error","summary":"","tool_calls":2,"duration_s":3.5,"error":"timeout"}]}'
    const args =
      '{"tasks":[{"goal":"Group the changes by area"},{"goal":"Find the PR of each change"}]}'
    await mk(tool("delegate_task", text, args))
    expect(screen.getByText("Group the changes by area")).toBeTruthy()
    expect(screen.getByText("Find the PR of each change")).toBeTruthy()
    expect(screen.getByText("9 tool calls · 21 s")).toBeTruthy()
    expect(screen.getByText("ok")).toBeTruthy()
    expect(screen.getByText("error")).toBeTruthy()
    expect(screen.getByText("timeout")).toBeTruthy()
  })

  it("falls back to the plain text on a parse failure", async () => {
    const r = await mk(tool("delegate_task", "cannot start the background delegation"))
    const body = r.container.querySelector('[data-slot="code-block-content"]')!
    expect(body.textContent!.trim()).toBe("cannot start the background delegation")
  })
})

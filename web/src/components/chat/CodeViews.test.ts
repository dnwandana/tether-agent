import { afterEach, describe, expect, it, vi } from "vitest"
import { render, screen } from "@testing-library/vue"
import CodeView from "./CodeView.vue"
import DiffView from "./DiffView.vue"
import TerminalView from "./TerminalView.vue"

const block = (c: Element) => c.querySelector('[data-slot="code-block"]')!
const head = (c: Element) => c.querySelector('[data-slot="code-block-header"]')!
const body = (c: Element) => c.querySelector('[data-slot="code-block-content"]') as HTMLElement
const foot = (c: Element) => c.querySelector('[data-slot="code-block-footer"]')!

const col = (l: Element, c: string) => l.querySelector(`[data-col="${c}"]`)!.textContent!.trim()

afterEach(() => vi.unstubAllGlobals())

describe("TerminalView", () => {
  it("shows a terminal result with its exit code", () => {
    const r = render(TerminalView, {
      props: { command: "go test ./...", text: "FAIL\tcron\n[exit code: 1]" },
    })
    expect(block(r.container).getAttribute("data-size")).toBe("sm")
    expect(block(r.container).getAttribute("data-variant")).toBe("destructive")
    expect(head(r.container).textContent).toContain("$ go test ./...")
    expect(body(r.container).textContent).toContain("FAIL\tcron")
    expect(body(r.container).textContent).not.toContain("[exit code: 1]")
    expect(foot(r.container).textContent!.trim()).toBe("exit 1")
  })

  it("keeps the default look for exit 0", () => {
    const r = render(TerminalView, { props: { command: "true", text: "ok\n[exit code: 0]" } })
    expect(block(r.container).getAttribute("data-variant")).toBe("default")
    expect(foot(r.container).textContent!.trim()).toBe("exit 0")
  })

  it("shows a background start", () => {
    const r = render(TerminalView, {
      props: { command: "npm run dev", text: '{"pid":4242,"log_path":"/tmp/bg-4242.log"}' },
    })
    expect(foot(r.container).textContent).toContain("background · PID 4242")
    expect(body(r.container).textContent).toContain("/tmp/bg-4242.log")
    expect(block(r.container).getAttribute("data-variant")).toBe("default")
  })
})

describe("DiffView", () => {
  it("shows a patch as a diff", () => {
    const r = render(DiffView, {
      props: {
        text: "patched a.go (1 replacement)\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x\n-old\n+new\n",
      },
    })
    const lines = Array.from(r.container.querySelectorAll('[data-slot="code-block-line"]'))
    expect(lines.find((l) => l.hasAttribute("data-hunk"))!.textContent!.trim()).toBe(
      "@@ -1,2 +1,2 @@",
    )
    const added = lines.filter((l) => l.getAttribute("data-variant") === "added")
    const removed = lines.filter((l) => l.getAttribute("data-variant") === "removed")
    expect(added).toHaveLength(1)
    expect(removed).toHaveLength(1)
    expect([col(removed[0], "old"), col(removed[0], "new")]).toEqual(["2", ""])
    expect([col(added[0], "old"), col(added[0], "new")]).toEqual(["", "2"])
    const ctx = lines.find((l) => l.textContent!.includes("x") && !l.hasAttribute("data-hunk"))!
    expect([col(ctx, "old"), col(ctx, "new")]).toEqual(["1", "1"])
    expect(head(r.container).textContent).toContain("+1")
    expect(head(r.container).textContent).toContain("−1")
    expect(head(r.container).textContent).toContain("patched a.go (1 replacement)")
  })
})

describe("CodeView", () => {
  const text =
    "   310\tfunc Runs() {\n   311\t\treturn nil\n[more lines follow; next_offset: 312]\n"

  it("shows read_file as code with line numbers", () => {
    const r = render(CodeView, { props: { path: "internal/cron/jobs.go", text } })
    expect(head(r.container).textContent).toContain("internal/cron/jobs.go")
    expect(head(r.container).textContent).toContain("lines 310–311")
    const pre = body(r.container)
    expect(pre.hasAttribute("data-numbered")).toBe(true)
    expect(pre.style.counterReset).toBe("line 309")
    const lines = Array.from(pre.querySelectorAll('[data-slot="code-block-line"]'))
    expect(lines).toHaveLength(2)
    expect(lines[0].textContent).toContain("func Runs() {")
    expect(foot(r.container).textContent).toContain("More lines follow from line 312.")
  })
})

describe("copy button", () => {
  const cases = [
    () => render(CodeView, { props: { path: "a.go", text: "     1\tpackage a\n" } }),
    () => render(DiffView, { props: { text: "@@ -1 +1 @@\n-a\n+b\n" } }),
    () => render(TerminalView, { props: { command: "ls", text: "a\n[exit code: 0]" } }),
  ]

  it("shows Copy in a secure context", () => {
    vi.stubGlobal("isSecureContext", true)
    for (const mk of cases) {
      const r = mk()
      expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy()
      r.unmount()
    }
  })

  it("hides Copy on plain HTTP", () => {
    vi.stubGlobal("isSecureContext", false)
    for (const mk of cases) {
      const r = mk()
      expect(screen.queryByRole("button", { name: "Copy" })).toBeNull()
      r.unmount()
    }
  })
})

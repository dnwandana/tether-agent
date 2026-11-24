import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/vue"
import { flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
import { api } from "../api/client"
import FileViewer from "./FileViewer.vue"

const scroll = vi.fn()
beforeEach(() => {
  scroll.mockReset()
  Element.prototype.scrollIntoView = scroll
  vi.mocked(api).mockReset().mockResolvedValue({ text: "a\nb\nc", cut: false })
})
const lines = () => screen.getAllByRole("listitem")

describe("FileViewer", () => {
  it("reads the file through the skill file route and numbers each line", async () => {
    render(FileViewer, { props: { skill: "release-notes", path: "scripts/notes.sh", line: null } })
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/skills/release-notes/file?path=scripts%2Fnotes.sh",
    )
    expect(lines().map((l) => l.textContent)).toEqual(["a", "b", "c"])
    expect(lines()[0].getAttribute("data-n")).toBe("1")
    expect(screen.getByRole("list").classList.contains("font-mono")).toBe(true)
    expect(screen.queryByText("The file is cut at 256 KiB.")).toBeNull()
  })

  it("puts each line in its li with no leading or trailing white space", async () => {
    vi.mocked(api).mockResolvedValue({ text: "  indented\nb", cut: false })
    render(FileViewer, { props: { skill: "s", path: "a.sh", line: null } })
    await flushPromises()
    expect(lines().map((l) => l.textContent)).toEqual(["  indented", "b"])
  })

  it("marks the line of a finding and scrolls it into view", async () => {
    render(FileViewer, { props: { skill: "s", path: "SKILL.md", line: 2 } })
    await flushPromises()
    expect(lines()[1].getAttribute("data-hit")).toBe("true")
    expect(lines()[1].getAttribute("aria-current")).toBe("true")
    expect(lines()[0].getAttribute("data-hit")).toBeNull()
    expect(scroll).toHaveBeenCalledTimes(1)
  })

  it("says when the server cut the file", async () => {
    vi.mocked(api).mockResolvedValue({ text: "x", cut: true })
    render(FileViewer, { props: { skill: "s", path: "big.txt", line: null } })
    await flushPromises()
    expect(screen.getByText("The file is cut at 256 KiB.")).toBeTruthy()
  })

  it("shows the server text of a refused file", async () => {
    vi.mocked(api).mockRejectedValue(new Error("The file is not a text file."))
    const r = render(FileViewer, { props: { skill: "s", path: "logo.png", line: null } })
    await flushPromises()
    expect(r.container.textContent).toContain("Could not open the file")
    expect(r.container.textContent).toContain("The file is not a text file.")
  })

  it("loads again when the path changes", async () => {
    const r = render(FileViewer, { props: { skill: "s", path: "a.sh", line: null } })
    await flushPromises()
    await r.rerender({ skill: "s", path: "b.sh", line: null })
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/skills/s/file?path=b.sh")
  })
})

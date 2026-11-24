import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

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

describe("FileViewer", () => {
  it("reads the file through the skill file route and numbers each line", async () => {
    const w = mount(FileViewer, {
      props: { skill: "release-notes", path: "scripts/notes.sh", line: null },
    })
    await flushPromises()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/skills/release-notes/file?path=scripts%2Fnotes.sh",
    )
    expect(w.findAll(".file-view li").map((l) => l.text())).toEqual(["a", "b", "c"])
    expect(w.find(".file-view li").attributes("data-n")).toBe("1")
    expect(w.text()).not.toContain("The file is cut at 256 KiB.")
  })

  it("puts each line in its li with no leading or trailing white space", async () => {
    vi.mocked(api).mockResolvedValue({ text: "  indented\nb", cut: false })
    const w = mount(FileViewer, { props: { skill: "s", path: "a.sh", line: null } })
    await flushPromises()
    expect(w.findAll(".file-view li").map((l) => l.element.textContent)).toEqual([
      "  indented",
      "b",
    ])
  })

  it("marks the line of a finding and scrolls it into view", async () => {
    const w = mount(FileViewer, { props: { skill: "s", path: "SKILL.md", line: 2 } })
    await flushPromises()
    const hit = w.findAll(".file-view li")[1]
    expect(hit.classes()).toContain("is-hit")
    expect(scroll).toHaveBeenCalledTimes(1)
  })

  it("says when the server cut the file", async () => {
    vi.mocked(api).mockResolvedValue({ text: "x", cut: true })
    const w = mount(FileViewer, { props: { skill: "s", path: "big.txt", line: null } })
    await flushPromises()
    expect(w.text()).toContain("The file is cut at 256 KiB.")
  })

  it("shows the server text of a refused file", async () => {
    vi.mocked(api).mockRejectedValue(new Error("The file is not a text file."))
    const w = mount(FileViewer, { props: { skill: "s", path: "logo.png", line: null } })
    await flushPromises()
    expect(w.text()).toContain("Could not open the file")
    expect(w.text()).toContain("The file is not a text file.")
  })

  it("loads again when the path changes", async () => {
    const w = mount(FileViewer, { props: { skill: "s", path: "a.sh", line: null } })
    await flushPromises()
    await w.setProps({ path: "b.sh" })
    await flushPromises()
    expect(api).toHaveBeenLastCalledWith("GET", "/api/skills/s/file?path=b.sh")
  })
})

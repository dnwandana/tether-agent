import { cleanup, fireEvent, render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { reactive } from "vue"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const route = reactive({ query: {} as Record<string, string> })
const replace = vi.fn()
const secure = { value: true }
vi.mock("vue-router", () => ({ useRoute: () => route, useRouter: () => ({ replace }) }))
vi.mock("../../api/client", () => ({
  api: vi.fn(async () => ({
    commands: [
      { name: "/usage", description: "Tokens and cost" },
      { name: "/stop", description: "Stop the task" },
    ],
    skills: [{ name: "/release-notes", description: "Write release notes" }],
  })),
  postForm: vi.fn(async () => ({ ok: true })),
  runCommand: vi.fn(async () => ({ request_id: "r1" })),
}))
vi.mock("../../lib/errors", () => ({
  friendlyError: (e: unknown) => "Mapped: " + (e as Error).message,
}))
vi.mock("../../stores/ui", () => ({ isSecure: () => secure.value, toast: vi.fn() }))

import Composer from "./Composer.vue"
import { turn } from "../../stores/turn"
import { postForm, runCommand } from "../../api/client"
import { toast } from "../../stores/ui"

async function mountIt() {
  const r = render(Composer)
  await flushPromises()
  const ta = screen.getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement
  return { ...r, ta }
}
const lastForm = () => vi.mocked(postForm).mock.calls.at(-1)![1] as FormData
const chipOf = (label: string) =>
  screen.getByText(label).closest('[data-slot="attachment"]') as HTMLElement

beforeEach(() => {
  vi.mocked(postForm).mockClear()
  vi.mocked(runCommand).mockClear()
  vi.mocked(toast).mockClear()
  sessionStorage.clear()
  replace.mockClear()
  route.query = {}
  secure.value = true
  turn.view = null
  turn.acks = []
  turn.queue = []
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("Composer", () => {
  it("grows from one line to eight lines", async () => {
    const { ta } = await mountIt()
    Object.defineProperty(ta, "scrollHeight", { configurable: true, get: () => 400 })
    await fireEvent.update(ta, "1\n2\n3\n4\n5\n6\n7\n8\n9\n10")
    await flushPromises()
    expect(ta.style.height).toBe("194px")
    Object.defineProperty(ta, "scrollHeight", { configurable: true, get: () => 44 })
    await fireEvent.update(ta, "1")
    await flushPromises()
    expect(ta.style.height).toBe("44px")
  })

  it("sends a message with Enter, and not with Shift+Enter", async () => {
    const user = userEvent.setup()
    const { ta, emitted } = await mountIt()
    await fireEvent.update(ta, "hello")
    ta.focus()
    await user.keyboard("{Shift>}{Enter}{/Shift}")
    expect(postForm).not.toHaveBeenCalled()
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(vi.mocked(postForm).mock.calls[0][0]).toBe("/api/messages")
    expect(lastForm().get("text")).toBe("hello")
    expect(lastForm().get("client_id")).toMatch(/^[0-9a-f]{32}$/)
    expect([...lastForm().keys()][0]).toBe("client_id")
    expect(ta.value).toBe("")
    expect(emitted().sent).toEqual([[{ text: "hello", files: [] }]])
  })

  it("shows a send error above the composer with Send again, and keeps the draft", async () => {
    vi.mocked(postForm).mockRejectedValueOnce(new Error("The gateway is stopping."))
    const user = userEvent.setup()
    const { ta, emitted } = await mountIt()
    await fireEvent.update(ta, "hello")
    ta.focus()
    await user.keyboard("{Enter}")
    await flushPromises()
    const line = screen.getByRole("alert")
    expect(line.textContent).toContain("Mapped: The gateway is stopping.")
    expect(toast).not.toHaveBeenCalled()
    expect(ta.value).toBe("hello")
    expect(emitted().failed).toEqual([[{ text: "hello", files: [] }]])
    const first = lastForm().get("client_id")
    await user.click(within(line).getByRole("button", { name: "Send again" }))
    await flushPromises()
    expect(lastForm().get("client_id")).toBe(first)
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("shows a voice error on the same line", async () => {
    vi.stubGlobal("navigator", {
      ...navigator,
      mediaDevices: {
        getUserMedia: vi.fn(async () => {
          throw Object.assign(new Error("denied"), { name: "NotAllowedError" })
        }),
      },
    })
    await mountIt()
    await fireEvent.pointerDown(screen.getByRole("button", { name: "Hold to record" }))
    await flushPromises()
    const line = screen.getByRole("alert")
    expect(line.textContent).toContain("Mapped: denied")
    expect(within(line).queryByRole("button")).toBeNull()
    expect(toast).not.toHaveBeenCalled()
  })

  it("keeps the draft in sessionStorage over a reload", async () => {
    const user = userEvent.setup()
    const a = await mountIt()
    await fireEvent.update(a.ta, "half a thought")
    expect(sessionStorage.getItem("tether.draft")).toBe("half a thought")
    a.unmount()
    const b = await mountIt()
    expect(b.ta.value).toBe("half a thought")
    b.ta.focus()
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(sessionStorage.getItem("tether.draft")).toBeNull()
  })

  it("runs a built-in command and sends a skill as a message", async () => {
    const user = userEvent.setup()
    const { ta, emitted } = await mountIt()
    await fireEvent.update(ta, "/usage today")
    ta.focus()
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/usage today")
    expect(emitted().reply).toEqual([[{ request_id: "r1" }]])
    await fireEvent.update(ta, "/release-notes v0.24")
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(lastForm().get("text")).toBe("/release-notes v0.24")
  })

  it("completes a command from the slash menu", async () => {
    const user = userEvent.setup()
    const { ta } = await mountIt()
    await fireEvent.update(ta, "/us")
    const list = screen.getByRole("listbox")
    const rows = within(list).getAllByRole("option")
    expect(rows.map((o) => o.firstElementChild!.textContent)).toEqual(["/usage"])
    expect(rows[0].getAttribute("aria-selected")).toBe("true")
    ta.focus()
    await user.keyboard("{Enter}")
    expect(ta.value).toBe("/usage ")
    expect(runCommand).not.toHaveBeenCalled()
    expect(screen.queryByRole("listbox")).toBeNull()
  })

  it("shows Stop while a turn runs and the field is empty", async () => {
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    const user = userEvent.setup()
    const { ta } = await mountIt()
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull()
    await user.click(screen.getByRole("button", { name: "Stop the task" }))
    expect(runCommand).toHaveBeenCalledWith("/stop")
    await fireEvent.update(ta, "also check the docs")
    expect(screen.getByRole("button", { name: "Send" }).hasAttribute("disabled")).toBe(false)
  })

  it("turns Send off with nothing to send", async () => {
    await mountIt()
    expect(screen.getByRole("button", { name: "Send" }).hasAttribute("disabled")).toBe(true)
  })

  it("shows one HTTPS note on plain HTTP and none on HTTPS", async () => {
    secure.value = false
    const a = await mountIt()
    expect(screen.queryByRole("button", { name: "Hold to record" })).toBeNull()
    expect(screen.getAllByText(/need HTTPS or localhost/)).toHaveLength(1)
    a.unmount()
    secure.value = true
    await mountIt()
    expect(screen.getByRole("button", { name: "Hold to record" })).toBeTruthy()
    expect(screen.queryByText(/need HTTPS or localhost/)).toBeNull()
  })

  it("attaches files and sends them", async () => {
    const user = userEvent.setup()
    const { container, ta, emitted } = await mountIt()
    const input = container.querySelector('input[type="file"][multiple]') as HTMLInputElement
    await user.upload(input, new File(["# notes"], "notes.md", { type: "text/markdown" }))
    expect(chipOf("📎 notes.md")).toBeTruthy()
    expect(screen.getByRole("button", { name: "Remove notes.md" })).toBeTruthy()
    ta.focus()
    await user.keyboard("{Enter}")
    await flushPromises()
    expect((lastForm().getAll("files") as File[]).map((f) => f.name)).toEqual(["notes.md"])
    expect(emitted().sent).toEqual([[{ text: "", files: ["notes.md"] }]])
  })

  it("marks the chips as uploading during a send and as error after a failed send", async () => {
    let fail!: (e: Error) => void
    vi.mocked(postForm).mockImplementationOnce(() => new Promise((_, reject) => (fail = reject)))
    const user = userEvent.setup()
    const { container, ta } = await mountIt()
    const input = container.querySelector('input[type="file"][multiple]') as HTMLInputElement
    await user.upload(input, new File(["x"], "a.txt", { type: "text/plain" }))
    ta.focus()
    await user.keyboard("{Enter}")
    await flushPromises()
    expect(chipOf("📎 a.txt").dataset.state).toBe("uploading")
    fail(new Error("offline"))
    await flushPromises()
    expect(chipOf("📎 a.txt").dataset.state).toBe("error")
    expect(screen.getByRole("button", { name: "Send again" })).toBeTruthy()
  })

  it("shows the live acks and the queue, and runs the queue now", async () => {
    turn.acks = [
      { id: "a1", kind: "steer", text: "↪️ Got it.", request_id: "", buttons: [] },
      { id: "a2", kind: "reply", text: "⏸ Paused.", request_id: "r1", buttons: [] },
    ] as never
    turn.queue = [{ kind: "skill", preview: "/release-notes" }]
    const user = userEvent.setup()
    await mountIt()
    expect(screen.getByText("↪️ Got it.")).toBeTruthy()
    expect(screen.queryByText("⏸ Paused.")).toBeNull()
    await user.click(screen.getByRole("button", { name: /Do this now/ }))
    expect(runCommand).toHaveBeenCalledWith("/now")
  })

  it("sets --dock-h from the ResizeObserver", async () => {
    let cb: ResizeObserverCallback | undefined
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(f: ResizeObserverCallback) {
          cb = f
        }
        observe() {}
        disconnect() {}
      },
    )
    const { container, emitted } = await mountIt()
    cb!(
      [
        {
          borderBoxSize: [{ blockSize: 132, inlineSize: 600 }],
          contentRect: { height: 100 },
        } as unknown as ResizeObserverEntry,
      ],
      {} as ResizeObserver,
    )
    expect(container.style.getPropertyValue("--dock-h")).toBe("132px")
    expect(emitted().resize).toEqual([[132]])
  })

  it("takes the draft of the palette", async () => {
    route.query = { draft: "/usage " }
    const { ta } = await mountIt()
    expect(ta.value).toBe("/usage ")
    expect(replace).toHaveBeenCalledWith({ query: {} })
  })
})

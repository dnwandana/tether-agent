import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ref } from "vue"

vi.mock("../api/client", () => ({ runCommand: vi.fn() }))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { runCommand } from "../api/client"
import { confirm, toast } from "../stores/ui"
import { emitEvents } from "./bus"
import { replyKind, useCommand, type ResultNoteData } from "./command"

const reply = (text: string, buttons: { text: string; data: string }[][] = []) => ({
  request_id: "r1",
  text,
  buttons,
  edit: false,
})

beforeEach(() => {
  vi.mocked(runCommand).mockReset()
  vi.mocked(toast).mockReset()
  vi.mocked(confirm).mockReset()
})
afterEach(() => vi.useRealTimers())

describe("replyKind", () => {
  it("reads the kind from the start of the reply", () => {
    const cases: [string, string][] = [
      ["⚠️ **/rename failed**\nboom. Try again later.", "error"],
      ["❌ Approval denied", "error"],
      ["🚫 Blocked", "error"],
      ["⛔ Only the owner can use /mode.", "error"],
      ["🔒 Human-only", "error"],
      ["Usage: /goal wait <pid> [reason]", "error"],
      ["/goal wait: pid must be a positive integer", "error"],
      ["/goal gate add: no active goal", "error"],
      ["/subgoal remove: <n> must be an integer (1-based index).", "error"],
      ["⚠️ The list changed. Nothing was removed.", "warn"],
      ["⏳ **Busy**\n`/rename` cannot run during a task.", "warn"],
      ["ℹ️ Nothing to do.", "warn"],
      ["✅ Saved.", "ok"],
      ["✓ Goal cleared.", "ok"],
      ["💾 Backup written.", "ok"],
      ['🗄 **Archived "Trip"**', "ok"],
      ['✏️ **Renamed to "Trip"**', "ok"],
      ["🔀 Switched.", "ok"],
      ["🆕 **New session**", "ok"],
      ['↩️ **Back to "Trip"**', "ok"],
      ['🍴 **Copied to "Trip"**', "ok"],
      ["No goal set.", "info"],
      ["  ✅ Saved with a leading space.", "ok"],
    ]
    for (const [text, kind] of cases) expect(replyKind(text), text).toBe(kind)
  })
})

describe("useCommand", () => {
  it("shows a plain reply as a toast with its kind and Markdown", async () => {
    vi.mocked(runCommand).mockResolvedValue(reply("No goal set."))
    const { run } = useCommand()
    await expect(run("/goal")).resolves.toBe("info")
    expect(toast).toHaveBeenCalledWith("No goal set.", "info", { actions: [], markdown: true })
  })

  it("shows up to 2 buttons as toast actions", async () => {
    vi.mocked(runCommand).mockResolvedValue(
      reply('🗄 **Archived "Trip"**', [
        [
          { text: "↩️ Undo", data: "/unarchive 0123456789abcdef012" },
          { text: "🆕 New session", data: "/new" },
        ],
      ]),
    )
    const { run, reply: open } = useCommand()
    await expect(run("/archive 0123456789abcdef012")).resolves.toBe("ok")
    expect(toast).toHaveBeenCalledWith('🗄 **Archived "Trip"**', "ok", {
      actions: [
        { label: "↩️ Undo", data: "/unarchive 0123456789abcdef012" },
        { label: "🆕 New session", data: "/new" },
      ],
      markdown: true,
    })
    expect(open.value).toBeNull()
  })

  it("keeps the popover for more than 2 buttons", async () => {
    const r = reply("Pick a model.", [
      [{ text: "a", data: "/model a" }],
      [{ text: "b", data: "/model b" }],
      [{ text: "c", data: "/model c" }],
    ])
    vi.mocked(runCommand).mockResolvedValue(r)
    const { run, reply: open, closeReply } = useCommand()
    await run("/model")
    expect(open.value).toEqual(r)
    expect(toast).not.toHaveBeenCalled()
    closeReply()
    expect(open.value).toBeNull()
  })

  it("asks first for a confirm button and sends it on yes", async () => {
    vi.mocked(runCommand)
      .mockResolvedValueOnce(
        reply('Delete "Old"?', [
          [
            { text: "🗑 Delete", data: "/delete s1 confirm" },
            { text: "Cancel", data: "/sessions" },
          ],
        ]),
      )
      .mockResolvedValueOnce(reply('✅ Deleted "Old".'))
    vi.mocked(confirm).mockResolvedValue(true)
    const { run } = useCommand()
    await expect(run("/delete s1")).resolves.toBe("ok")
    expect(confirm).toHaveBeenCalledWith({
      title: 'Delete "Old"?',
      body: "",
      confirmLabel: "🗑 Delete",
      danger: true,
      markdown: true,
    })
    expect(runCommand).toHaveBeenLastCalledWith("/delete s1 confirm")
  })

  it("sends the gate no button when the owner cancels a gate confirm", async () => {
    vi.mocked(runCommand)
      .mockResolvedValueOnce(
        reply("⚠️ This gate matches a dangerous pattern: rm. Add it?", [
          [
            { text: "Add gate", data: "/goal gate ok 3" },
            { text: "Cancel", data: "/goal gate no 3" },
          ],
        ]),
      )
      .mockResolvedValueOnce(reply("Gate not added."))
    vi.mocked(confirm).mockResolvedValue(false)
    const { run } = useCommand()
    await run("/goal gate add rm -rf build")
    expect(runCommand).toHaveBeenLastCalledWith("/goal gate no 3")
  })

  it("puts the reply in the result note and not in a toast", async () => {
    vi.mocked(runCommand).mockResolvedValue(reply("⚠️ 2 checks warn."))
    const note = ref<ResultNoteData | null>(null)
    const { run } = useCommand()
    await expect(run("/doctor", { result: note, title: "Doctor" })).resolves.toBe("warn")
    expect(note.value).toMatchObject({ title: "Doctor", text: "⚠️ 2 checks warn.", kind: "warn" })
    expect(note.value?.at).toBeInstanceOf(Date)
    expect(toast).not.toHaveBeenCalled()
  })

  it("shows the friendly error of a failed call", async () => {
    vi.mocked(runCommand).mockRejectedValue(new TypeError("Failed to fetch"))
    const { run } = useCommand()
    await expect(run("/stop")).resolves.toBe("error")
    expect(toast).toHaveBeenCalledWith(
      "Cannot reach the gateway. Check the network, then try again.",
      "error",
    )
  })

  it("keeps an ack that arrives before the 202 reply is handled", async () => {
    vi.mocked(runCommand).mockImplementation(async () => {
      emitEvents([
        {
          name: "ack",
          data: { id: "a3", kind: "reply", text: "✅ Early", request_id: "r8", buttons: [] },
        },
      ])
      return { request_id: "r8" }
    })
    const { run } = useCommand()
    await expect(run("/compact")).resolves.toBe("ok")
    expect(toast).not.toHaveBeenCalledWith(expect.stringContaining("did not come"), "warn", {})
  })

  it("follows a late reply through the ack event", async () => {
    vi.mocked(runCommand).mockResolvedValue({ request_id: "r9" })
    const { run } = useCommand()
    const p = run("/compact")
    await Promise.resolve()
    await Promise.resolve()
    expect(toast).toHaveBeenCalledWith("Still running…", "info")
    emitEvents([
      {
        name: "ack",
        data: { id: "a1", kind: "reply", text: "x", request_id: "other", buttons: [] },
      },
    ])
    emitEvents([
      {
        name: "ack",
        data: { id: "a2", kind: "reply", text: "🗜 **Compacted**", request_id: "r9", buttons: [] },
      },
    ])
    await expect(p).resolves.toBe("info")
    expect(toast).toHaveBeenLastCalledWith("🗜 **Compacted**", "info", {
      actions: [],
      markdown: true,
    })
  })

  it("stops the follow after 60 s", async () => {
    vi.useFakeTimers()
    vi.mocked(runCommand).mockResolvedValue({ request_id: "r10" })
    const { run } = useCommand()
    const p = run("/compact")
    await vi.advanceTimersByTimeAsync(60_000)
    await expect(p).resolves.toBe("warn")
    expect(toast).toHaveBeenLastCalledWith(
      "The reply did not come. See the chat or the log.",
      "warn",
    )
  })
})

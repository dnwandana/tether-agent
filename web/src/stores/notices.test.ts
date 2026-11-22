import { describe, expect, it } from "vitest"
import { applyNoticeEvents, notices } from "./notices"

describe("notices store", () => {
  it("takes the buffer from hello and adds each notice event", () => {
    applyNoticeEvents([
      { name: "hello", data: { notices: [{ text: "🔄 Back.", at: "2026-11-20T10:00:00Z" }] } },
    ])
    expect(notices.list.map((n) => n.text)).toEqual(["🔄 Back."])
    applyNoticeEvents([
      { name: "notice", data: { text: "⏰ Job done.", at: "2026-11-20T10:01:00Z" } },
    ])
    expect(notices.list.map((n) => n.text)).toEqual(["🔄 Back.", "⏰ Job done."])
  })

  it("keeps the last 20 notices", () => {
    applyNoticeEvents([{ name: "hello", data: { notices: [] } }])
    for (let i = 0; i < 25; i++)
      applyNoticeEvents([{ name: "notice", data: { text: "n" + i, at: "" } }])
    expect(notices.list).toHaveLength(20)
    expect(notices.list[0].text).toBe("n5")
  })
})

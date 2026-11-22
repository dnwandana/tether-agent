import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import QueueStrip from "./QueueStrip.vue"

describe("QueueStrip", () => {
  it("lists the queue and runs it now", async () => {
    const w = mount(QueueStrip, {
      props: {
        items: [
          { kind: "media", preview: "📎 notes.md" },
          { kind: "skill", preview: "/release-notes" },
        ],
      },
    })
    expect(w.find(".qhead").text()).toContain("Waiting · 2 / 20")
    expect(w.find(".qhead").text()).toContain("They run after the current task, one at a time.")
    expect(w.findAll(".qitem:not(.qhead)").map((q) => q.text())).toEqual([
      "1📎 notes.md",
      "2/release-notes",
    ])
    await w.find("button").trigger("click")
    expect(w.find("button").text()).toBe("⚡ Do this now")
    expect(w.emitted("now")).toHaveLength(1)
  })
})

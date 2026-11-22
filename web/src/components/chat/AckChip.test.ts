import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import AckChip from "./AckChip.vue"

describe("AckChip", () => {
  it("shows the text with the class of its kind", () => {
    const w = mount(AckChip, {
      props: {
        ack: {
          id: "a1",
          kind: "steer",
          text: "↪️ Got it. I’ll do this after the current step.",
          request_id: "",
          buttons: [],
        },
      },
    })
    expect(w.find(".ack").classes()).toContain("ack-run")
    expect(w.text()).toContain("↪️ Got it.")
    expect(
      mount(AckChip, {
        props: {
          ack: { id: "a2", kind: "queue", text: "⏳ Queued.", request_id: "", buttons: [] },
        },
      })
        .find(".ack")
        .classes(),
    ).toContain("ack-pending")
  })

  it("emits the data of a button", async () => {
    const w = mount(AckChip, {
      props: {
        ack: {
          id: "a3",
          kind: "busy",
          text: "⏳ Busy",
          request_id: "",
          buttons: [[{ text: "⏹ Stop the task", data: "/stop" }]],
        },
      },
    })
    expect(w.find(".ack").classes()).toContain("ack-warn")
    await w.find("button").trigger("click")
    expect(w.emitted("action")).toEqual([["/stop"]])
  })
})

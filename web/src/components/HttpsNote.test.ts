import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import HttpsNote from "./HttpsNote.vue"

describe("HttpsNote", () => {
  it("shows one line with the help link, and opens the help in a side panel", async () => {
    const w = mount(HttpsNote)
    expect(w.find(".https-line").text()).toBe(
      "Voice notes, Copy, and notifications need HTTPS or localhost. How to turn on HTTPS",
    )
    expect(w.find("aside").classes()).not.toContain("is-open")
    await w.find(".https-line button.link").trigger("click")
    expect(w.find("aside").classes()).toContain("is-open")
    expect(w.find("aside").text()).toContain("Turn on HTTPS")
    expect(w.find("aside").text()).toContain("Set dashboard.tls_cert and dashboard.tls_key")
  })
})

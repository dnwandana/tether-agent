import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import LiveHeader from "./LiveHeader.vue"

describe("LiveHeader", () => {
  it("shows the time of the last load and emits refresh", async () => {
    const w = mount(LiveHeader, {
      props: { updatedAt: new Date(2026, 10, 23, 9, 5), stale: false },
    })
    expect(w.find(".live-h").text()).toContain("Updated 09:05")
    await w.find('[data-act="refresh"]').trigger("click")
    expect(w.find('[data-act="refresh"]').text()).toBe("↻ Refresh")
    expect(w.emitted("refresh")).toHaveLength(1)
  })

  it("shows no time before the first load, and marks a load as busy", () => {
    const w = mount(LiveHeader, { props: { updatedAt: null, stale: true } })
    expect(w.find(".live-h").text()).not.toContain("Updated")
    expect(w.find(".live-h").attributes("aria-busy")).toBe("true")
  })

  it("links to the Pages help of the wiki in a new tab", () => {
    const w = mount(LiveHeader, { props: { updatedAt: null, stale: false } })
    const a = w.find("a.help-link")
    expect(a.text()).toBe("?")
    expect(a.attributes("href")).toBe(
      "https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages",
    )
    expect(a.attributes("target")).toBe("_blank")
    expect(a.attributes("rel")).toBe("noreferrer")
    expect(a.attributes("title")).toBe("docs/wiki/Dashboard.md#pages")
  })
})

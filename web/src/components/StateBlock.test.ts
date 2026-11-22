import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import StateBlock from "./StateBlock.vue"

describe("StateBlock", () => {
  it("shows 5 skeleton rows while loading", () => {
    const w = mount(StateBlock, { props: { state: "loading" } })
    expect(w.findAll(".skel-row")).toHaveLength(5)
    expect(w.find('[aria-busy="true"]').exists()).toBe(true)
  })

  it("shows the help line and the next action when empty", async () => {
    const w = mount(StateBlock, {
      props: { state: "empty", text: "No jobs yet", action: "Create a job" },
    })
    expect(w.find(".state").text()).toContain("No jobs yet")
    await w.find("button").trigger("click")
    expect(w.find("button").text()).toBe("Create a job")
    expect(w.emitted("retry")).toHaveLength(1)
  })

  it("shows the cause and Retry on an error", async () => {
    const w = mount(StateBlock, { props: { state: "error", text: "GET /api/jobs: HTTP 502" } })
    expect(w.find(".state.state-error").exists()).toBe(true)
    expect(w.find(".cause").text()).toBe("GET /api/jobs: HTTP 502")
    expect(w.find("button").text()).toBe("Retry")
    await w.find("button").trigger("click")
    expect(w.emitted("retry")).toHaveLength(1)
  })
})

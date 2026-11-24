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
    expect(w.emitted("action")).toHaveLength(1)
  })

  it("shows the cause and Retry on an error", async () => {
    const w = mount(StateBlock, { props: { state: "error", text: "GET /api/jobs: HTTP 502" } })
    expect(w.find(".state.state-error").exists()).toBe(true)
    expect(w.find(".cause").text()).toBe("GET /api/jobs: HTTP 502")
    expect(w.find("button").text()).toBe("Retry")
    await w.find("button").trigger("click")
    expect(w.emitted("action")).toHaveLength(1)
  })

  it("emits only action, not retry", async () => {
    const empty = mount(StateBlock, {
      props: { state: "empty", text: "No jobs yet.", action: "+ New job" },
    })
    await empty.find("button").trigger("click")
    expect(empty.emitted("action")).toHaveLength(1)
    expect(empty.emitted("retry")).toBeUndefined()
    const error = mount(StateBlock, { props: { state: "error", text: "HTTP 502" } })
    await error.find("button").trigger("click")
    expect(error.emitted("action")).toHaveLength(1)
    expect(error.emitted("retry")).toBeUndefined()
  })

  it("uses a p.state-t heading and no h4", () => {
    const w = mount(StateBlock, { props: { state: "empty", text: "No jobs yet." } })
    expect(w.find("p.state-t").text()).toBe("No jobs yet.")
    expect(w.find("h4").exists()).toBe(false)
  })

  it("shows a title, the text, and a one-line hint when empty", () => {
    const w = mount(StateBlock, {
      props: {
        state: "empty",
        title: "No runs yet.",
        text: "The first run is at 09:00.",
        hint: "Run history keeps the last runs.",
        action: "⚡ Run now",
      },
    })
    expect(w.find("p.state-t").text()).toBe("No runs yet.")
    expect(w.find(".state").text()).toContain("The first run is at 09:00.")
    expect(w.find(".state-hint").text()).toBe("Run history keeps the last runs.")
    expect(w.find("button").text()).toBe("⚡ Run now")
  })

  it("shows no button when an empty state has no action", () => {
    const w = mount(StateBlock, { props: { state: "empty", text: "No messages yet." } })
    expect(w.find("button").exists()).toBe(false)
  })

  it("makes an error an alert with Could not load as the default title", () => {
    const w = mount(StateBlock, {
      props: {
        state: "error",
        text: "Cannot reach the gateway. Check the network, then try again.",
      },
    })
    expect(w.find(".state-error").attributes("role")).toBe("alert")
    expect(w.find("p.state-t").text()).toBe("Could not load")
  })

  it("uses the title prop of an error", async () => {
    const w = mount(StateBlock, {
      props: {
        state: "error",
        title: "The doctor failed",
        text: "HTTP 502",
        hint: "See the log on System operations.",
        action: "Run doctor",
      },
    })
    expect(w.find("p.state-t").text()).toBe("The doctor failed")
    expect(w.find(".cause").text()).toBe("HTTP 502")
    expect(w.find(".state-hint").text()).toBe("See the log on System operations.")
    await w.find("button").trigger("click")
    expect(w.emitted("action")).toHaveLength(1)
  })
})

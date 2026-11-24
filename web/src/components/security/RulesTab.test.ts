import { describe, it, expect, beforeEach } from "vitest"
import { mount } from "@vue/test-utils"
import RulesTab from "./RulesTab.vue"

const fixed = {
  protected_paths: ["~/.ssh/"],
  human_only: ["password change"],
  mcp_classes: { gmail: { send: 2, read: 5 } },
  acp_presets: ["claude", "codex"],
  acp_workdirs: [],
  toolsets: { telegram: ["all"] },
}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
})

describe("RulesTab", () => {
  it("shows the ACP presets and an empty block as None", () => {
    const w = mount(RulesTab, { props: { fixed, processMode: "smart", cronMode: "deny" } })
    const acp = w.findAll(".card").find((c) => c.text().includes("ACP presets"))!
    expect(acp.text()).toContain("claude")
    expect(acp.text()).toContain("codex")
    const folders = w.findAll(".card").find((c) => c.text().includes("ACP work folders"))!
    expect(folders.text()).toContain("None.")
  })

  it("explains each process mode and cron mode and marks the current one", () => {
    const w = mount(RulesTab, { props: { fixed, processMode: "smart", cronMode: "deny" } })
    const proc = w.findAll(".card").find((c) => c.text().includes("Process mode"))!
    const lines = proc.findAll("li")
    expect(lines.map((l) => l.find("code").text())).toEqual(["manual", "smart", "off"])
    expect(lines[0].text()).toContain("The gate asks you about each dangerous command.")
    expect(lines[1].find(".chip").text()).toBe("now")
    const cron = w.findAll(".card").find((c) => c.text().includes("Cron mode"))!
    expect(cron.findAll("li").map((l) => l.find("code").text())).toEqual(["deny", "approve"])
    expect(cron.findAll("li")[0].find(".chip").text()).toBe("now")
  })

  it("stacks the cards and folds the protected paths", () => {
    const one = mount(RulesTab, { props: { fixed, processMode: "smart", cronMode: "deny" } })
    expect(one.classes()).toContain("page-stack")
    const card = one.findAll(".card").find((c) => c.text().includes("Protected paths"))!
    expect(card.find("details summary").text()).toBe("1 path")
    expect(card.find("details").attributes("open")).toBeUndefined()
    expect(card.find("details .code-block").text()).toContain("~/.ssh/")
    const two = mount(RulesTab, {
      props: {
        fixed: { ...fixed, protected_paths: ["~/.ssh/", "~/.tether/state.db"] },
        processMode: "smart",
        cronMode: "deny",
      },
    })
    expect(two.find("details summary").text()).toBe("2 paths")
  })

  it("puts a space between the mode name, the now chip, and the text", () => {
    const w = mount(RulesTab, { props: { fixed, processMode: "smart", cronMode: "deny" } })
    const proc = w.findAll(".card").find((c) => c.text().includes("Process mode"))!
    expect(proc.findAll("li")[1].text().replace(/\s+/g, " ")).toMatch(/^smart now \S/)
  })
})

import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import { flushPromises } from "@vue/test-utils"
import { createMemoryHistory, createRouter } from "vue-router"
import SearchList from "./SearchList.vue"

const Blank = { render: () => null }
async function withRouter(props: object) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: Blank },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/")
  const r = render(SearchList, { props: props as any, global: { plugins: [router] } })
  await flushPromises()
  return r
}

describe("SearchList", () => {
  it("shows web_search as a list with safe links", async () => {
    const text =
      "1. Good\n   https://example.com/a\n   A snippet.\n\n2. Bad\n   javascript:alert(1)\n"
    const r = await withRouter({ kind: "web", text, query: "changelog format" })
    const trigger = screen.getByRole("button", { name: /changelog format/ })
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
    expect(trigger.textContent).toContain("2 results")
    const links = screen.getAllByRole("link")
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://example.com/a"])
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer")
    expect(links[0].getAttribute("target")).toBe("_blank")
    expect(r.container.textContent).toContain("javascript:alert(1)")
    expect(r.container.textContent).toContain("A snippet.")
    expect(r.container.querySelector('[data-slot="trace"]')!.hasAttribute("data-untrusted")).toBe(
      true,
    )
    const icon = trigger.querySelector('[data-slot="trace-icon"]')!
    expect(icon.querySelector("svg.lucide-search")).not.toBeNull()
    expect(trigger.textContent).not.toContain("🔍")
  })

  it("shows session_search hits with a link to the session", async () => {
    const text =
      '1 hits for "flaky", newest first:\nsession s1 · Fix the cron test · Nov 19 · msg 42: the runs test is flaky'
    const r = await withRouter({ kind: "session", text, query: "flaky" })
    const link = screen.getByRole("link", { name: /Fix the cron test/ })
    expect(link.getAttribute("href")).toBe("/sessions/s1?message=42")
    expect(r.container.textContent).toContain("the runs test is flaky")
    expect(screen.getByRole("button", { name: /flaky/ }).textContent).toContain("1 result")
    expect(r.container.querySelector("[data-untrusted]")).toBeNull()
    const trigger = screen.getByRole("button", { name: /flaky/ })
    expect(trigger.querySelector("svg.lucide-history")).not.toBeNull()
    expect(trigger.textContent).not.toContain("🔍")
  })
})

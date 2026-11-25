import { render, screen } from "@testing-library/vue"
import { flushPromises } from "@vue/test-utils"
import { defineComponent, h } from "vue"
import { createMemoryHistory, createRouter } from "vue-router"
import { describe, expect, it, vi } from "vitest"
import { listLink } from "@/stores/ui"
import { useRoutePage } from "./routePage"

const Blank = defineComponent({ render: () => null })
let api: ReturnType<typeof useRoutePage>
const Jobs = defineComponent({
  setup() {
    api = useRoutePage("jobs")
    return () => h("p", `page ${api.page.value}`)
  },
})

async function mk(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/jobs", name: "jobs", component: Jobs },
      { path: "/jobs/:id", name: "job", component: Blank },
    ],
  })
  await router.push(path)
  render(Jobs, { global: { plugins: [router] } })
  await flushPromises()
  return router
}

describe("useRoutePage", () => {
  it("reads the page from the query", async () => {
    await mk("/jobs?page=3")
    expect(screen.getByText("page 3")).toBeTruthy()
  })

  it("gives page 1 for a bad value", async () => {
    await mk("/jobs?page=abc")
    expect(screen.getByText("page 1")).toBeTruthy()
  })

  it("writes the page with replace and keeps the other keys", async () => {
    const router = await mk("/jobs?q=x")
    const replace = vi.spyOn(router, "replace")
    const push = vi.spyOn(router, "push")
    api.setPage(2)
    await flushPromises()
    expect(replace).toHaveBeenCalledTimes(1)
    expect(push).not.toHaveBeenCalled()
    expect(router.currentRoute.value.query).toEqual({ q: "x", page: "2" })
    expect(screen.getByText("page 2")).toBeTruthy()
  })

  it("leaves page 1 out of the query", async () => {
    const router = await mk("/jobs?page=2")
    api.setPage(1)
    await flushPromises()
    expect(router.currentRoute.value.query).toEqual({})
  })

  it("does not write when the value does not change", async () => {
    const router = await mk("/jobs?page=2")
    const replace = vi.spyOn(router, "replace")
    api.setPage(2)
    await flushPromises()
    expect(replace).not.toHaveBeenCalled()
  })

  it("does not write while another route shows", async () => {
    const router = await mk("/jobs")
    await router.push("/jobs/j1")
    const replace = vi.spyOn(router, "replace")
    api.setPage(4)
    await flushPromises()
    expect(replace).not.toHaveBeenCalled()
    expect(router.currentRoute.value.fullPath).toBe("/jobs/j1")
  })

  it("follows a change of the query", async () => {
    const router = await mk("/jobs?page=3")
    await router.push("/jobs")
    await flushPromises()
    expect(screen.getByText("page 1")).toBeTruthy()
    await router.replace({ query: { page: "2" } })
    await flushPromises()
    expect(screen.getByText("page 2")).toBeTruthy()
  })

  it("remembers the written query for the back link", async () => {
    await mk("/jobs?q=x")
    api.setPage(2)
    await flushPromises()
    expect(listLink("jobs")).toEqual({ name: "jobs", query: { q: "x", page: "2" } })
  })
})

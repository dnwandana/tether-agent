import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"
import TablePager from "./TablePager.vue"

const pageNames = () =>
  screen.getAllByRole("button", { name: /^Page \d+$/ }).map((b) => b.getAttribute("aria-label"))
const sizeSelect = () => screen.getByLabelText("Rows per page") as HTMLSelectElement
// Gives a matchMedia that matches only the phone query of the pager.
function stubPhone() {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: q === "(max-width: 599px)",
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

afterEach(() => vi.unstubAllGlobals())

describe("TablePager", () => {
  it("shows the range from the page, the size, and the total", () => {
    render(TablePager, { props: { page: 2, size: 20, total: 86, label: "Sessions" } })
    expect(screen.getByText("21–40 of 86")).toBeTruthy()
  })

  it("cuts the range at the total on the last page", () => {
    render(TablePager, { props: { page: 5, size: 20, total: 86, label: "Sessions" } })
    expect(screen.getByText("81–86 of 86")).toBeTruthy()
  })

  it("names the nav from the label", () => {
    render(TablePager, { props: { page: 1, size: 20, total: 86, label: "Approval log" } })
    expect(screen.getByRole("navigation", { name: "Approval log pages" })).toBeTruthy()
  })

  it("shows a Page N button for each page and marks the current page", () => {
    render(TablePager, { props: { page: 2, size: 20, total: 86, label: "Sessions" } })
    expect(pageNames()).toEqual(["Page 1", "Page 2", "Page 3", "Page 4", "Page 5"])
    expect(screen.getByRole("button", { name: "Page 2" }).getAttribute("aria-current")).toBe("page")
    expect(screen.getByRole("button", { name: "Page 1" }).getAttribute("aria-current")).toBeNull()
  })

  it("keeps the first and the last page when there are many pages", () => {
    render(TablePager, { props: { page: 1, size: 20, total: 412, label: "Approval log" } })
    expect(pageNames()).toEqual(["Page 1", "Page 2", "Page 3", "Page 4", "Page 5", "Page 21"])
  })

  it("disables Previous Page on the first page and Next Page on the last page", () => {
    const { rerender } = render(TablePager, {
      props: { page: 1, size: 20, total: 86, label: "Sessions" },
    })
    expect(screen.getByRole("button", { name: "Previous Page" }).hasAttribute("disabled")).toBe(
      true,
    )
    expect(screen.getByRole("button", { name: "Next Page" }).hasAttribute("disabled")).toBe(false)
    return rerender({ page: 5 }).then(() => {
      expect(screen.getByRole("button", { name: "Next Page" }).hasAttribute("disabled")).toBe(true)
    })
  })

  it("emits update:page and does not change its own page", async () => {
    const user = userEvent.setup()
    const { emitted } = render(TablePager, {
      props: { page: 2, size: 20, total: 86, label: "Sessions" },
    })
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    await user.click(screen.getByRole("button", { name: "Page 4" }))
    await user.click(screen.getByRole("button", { name: "Previous Page" }))
    expect(emitted()["update:page"]).toEqual([[3], [4], [1]])
    expect(screen.getByRole("button", { name: "Page 2" }).getAttribute("aria-current")).toBe("page")
  })

  it("offers 10, 20, 50, and 100 rows and emits a number", async () => {
    const user = userEvent.setup()
    const { emitted } = render(TablePager, {
      props: { page: 1, size: 20, total: 86, label: "Sessions" },
    })
    const select = sizeSelect()
    expect(Array.from(select.options).map((o) => o.textContent?.trim())).toEqual([
      "10",
      "20",
      "50",
      "100",
    ])
    expect(select.value).toBe("20")
    await user.selectOptions(select, "50")
    expect(emitted()["update:size"]).toEqual([[50]])
  })

  it("renders nothing while the total is 10 or less", () => {
    render(TablePager, { props: { page: 1, size: 10, total: 10, label: "Jobs" } })
    expect(screen.queryByRole("navigation")).toBeNull()
    expect(screen.queryByText("Rows per page")).toBeNull()
  })

  it("shows at a total of 11", () => {
    render(TablePager, { props: { page: 1, size: 10, total: 11, label: "Jobs" } })
    expect(screen.getByRole("navigation", { name: "Jobs pages" })).toBeTruthy()
    expect(screen.getByText("1–10 of 11")).toBeTruthy()
  })

  it("disables the buttons and the select while a page loads", () => {
    render(TablePager, {
      props: { page: 2, size: 20, total: 86, label: "Sessions", loading: true },
    })
    expect(screen.getByRole("button", { name: "Next Page" }).hasAttribute("disabled")).toBe(true)
    expect(screen.getByRole("button", { name: "Previous Page" }).hasAttribute("disabled")).toBe(
      true,
    )
    expect(screen.getByRole("button", { name: "Page 3" }).hasAttribute("disabled")).toBe(true)
    expect(sizeSelect().disabled).toBe(true)
  })

  it("shows 2 / 5 and hides the page numbers below 600 px", () => {
    stubPhone()
    render(TablePager, { props: { page: 2, size: 20, total: 86, label: "Sessions" } })
    expect(screen.getByText("2 / 5")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "Page 1" })).toBeNull()
    expect(screen.getByText("Rows per page").className).toContain("sr-only")
    expect(screen.getByRole("button", { name: "Previous Page" }).className).toContain(
      "max-tab:h-11",
    )
    expect(screen.getByRole("button", { name: "Next Page" }).className).toContain("max-tab:h-11")
    expect(sizeSelect().className).toContain("max-tab:h-11")
  })
})

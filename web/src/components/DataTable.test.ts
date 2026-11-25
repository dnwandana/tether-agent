import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"
import DataTable from "./DataTable.vue"
import { flushPromises } from "@vue/test-utils"

const h = vi.hoisted(() => ({ width: 0 }))
vi.mock("@vueuse/core", async (importOriginal) => {
  const { ref } = await import("vue")
  const mod = await importOriginal<typeof import("@vueuse/core")>()
  return {
    ...mod,
    useElementSize: () => ({ width: ref(h.width), height: ref(0), stop: () => {} }),
  }
})

const columns = [
  { key: "name", label: "Job" },
  { key: "next", label: "Next run" },
  { key: "cost", label: "Cost", align: "right" as const },
  { key: "actions", label: "" },
]
const rows = [
  { id: "j1", name: "nightly-backup", next: "02:00", cost: "$0.0120" },
  { id: "j2", name: "gh-digest", next: "07:00", cost: "$0.0031" },
]
// The body rows, without the header row.
const bodyRows = () => within(screen.getAllByRole("rowgroup")[1]).getAllByRole("row")
const cellsOf = (row: HTMLElement) => within(row).getAllByRole("cell")
const text = (el: HTMLElement) => el.textContent?.trim() ?? ""

afterEach(() => {
  vi.unstubAllGlobals()
  h.width = 0
})

describe("DataTable", () => {
  it("renders the headers, the cells, and the card labels", () => {
    render(DataTable, { props: { columns, rows, rowKey: "id" } })
    expect(screen.getAllByRole("columnheader").map(text)).toEqual(["Job", "Next run", "Cost", ""])
    const cells = cellsOf(bodyRows()[0])
    expect(cells.map(text)).toEqual(["nightly-backup", "02:00", "$0.0120", ""])
    expect(cells[0].hasAttribute("data-first")).toBe(true)
    expect(cells[0].getAttribute("data-l")).toBe("")
    expect(cells[1].getAttribute("data-l")).toBe("Next run")
    expect(cells[2].className).toContain("text-right")
    expect(screen.getAllByRole("columnheader")[2].className).toContain("text-right")
    expect(cells[3].hasAttribute("data-act-col")).toBe(true)
    expect(cells[3].getAttribute("data-l")).toBe("")
    expect(screen.getByRole("table").closest("[data-layout]")?.getAttribute("data-layout")).toBe(
      "table",
    )
  })

  it("uses the cell slot", () => {
    render(DataTable, {
      props: { columns, rows, rowKey: "id" },
      slots: {
        "cell-name":
          '<template #cell-name="{ row, value }"><b>{{ value }} ({{ row.id }})</b></template>',
      },
    })
    expect(screen.getByText("nightly-backup (j1)").tagName).toBe("B")
  })

  it("prints nothing when a cell slot renders only comments", () => {
    render(DataTable, {
      props: {
        columns: [
          { key: "name", label: "Skill" },
          { key: "tags", label: "Tags" },
          { key: "n", label: "Uses", align: "right" as const },
        ],
        rows: [{ id: "k1", name: "deploy", tags: [], n: 7 }],
        rowKey: "id",
      },
      slots: {
        "cell-tags": '<template #cell-tags><button v-if="false">Add</button></template>',
        "cell-n": '<template #cell-n><button v-if="false">Reset</button></template>',
      },
    })
    expect(cellsOf(bodyRows()[0]).map(text)).toEqual(["deploy", "", ""])
  })

  it("prints only a string or a number when the column has no slot", () => {
    render(DataTable, {
      props: {
        columns: [
          { key: "s", label: "S" },
          { key: "zero", label: "Zero" },
          { key: "list", label: "List" },
          { key: "obj", label: "Obj" },
          { key: "none", label: "None" },
          { key: "missing", label: "Missing" },
        ],
        rows: [{ id: "r1", s: "text", zero: 0, list: ["a", "b"], obj: { a: 1 }, none: null }],
        rowKey: "id",
      },
    })
    expect(cellsOf(bodyRows()[0]).map(text)).toEqual(["text", "0", "", "", "", ""])
  })

  it("emits row-click with the row", async () => {
    const user = userEvent.setup()
    const { emitted } = render(DataTable, { props: { columns, rows, rowKey: "id" } })
    await user.click(screen.getByText("gh-digest"))
    expect(emitted()["row-click"]).toEqual([[rows[1]]])
  })

  it("shows Nothing to show. with no rows", () => {
    render(DataTable, { props: { columns, rows: [], rowKey: "id" } })
    const cell = screen.getByText("Nothing to show.")
    expect(cell.getAttribute("colspan")).toBe("4")
  })

  it("becomes a card list below cardsBelow", () => {
    const asked: string[] = []
    vi.stubGlobal("matchMedia", (q: string) => {
      asked.push(q)
      return { matches: true, media: q, addEventListener: () => {}, removeEventListener: () => {} }
    })
    render(DataTable, { props: { columns, rows, rowKey: "id", cardsBelow: 1024 } })
    expect(asked).toContain("(max-width: 1023px)")
    expect(screen.getByRole("table").closest("[data-layout]")?.getAttribute("data-layout")).toBe(
      "cards",
    )
    const cell = cellsOf(bodyRows()[0])[1]
    expect(cell.className).toContain("before:content-[attr(data-l)]")
  })
})

// Gives n rows with the names "Row 1" to "Row n".
const many = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    id: "r" + (i + 1),
    name: "Row " + (i + 1),
    next: "",
    cost: "",
  }))
const firstName = () => text(cellsOf(bodyRows()[0])[0])
const sizeSelect = () => screen.getByLabelText("Rows per page") as HTMLSelectElement

describe("DataTable paging", () => {
  it("shows no pager with 10 rows or fewer", () => {
    render(DataTable, { props: { columns, rows: many(10), rowKey: "id", pageKey: "t" } })
    expect(bodyRows()).toHaveLength(10)
    expect(screen.queryByRole("navigation")).toBeNull()
  })

  it("slices the rows in the browser", async () => {
    const user = userEvent.setup()
    const { emitted } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t" },
    })
    expect(bodyRows()).toHaveLength(20)
    expect(screen.getByText("1–20 of 25")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    expect(bodyRows()).toHaveLength(5)
    expect(firstName()).toBe("Row 21")
    expect(screen.getByText("21–25 of 25")).toBeTruthy()
    expect(emitted()["update:page"]).toEqual([[2]])
  })

  it("names the nav from pageLabel, with Rows as the default", () => {
    render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t", pageLabel: "Jobs" },
    })
    expect(screen.getByRole("navigation", { name: "Jobs pages" })).toBeTruthy()
  })

  it("uses Rows pages with no pageLabel", () => {
    render(DataTable, { props: { columns, rows: many(25), rowKey: "id", pageKey: "t" } })
    expect(screen.getByRole("navigation", { name: "Rows pages" })).toBeTruthy()
  })

  it("reads and writes the stored size of its pageKey", async () => {
    const user = userEvent.setup()
    localStorage.setItem("tether.rows.t", "10")
    const { emitted } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t" },
    })
    expect(bodyRows()).toHaveLength(10)
    expect(screen.getByText("1–10 of 25")).toBeTruthy()
    await user.selectOptions(sizeSelect(), "50")
    expect(localStorage.getItem("tether.rows.t")).toBe("50")
    expect(emitted()["update:size"]).toEqual([[50]])
    expect(bodyRows()).toHaveLength(25)
  })

  it("does not read the size of another pageKey", () => {
    localStorage.setItem("tether.rows.t", "10")
    render(DataTable, { props: { columns, rows: many(25), rowKey: "id", pageKey: "u" } })
    expect(bodyRows()).toHaveLength(20)
  })

  it("goes to page 1 on a size change", async () => {
    const user = userEvent.setup()
    const { emitted } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t" },
    })
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    await user.selectOptions(sizeSelect(), "10")
    expect(screen.getByText("1–10 of 25")).toBeTruthy()
    expect(firstName()).toBe("Row 1")
    expect(emitted()["update:page"]).toEqual([[2], [1]])
  })

  it("follows v-model:page in browser mode", async () => {
    const user = userEvent.setup()
    const { emitted, rerender } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t", page: 2 },
    })
    expect(firstName()).toBe("Row 21")
    await user.click(screen.getByRole("button", { name: "Page 1" }))
    expect(emitted()["update:page"]).toEqual([[1]])
    expect(firstName()).toBe("Row 21")
    await rerender({ page: 1 })
    expect(firstName()).toBe("Row 1")
  })

  it("shows the server rows as they are, with the total of the parent", async () => {
    const user = userEvent.setup()
    const { emitted } = render(DataTable, {
      props: { columns, rows: many(20), rowKey: "id", pageKey: "s", total: 412, page: 2, size: 20 },
    })
    expect(bodyRows()).toHaveLength(20)
    expect(firstName()).toBe("Row 1")
    expect(screen.getByText("21–40 of 412")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    expect(emitted()["update:page"]).toEqual([[3]])
    await user.selectOptions(sizeSelect(), "50")
    expect(emitted()["update:size"]).toEqual([[50]])
    expect(emitted()["update:page"]).toEqual([[3], [1]])
    expect(localStorage.getItem("tether.rows.s")).toBeNull()
  })

  it("disables the pager while a server page loads", () => {
    render(DataTable, {
      props: {
        columns,
        rows: many(20),
        rowKey: "id",
        pageKey: "s",
        total: 412,
        page: 2,
        size: 20,
        loading: true,
      },
    })
    expect(screen.getByRole("button", { name: "Next Page" }).hasAttribute("disabled")).toBe(true)
    expect(sizeSelect().disabled).toBe(true)
  })

  it("moves to the last page when the browser rows shrink", async () => {
    const user = userEvent.setup()
    const { emitted, rerender } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t" },
    })
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    await rerender({ rows: many(22) })
    expect(screen.getByText("21–22 of 22")).toBeTruthy()
    await rerender({ rows: many(15) })
    expect(screen.getByText("1–15 of 15")).toBeTruthy()
    expect(bodyRows()).toHaveLength(15)
    expect(emitted()["update:page"]).toEqual([[2], [1]])
  })

  it("moves a page beyond the end to the last page at mount", async () => {
    const { emitted } = render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t", page: 99 },
    })
    await flushPromises()
    expect(emitted()["update:page"]).toEqual([[2]])
  })

  it("keeps the page at mount while the rows are not loaded", async () => {
    const { emitted } = render(DataTable, {
      props: { columns, rows: [], rowKey: "id", pageKey: "t", page: 3 },
    })
    await flushPromises()
    expect(emitted()["update:page"]).toBeUndefined()
  })

  it("renders the footer slot on each page and does not count it", async () => {
    const user = userEvent.setup()
    render(DataTable, {
      props: { columns, rows: many(25), rowKey: "id", pageKey: "t" },
      slots: { footer: '<tr><td colspan="4">Total · 3 jobs</td></tr>' },
    })
    expect(screen.getByText("Total · 3 jobs").closest("tfoot")).not.toBeNull()
    expect(screen.getByText("1–20 of 25")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    expect(screen.getByText("Total · 3 jobs").closest("tfoot")).not.toBeNull()
    expect(screen.getByText("21–25 of 25")).toBeTruthy()
  })

  it("scrolls the table top into view when it is above the view", async () => {
    const user = userEvent.setup()
    render(DataTable, { props: { columns, rows: many(25), rowKey: "id", pageKey: "t" } })
    const wrap = screen.getByRole("table").closest<HTMLElement>('[data-slot="data-table"]')!
    wrap.scrollIntoView = vi.fn()
    wrap.getBoundingClientRect = () => ({ top: -200 }) as DOMRect
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    expect(wrap.scrollIntoView).toHaveBeenCalledWith({ block: "start" })
  })

  it("does not scroll when the table top is in view", async () => {
    const user = userEvent.setup()
    render(DataTable, { props: { columns, rows: many(25), rowKey: "id", pageKey: "t" } })
    const wrap = screen.getByRole("table").closest<HTMLElement>('[data-slot="data-table"]')!
    wrap.scrollIntoView = vi.fn()
    wrap.getBoundingClientRect = () => ({ top: 40 }) as DOMRect
    await user.click(screen.getByRole("button", { name: "Next Page" }))
    expect(wrap.scrollIntoView).not.toHaveBeenCalled()
  })
})

const typed = [
  { key: "name", label: "Name" },
  { key: "when", label: "When", type: "date" as const },
  { key: "state", label: "State", type: "badge" as const },
  { key: "n", label: "Uses", align: "right" as const },
  { key: "note", label: "Note", type: "wrap" as const },
  { key: "tags", label: "Tags", type: "block" as const },
  { key: "actions", label: "", align: "right" as const, width: "11rem" },
]
const typedRows = [
  { id: "a", name: "nightly-backup", when: "", state: "", n: 3, note: "", tags: "" },
]
const optCols = [
  { key: "name", label: "Name" },
  { key: "started", label: "Started", type: "date" as const, optional: true },
  { key: "msgs", label: "Messages", align: "right" as const, optional: true },
  { key: "actions", label: "", width: "6rem" },
]
const optRows = [{ id: "a", name: "s1", started: "", msgs: 4 }]
const colWidths = () =>
  Array.from(screen.getByRole("table").querySelectorAll("col")).map((c) => c.style.width)
const minWidth = () =>
  screen.getByRole("table").parentElement!.style.getPropertyValue("--table-min")
const headers = () => screen.getAllByRole("columnheader").map(text)
// Gives a matchMedia that matches each query, so DataTable shows cards.
function stubCards() {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: true,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

describe("DataTable columns", () => {
  it("writes a col width for each type and the act width", () => {
    render(DataTable, { props: { columns: typed, rows: typedRows, rowKey: "id" } })
    expect(colWidths()).toEqual(["", "9rem", "8rem", "6rem", "", "", "11rem"])
    expect(screen.getByRole("table").className).toContain("table-fixed")
  })

  it("sets the minimum width of the table", () => {
    render(DataTable, { props: { columns: typed, rows: typedRows, rowKey: "id" } })
    expect(minWidth()).toBe("64rem")
    expect(screen.getByRole("table").className).toContain("min-w-(--table-min)")
  })

  it("gives each cell the classes of its type", () => {
    render(DataTable, { props: { columns: typed, rows: typedRows, rowKey: "id" } })
    const [name, , , n, note, tags, act] = cellsOf(bodyRows()[0])
    expect(name.className).toContain("overflow-hidden")
    expect(name.className).toContain("text-ellipsis")
    expect(name.getAttribute("title")).toBe("nightly-backup")
    expect(n.className).toContain("tabular-nums")
    expect(note.className).toContain("whitespace-normal")
    expect(note.className).toContain("overflow-hidden")
    expect(tags.className).toContain("whitespace-normal")
    expect(tags.className).not.toContain("overflow-hidden")
    expect(act.className).toContain("text-right")
    expect(act.className).not.toContain("tabular-nums")
    expect(act.className).not.toContain("overflow-hidden")
  })

  it("puts the act cell content in an inline-flex box", () => {
    render(DataTable, {
      props: { columns: typed, rows: typedRows, rowKey: "id" },
      slots: { "cell-actions": "<template #cell-actions><button>Run</button></template>" },
    })
    const box = screen.getByRole("button", { name: "Run" }).parentElement!
    expect(box.className).toContain("inline-flex")
    expect(box.className).toContain("justify-end")
  })

  it("has no colgroup and no fixed layout in card mode", () => {
    stubCards()
    render(DataTable, {
      props: { columns: typed, rows: typedRows, rowKey: "id", cardsBelow: 1024 },
    })
    expect(colWidths()).toEqual([])
    expect(screen.getByRole("table").className).not.toContain("table-fixed")
    expect(minWidth()).toBe("")
  })

  it("is a container and shows the optional columns when the table fits", () => {
    h.width = 500
    render(DataTable, { props: { columns: optCols, rows: optRows, rowKey: "id" } })
    const wrap = screen.getByRole("table").closest('[data-slot="data-table"]')!
    expect(wrap.className).toContain("@container")
    expect(headers()).toEqual(["Name", "Started", "Messages", ""])
    expect(minWidth()).toBe("31rem")
  })

  it("hides the optional columns while the wrapper is narrower than the full table", () => {
    h.width = 400
    render(DataTable, { props: { columns: optCols, rows: optRows, rowKey: "id" } })
    expect(headers()).toEqual(["Name", ""])
    expect(colWidths()).toEqual(["", "6rem"])
    expect(cellsOf(bodyRows()[0])).toHaveLength(2)
    expect(minWidth()).toBe("16rem")
  })

  it("shows the optional columns before the first measure", () => {
    h.width = 0
    render(DataTable, { props: { columns: optCols, rows: optRows, rowKey: "id" } })
    expect(headers()).toEqual(["Name", "Started", "Messages", ""])
  })

  it("shows the optional columns in card mode", () => {
    stubCards()
    h.width = 400
    render(DataTable, { props: { columns: optCols, rows: optRows, rowKey: "id" } })
    expect(cellsOf(bodyRows()[0])).toHaveLength(4)
  })
})

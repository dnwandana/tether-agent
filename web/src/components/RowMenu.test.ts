import { render, screen, waitFor } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h } from "vue"
import { describe, expect, it } from "vitest"
import RowMenu from "./RowMenu.vue"
import ConfirmDialog from "./ConfirmDialog.vue"
import { confirm } from "../stores/ui"

const items = [
  { label: "Rename", act: "rename" },
  { label: "Delete…", act: "delete", danger: true },
]
// Reka sets pointer-events: none on body while a modal layer is open.
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
// A modal menu hides the rest of the page with aria-hidden, so the query includes hidden nodes.
const trigger = () => screen.getByRole("button", { name: "More actions", hidden: true })

describe("RowMenu", () => {
  it("opens a popover outside the table and emits the action", async () => {
    const user = setup()
    const { container, emitted } = render(RowMenu, { props: { items } })
    expect(trigger().textContent?.trim()).toBe("⋯")
    expect(trigger().getAttribute("aria-expanded")).toBe("false")
    await user.click(trigger())
    expect(trigger().getAttribute("aria-expanded")).toBe("true")
    // The menu is in a portal on body, so a table wrapper cannot cut it.
    const menu = screen.getByRole("menu")
    expect(container.contains(menu)).toBe(false)
    const del = screen.getByRole("menuitem", { name: "Delete…" })
    expect(del.getAttribute("data-variant")).toBe("destructive")
    expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("data-variant")).not.toBe(
      "destructive",
    )
    await user.click(del)
    expect(emitted().select).toEqual([["delete"]])
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())
  })

  it("closes on Esc and on a tap outside", async () => {
    const user = setup()
    render(RowMenu, { props: { items } })
    await user.click(trigger())
    await user.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())
    await user.click(trigger())
    expect(screen.getByRole("menu")).toBeTruthy()
    await user.click(document.body)
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())
  })

  it("focuses the first item and moves with the arrow keys", async () => {
    const user = setup()
    render(RowMenu, { props: { items } })
    trigger().focus()
    await user.keyboard("{Enter}")
    const its = screen.getAllByRole("menuitem")
    await waitFor(() => expect(document.activeElement).toBe(its[0]))
    await user.keyboard("{ArrowDown}")
    expect(document.activeElement).toBe(its[1])
    await user.keyboard("{ArrowDown}")
    expect(document.activeElement).toBe(its[0])
    await user.keyboard("{ArrowUp}")
    expect(document.activeElement).toBe(its[1])
  })

  it("closes when Tab leaves the menu", async () => {
    const user = setup()
    render(RowMenu, { props: { items } })
    trigger().focus()
    await user.keyboard("{Enter}")
    await waitFor(() => expect(document.activeElement?.getAttribute("role")).toBe("menuitem"))
    await user.keyboard("{Tab}")
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull())
  })

  it("gives each item a height of at least 44 px below 600 px", async () => {
    const user = setup()
    render(RowMenu, { props: { items } })
    await user.click(trigger())
    for (const item of screen.getAllByRole("menuitem")) {
      expect(item.className).toContain("max-tab:min-h-11")
    }
  })

  it("puts the focus on the trigger after Delete… and Cancel", async () => {
    const user = setup()
    const Host = defineComponent({
      setup: () => () => [
        h(RowMenu, {
          items,
          onSelect: (act: string) => {
            if (act === "delete") {
              void confirm({
                title: "Delete the session?",
                body: "",
                confirmLabel: "Delete",
                danger: true,
              })
            }
          },
        }),
        h(ConfirmDialog),
      ],
    })
    render(Host)
    await user.click(trigger())
    await user.click(screen.getByRole("menuitem", { name: "Delete…" }))
    await screen.findByRole("alertdialog")
    await user.click(screen.getByRole("button", { name: "Cancel" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger()))
  })
})

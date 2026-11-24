import { cleanup, render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"
import { defineComponent, h } from "vue"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

// One root element: Testing Library removes its container only when the root is an element.
const Menu = defineComponent({
  render: () =>
    h("div", null, [
      h(DropdownMenu, null, {
        default: () => [
          h(DropdownMenuTrigger, null, { default: () => "Open" }),
          h(DropdownMenuContent, null, {
            default: () =>
              h(DropdownMenuGroup, null, {
                default: () => h(DropdownMenuItem, null, { default: () => "Rename" }),
              }),
          }),
        ],
      }),
    ]),
})

describe("vitest.setup.ts", () => {
  it("gives jsdom the APIs that Reka UI reads", () => {
    expect(typeof ResizeObserver).toBe("function")
    expect(typeof PointerEvent).toBe("function")
    expect(new PointerEvent("pointerdown", { pointerId: 3 }).pointerId).toBe(3)
    expect(typeof HTMLElement.prototype.scrollIntoView).toBe("function")
    const el = document.createElement("div")
    expect(el.hasPointerCapture(1)).toBe(false)
    expect(() => el.setPointerCapture(1)).not.toThrow()
    expect(() => el.releasePointerCapture(1)).not.toThrow()
    expect(() => getComputedStyle(el, "::before")).not.toThrow()
  })

  it("opens a Reka menu with a click, in a portal on the body", async () => {
    const user = userEvent.setup()
    render(Menu)
    await user.click(screen.getByRole("button", { name: "Open" }))
    const item = await screen.findByRole("menuitem", { name: "Rename" })
    expect(document.body.contains(item)).toBe(true)
  })

  it("removes the portal content on cleanup", async () => {
    const user = userEvent.setup()
    render(Menu)
    await user.click(screen.getByRole("button", { name: "Open" }))
    await screen.findByRole("menuitem", { name: "Rename" })
    cleanup()
    expect(document.body.children).toHaveLength(0)
  })
})

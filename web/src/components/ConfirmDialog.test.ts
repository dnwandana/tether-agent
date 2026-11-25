import { fireEvent, render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it } from "vitest"
import { defineComponent, h, nextTick } from "vue"
import type { Card } from "../api/types.gen"
import { rememberOpener } from "../lib/layer"
import { applyCardEvents, cards } from "../stores/cards"
import { confirm, pendingConfirm } from "../stores/ui"
import ConfirmDialog from "./ConfirmDialog.vue"

afterEach(() => {
  pendingConfirm.resolve?.(false)
  cards.open = []
  rememberOpener(null)
  for (const el of document.querySelectorAll("[data-opener]")) el.remove()
})

const dialog = (name: string | RegExp) => screen.findByRole("alertdialog", { name })
const focused = () => document.activeElement?.textContent?.trim()

// Adds a button outside the dialog, as a page button that opens a confirm.
function opener(text: string): HTMLButtonElement {
  const b = document.createElement("button")
  b.textContent = text
  b.setAttribute("data-opener", "")
  document.body.append(b)
  return b
}

describe("ConfirmDialog", () => {
  it("shows the open confirm and answers true", async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    const p = confirm({
      title: "Approve 2 commands once?",
      body: "rm -rf build/\ngo test ./...",
      confirmLabel: "Approve all once",
    })
    const d = await dialog("Approve 2 commands once?")
    expect(d.textContent).toContain("rm -rf build/")
    const buttons = within(d).getAllByRole("button")
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["Cancel", "Approve all once"])
    expect(buttons[1].className).toContain("bg-primary")
    await user.click(buttons[1])
    await expect(p).resolves.toBe(true)
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
  })

  it("answers false on Esc and uses the danger button", async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    const p = confirm({ title: "Delete the job?", body: "", confirmLabel: "Delete", danger: true })
    const d = await dialog("Delete the job?")
    expect(within(d).getByRole("button", { name: "Delete" }).className).toContain("bg-destructive")
    await waitFor(() => expect(focused()).toBe("Cancel"))
    await user.keyboard("{Escape}")
    await expect(p).resolves.toBe(false)
  })

  it("keeps the dialog open on an outside tap", async () => {
    render(ConfirmDialog)
    const p = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await dialog("Restart?")
    // Reka adds its outside listener one timer after the open.
    await new Promise((r) => setTimeout(r, 0))
    await fireEvent.pointerDown(document.querySelector('[data-slot="alert-dialog-overlay"]')!)
    expect(pendingConfirm.opts?.title).toBe("Restart?")
    pendingConfirm.resolve?.(false)
    await p
  })

  it("starts on the button that focus names", async () => {
    render(ConfirmDialog)
    const p = confirm({
      title: "Approve 2 commands once?",
      body: "Each command runs one time.",
      confirmLabel: "Approve all once",
      focus: "cancel",
    })
    await dialog("Approve 2 commands once?")
    await waitFor(() => expect(focused()).toBe("Cancel"))
    pendingConfirm.resolve?.(false)
    await p
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    const q = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await dialog("Restart?")
    await waitFor(() => expect(focused()).toBe("Restart"))
    pendingConfirm.resolve?.(false)
    await q
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    const r = confirm({
      title: "Delete the job?",
      body: "",
      confirmLabel: "Delete",
      danger: true,
      focus: "ok",
    })
    await dialog("Delete the job?")
    await waitFor(() => expect(focused()).toBe("Delete"))
    pendingConfirm.resolve?.(false)
    await r
  })

  it("shows the items as typed, one row each", async () => {
    render(ConfirmDialog)
    confirm({
      title: "Approve 3 commands once?",
      body: "",
      confirmLabel: "Approve all once",
      items: ["![a](b)", "[](x)", "rm *.log *.tmp"],
    })
    const d = await dialog("Approve 3 commands once?")
    expect(
      within(d)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["![a](b)", "[](x)", "rm *.log *.tmp"])
    expect(within(d).queryByRole("img")).toBeNull()
    expect(within(d).queryByRole("link")).toBeNull()
    expect(d.querySelector("em")).toBeNull()
  })

  it("shows the title and the body as typed with no markdown flag", async () => {
    render(ConfirmDialog)
    confirm({
      title: "Remove **x**?",
      body: "![a](b) and [](x)",
      confirmLabel: "Remove",
      danger: true,
    })
    const d = await dialog("Remove **x**?")
    expect(within(d).getByRole("heading").textContent).toBe("Remove **x**?")
    expect(within(d).getByText("![a](b) and [](x)")).toBeTruthy()
    expect(d.querySelector("strong")).toBeNull()
  })

  it("renders a gateway reply with icons and a label with no mark", async () => {
    render(ConfirmDialog)
    confirm({
      title: '🗑 **Delete "Old"?**',
      body: "This deletes **12** messages. You cannot undo this.",
      confirmLabel: "🗑 Delete",
      danger: true,
      markdown: true,
    })
    const d = await dialog(/Delete\s*"Old"\?/)
    const head = within(d).getByRole("heading")
    expect(head.querySelector("strong")?.textContent).toBe('Delete "Old"?')
    expect(head.querySelector("p")).toBeNull()
    expect(head.querySelector("svg.glyph")).not.toBeNull()
    expect(head.textContent).not.toContain("🗑")
    expect(within(d).getByText("12").tagName).toBe("STRONG")
    const buttons = within(d).getAllByRole("button")
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["Cancel", "Delete"])
    expect(buttons[1].className).toContain("bg-destructive")
  })

  it("describes the dialog with the body and keeps Tab inside it", async () => {
    const user = userEvent.setup()
    render(ConfirmDialog)
    confirm({ title: "Restart?", body: "The chat stops for 10 s.", confirmLabel: "Restart" })
    const d = await dialog("Restart?")
    const id = d.getAttribute("aria-describedby")
    expect(document.getElementById(id!)?.textContent).toContain("The chat stops for 10 s.")
    await waitFor(() => expect(focused()).toBe("Restart"))
    await user.tab()
    expect(d.contains(document.activeElement)).toBe(true)
    await user.tab()
    expect(d.contains(document.activeElement)).toBe(true)
  })

  it("gives the focus back to the opener", async () => {
    const user = userEvent.setup()
    const page = opener("Restart…")
    page.focus()
    render(ConfirmDialog)
    const p = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    const d = await dialog("Restart?")
    await user.click(within(d).getByRole("button", { name: "Restart" }))
    await p
    await waitFor(() => expect(document.activeElement).toBe(page))
  })

  it("gives the focus to the remembered opener when the opener is gone", async () => {
    const user = userEvent.setup()
    const trigger = opener("More for nightly")
    const item = opener("Delete…")
    item.focus()
    rememberOpener(trigger)
    render(ConfirmDialog)
    const p = confirm({ title: "Delete the job?", body: "", confirmLabel: "Delete", danger: true })
    const d = await dialog("Delete the job?")
    item.remove()
    await user.click(within(d).getByRole("button", { name: "Cancel" }))
    await expect(p).resolves.toBe(false)
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it("answers an open confirm with false when a second confirm opens, and shows the new title", async () => {
    render(ConfirmDialog)
    const a = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await dialog("Restart?")
    const b = confirm({ title: "Delete the job?", body: "", confirmLabel: "Delete", danger: true })
    await expect(a).resolves.toBe(false)
    await dialog("Delete the job?")
    expect(screen.getAllByRole("alertdialog")).toHaveLength(1)
    expect(screen.queryByRole("heading", { name: "Restart?" })).toBeNull()
    await waitFor(() => expect(focused()).toBe("Cancel"))
    pendingConfirm.resolve?.(false)
    await expect(b).resolves.toBe(false)
  })

  it("keeps the focus in the dialog when a new card arrives", async () => {
    const Cards = defineComponent({
      render: () =>
        h(
          "ul",
          cards.open.map((c) => h("li", { key: c.id }, h("button", null, c.header))),
        ),
    })
    const Host = defineComponent({ render: () => h("div", [h(Cards), h(ConfirmDialog)]) })
    render(Host)
    const p = confirm({ title: "Delete the job?", body: "", confirmLabel: "Delete", danger: true })
    const d = await dialog("Delete the job?")
    await waitFor(() => expect(focused()).toBe("Cancel"))
    applyCardEvents([
      {
        name: "card",
        data: { id: "c9", kind: "approval", header: "⚠️ Run rm -rf build/?" } as Card,
      },
    ])
    await nextTick()
    expect(cards.open).toHaveLength(1)
    expect(d.contains(document.activeElement)).toBe(true)
    expect(focused()).toBe("Cancel")
    pendingConfirm.resolve?.(false)
    await p
  })
})

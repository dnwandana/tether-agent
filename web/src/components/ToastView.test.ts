import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("../api/client", () => ({
  runCommand: vi.fn(async () => ({
    request_id: "r1",
    text: "🗄 Unarchived",
    buttons: [],
    edit: false,
  })),
}))
import { runCommand } from "../api/client"
import ToastView from "./ToastView.vue"
import { toast, toasts, type Toast } from "../stores/ui"

const mk = (t: Partial<Toast>): Toast => ({
  id: 1,
  text: "",
  kind: "info",
  actions: [],
  sticky: false,
  markdown: false,
  ...t,
})

afterEach(() => {
  toasts.splice(0)
})

describe("ToastView", () => {
  it("shows an ok toast as a status with the check mark", () => {
    render(ToastView, {
      props: { toast: mk({ text: "Saved. Applies to new sessions.", kind: "ok" }) },
    })
    const t = screen.getByRole("status")
    expect(t.textContent).toContain("Saved. Applies to new sessions.")
    expect(t.textContent).toContain("✓")
  })

  it("shows an error toast as an alert with the warning sign", () => {
    render(ToastView, {
      props: { toast: mk({ text: "The gateway is stopping.", kind: "error", sticky: true }) },
    })
    expect(screen.getByRole("alert").textContent).toContain("⚠")
  })

  it("renders Markdown only for reply text", () => {
    const { unmount } = render(ToastView, {
      props: { toast: mk({ text: "**Saved** `a_b`", kind: "ok", markdown: true }) },
    })
    expect(screen.getByText("Saved").tagName).toBe("STRONG")
    unmount()
    render(ToastView, { props: { toast: mk({ text: "**name** of a device" }) } })
    expect(screen.getByText("**name** of a device")).toBeTruthy()
  })

  it("shows raw HTML and a javascript: link of a Markdown toast as text", () => {
    render(ToastView, {
      props: {
        toast: mk({
          text: '<img src=x onerror="alert(1)"> [run](javascript:alert(1))',
          markdown: true,
        }),
      },
    })
    const t = screen.getByRole("status")
    expect(t.textContent).toContain('<img src=x onerror="alert(1)">')
    expect(t.textContent).toContain("run")
    expect(t.querySelector("img")).toBeNull()
    expect(within(t).queryByRole("link")).toBeNull()
  })

  it("runs the actions in order, closes the toast, and has a close button", async () => {
    const user = userEvent.setup()
    toast("🗄 Archived", "ok", {
      actions: [
        { label: "↩️ Undo", data: "/unarchive abc" },
        { label: "Open", data: "/open abc" },
      ],
    })
    render(ToastView, { props: { toast: toasts[0] } })
    const names = screen
      .getAllByRole("button")
      .map((b) => b.getAttribute("aria-label") ?? b.textContent?.trim())
    expect(names).toEqual(["↩️ Undo", "Open", "Close"])
    await user.click(screen.getByRole("button", { name: "↩️ Undo" }))
    expect(runCommand).toHaveBeenCalledWith("/unarchive abc")
    // The reply of the command adds its own toast, so check only that the first toast is gone.
    expect(toasts.some((t) => t.text === "🗄 Archived")).toBe(false)
  })

  it("closes the toast with ✕", async () => {
    const user = userEvent.setup()
    toast("⏳ Busy", "warn")
    render(ToastView, { props: { toast: toasts[0] } })
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(toasts).toHaveLength(0)
  })
})

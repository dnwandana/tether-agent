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

// The icons of the toast, without the icons of its buttons.
const icons = (t: Element) => [...t.querySelectorAll("svg")].filter((s) => !s.closest("button"))

describe("ToastView", () => {
  it("shows an ok toast as a status with the CircleCheck icon", () => {
    render(ToastView, {
      props: { toast: mk({ text: "Saved. Applies to new sessions.", kind: "ok" }) },
    })
    const t = screen.getByRole("status")
    expect(t.textContent).toContain("Saved. Applies to new sessions.")
    expect(t.textContent).not.toContain("✓")
    expect(t.querySelector("svg.lucide-circle-check")!.classList.contains("text-success")).toBe(
      true,
    )
  })

  it("shows an error toast as an alert with the CircleAlert icon", () => {
    render(ToastView, {
      props: { toast: mk({ text: "The gateway is stopping.", kind: "error", sticky: true }) },
    })
    const t = screen.getByRole("alert")
    expect(t.textContent).not.toContain("⚠")
    expect(t.querySelector("svg.lucide-circle-alert")!.classList.contains("text-destructive")).toBe(
      true,
    )
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

  it("drops a lead mark of the same tone from a warn reply", () => {
    render(ToastView, {
      props: { toast: mk({ text: "⚠️ The list changed.", kind: "warn", markdown: true }) },
    })
    const t = screen.getByRole("status")
    expect(icons(t)).toHaveLength(1)
    expect(t.querySelector("svg.lucide-triangle-alert")).not.toBeNull()
    expect(t.textContent!.trim()).toBe("The list changed.")
  })

  it("keeps a lead mark of another tone as an icon in an info reply", () => {
    render(ToastView, {
      props: { toast: mk({ text: "⏸ Paused job x", kind: "info", markdown: true }) },
    })
    const t = screen.getByRole("status")
    expect(icons(t)).toHaveLength(2)
    expect(t.querySelector("svg.lucide-info")).not.toBeNull()
    expect(t.querySelector("svg.glyph")).not.toBeNull()
    expect(t.textContent).toContain("Paused job x")
    expect(t.textContent).not.toContain("⏸")
  })

  it("treats a lead mark of a plain toast in the same way", () => {
    const { unmount } = render(ToastView, {
      props: { toast: mk({ text: "⚠️ The list changed.", kind: "warn" }) },
    })
    let t = screen.getByRole("status")
    expect(icons(t)).toHaveLength(1)
    expect(t.textContent!.trim()).toBe("The list changed.")
    unmount()
    render(ToastView, { props: { toast: mk({ text: "⏸ Paused job x", kind: "info" }) } })
    t = screen.getByRole("status")
    expect(icons(t)).toHaveLength(2)
    expect(t.querySelector("svg.lucide-pause")).not.toBeNull()
    expect(t.textContent!.trim()).toBe("Paused job x")
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
    expect(screen.getByRole("status").textContent).not.toContain("🗄")
    const names = screen
      .getAllByRole("button")
      .map((b) => b.getAttribute("aria-label") ?? b.textContent?.trim())
    expect(names).toEqual(["Undo", "Open", "Close"])
    const close = screen.getByRole("button", { name: "Close" })
    expect(close.textContent!.trim()).toBe("")
    expect(close.querySelector("svg.lucide-x")).not.toBeNull()
    await user.click(screen.getByRole("button", { name: "Undo" }))
    expect(runCommand).toHaveBeenCalledWith("/unarchive abc")
    // The reply of the command adds its own toast, so check only that the first toast is gone.
    expect(toasts.some((t) => t.text === "🗄 Archived")).toBe(false)
  })

  it("shows the current state of a Go action label as a pressed button", () => {
    render(ToastView, {
      props: {
        toast: mk({ text: "Pick.", actions: [{ label: "✓ gpt-5", data: "/model gpt-5" }] }),
      },
    })
    const b = screen.getByRole("button", { name: "gpt-5" })
    expect(b.getAttribute("aria-pressed")).toBe("true")
    expect(b.querySelector("svg.lucide-check")).not.toBeNull()
  })

  it("closes the toast with the close button", async () => {
    const user = userEvent.setup()
    toast("⏳ Busy", "warn")
    render(ToastView, { props: { toast: toasts[0] } })
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(toasts).toHaveLength(0)
  })
})

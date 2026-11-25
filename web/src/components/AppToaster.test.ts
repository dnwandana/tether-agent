import { fireEvent, render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { nextTick } from "vue"
import { toast as sonner } from "vue-sonner"

vi.mock("../api/client", () => ({ runCommand: vi.fn() }))
import AppToaster from "./AppToaster.vue"
import { closeToast, prefs, toast, toasts } from "../stores/ui"

const list = () => document.querySelector<HTMLElement>("[data-sonner-toaster]")!

beforeEach(() => {
  prefs.theme = "dark"
})
afterEach(() => {
  toasts.splice(0)
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe("AppToaster", () => {
  it("renders the toaster at the bottom right, above modals, in the resolved theme", () => {
    render(AppToaster)
    expect(list().getAttribute("data-y-position")).toBe("bottom")
    expect(list().getAttribute("data-x-position")).toBe("right")
    expect(list().getAttribute("data-sonner-theme")).toBe("dark")
    expect(list().getAttribute("style")).toContain("pointer-events: auto")
  })

  it("shows a store toast through vue-sonner with no timer of its own", async () => {
    const custom = vi.spyOn(sonner, "custom")
    toast("Kept from before the mount.", "info")
    render(AppToaster)
    toast("Saved.", "ok")
    expect(await screen.findByText("Saved.")).toBeTruthy()
    expect(await screen.findByText("Kept from before the mount.")).toBeTruthy()
    expect(custom).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ id: toasts[1].id, duration: Infinity }),
    )
  })

  it("dismisses the vue-sonner toast when the store closes it", async () => {
    const dismiss = vi.spyOn(sonner, "dismiss")
    render(AppToaster)
    toast("The gateway is busy.", "warn")
    await screen.findByText("The gateway is busy.")
    const id = toasts[0].id
    closeToast(id)
    await nextTick()
    expect(dismiss).toHaveBeenCalledWith(id)
  })

  it("closes a toast from its close button", async () => {
    const user = userEvent.setup()
    render(AppToaster)
    toast("The gateway is busy.", "warn")
    await screen.findByText("The gateway is busy.")
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(toasts).toHaveLength(0)
  })

  it("holds the timers while the pointer or the focus is on the toasts", async () => {
    vi.useFakeTimers()
    render(AppToaster)
    toast("Saved.", "ok")
    await nextTick()
    const wrap = list().closest("section")!.parentElement!
    await fireEvent.mouseEnter(wrap)
    vi.advanceTimersByTime(10_000)
    expect(toasts).toHaveLength(1)
    await fireEvent.mouseLeave(wrap)
    vi.advanceTimersByTime(6000)
    expect(toasts).toHaveLength(0)
    toast("Saved again.", "ok")
    await nextTick()
    await fireEvent.focusIn(wrap)
    vi.advanceTimersByTime(10_000)
    expect(toasts).toHaveLength(1)
    await fireEvent.focusOut(wrap)
    vi.advanceTimersByTime(6000)
    expect(toasts).toHaveLength(0)
  })
})

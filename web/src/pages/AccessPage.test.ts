import { describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
  setSessionHeader: vi.fn(),
}))
vi.mock("../stores/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../stores/ui")>()),
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
}))
import { emitEvents } from "../lib/bus"
import { api } from "../api/client"
import { confirm, toast } from "../stores/ui"
import AccessPage from "./AccessPage.vue"
import { fullTime, shortTime } from "../lib/time"

const Blank = { render: () => null }
const view = {
  pending: [
    {
      platform: "telegram",
      user_id: "58210993",
      name: "Mira K.",
      status: "pending",
      since: "2026-11-20T09:56:00Z",
      expires: "2026-11-20T10:56:00Z",
    },
  ],
  approved: [
    {
      platform: "telegram",
      user_id: "73300412",
      name: "Arif (laptop)",
      status: "approved",
      since: "2026-11-04T10:00:00Z",
      expires: "0001-01-01T00:00:00Z",
    },
  ],
  tries_left: 5,
  pairing_on: true,
  locked_until: null,
  revoked: [
    {
      platform: "telegram",
      user_id: "41100077",
      name: "Old phone",
      status: "revoked",
      since: "2026-11-01T10:00:00Z",
      expires: "0001-01-01T00:00:00Z",
    },
  ],
}

const people = (status: string, n: number) =>
  Array.from({ length: n }, (_, i) => ({
    platform: "telegram",
    user_id: String(9000 + i),
    name: `Person ${i + 1}`,
    status,
    since: "2026-11-01T10:00:00Z",
    expires: "2026-11-20T10:56:00Z",
  }))

// Renders the page on /access with today's two routes (access: AccessPage, start: Blank).
async function mk(user = userEvent.setup()) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/access", name: "access", component: AccessPage },
      { path: "/start", name: "start", component: Blank },
    ],
  })
  await router.push("/access")
  const r = render(AccessPage, { global: { plugins: [router] } })
  await flushPromises()
  return Object.assign(r, { user })
}
const button = (name: string) => screen.getByRole("button", { name })
const code = () => screen.getByRole("textbox", { name: "Code of Mira K." })
const disabled = (el: HTMLElement) => el.hasAttribute("disabled")
const body = () => document.body.textContent ?? ""

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string) => (m === "GET" ? view : {}) as any)
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
})

describe("AccessPage", () => {
  it("shows the approved date in the shared short format, with the full time as a tooltip", async () => {
    await mk()
    const cell = screen.getByTitle(fullTime("2026-11-20T09:56:00Z"))
    expect(cell.textContent).toBe(shortTime("2026-11-20T09:56:00Z"))
    expect(cell.classList.contains("whitespace-nowrap")).toBe(true)
  })

  it("shows a short title and a normal text when no request waits", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, pending: [] } : {}) as any,
    )
    await mk()
    expect(screen.getByText("No pairing requests.")).toBeTruthy()
    expect(
      screen.getByText(
        "A person who sends /start to the bot gets a code. They tell you the code, and you approve it here.",
      ),
    ).toBeTruthy()
  })

  it("shows the Pending count next to its heading", async () => {
    await mk()
    const h2 = screen.getByRole("heading", { level: 2, name: /^Pending/ })
    expect(within(h2).getByText("1")).toBeTruthy()
  })

  it("shows the tries left from the view", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, tries_left: 2 } : {}) as any,
    )
    await mk()
    expect(body()).toContain("(2 tries left)")
  })

  it("approves a code after the owner confirm", async () => {
    const { user } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/pairing")
    expect(body()).toContain("5 wrong codes lock pairing for 1 hour (5 tries left).")
    await user.type(code(), "k7q2mxrp")
    await user.click(button("Approve"))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Approve Mira K.?",
      body: "This person becomes an owner of the agent.",
      confirmLabel: "Approve",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/approve", {
      platform: "telegram",
      code: "K7Q2MXRP",
    })
    expect(toast).toHaveBeenCalledWith("Approved Mira K..")
  })

  it("refuses a short code before a request", async () => {
    const { user } = await mk()
    await user.type(code(), "K7Q2")
    await user.click(button("Approve"))
    expect(toast).toHaveBeenCalledWith("The code has 8 characters.", "error")
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("revokes an approved person", async () => {
    const { user } = await mk()
    await user.click(button("Revoke"))
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Revoke Arif (laptop)?",
      body: "They lose access at once. Their sessions stay.",
      confirmLabel: "Revoke",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/revoke", {
      platform: "telegram",
      user_id: "73300412",
    })
    expect(toast).toHaveBeenCalledWith("Revoked Arif (laptop).")
  })

  it("says that pairing needs Telegram and hides the tables", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, pairing_on: false } : {}) as any,
    )
    await mk()
    expect(body()).toContain("Pairing needs Telegram.")
    expect(body()).toContain("telegram.enabled: true")
    expect(screen.queryAllByRole("table")).toHaveLength(0)
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
  })

  it("shows the lockout and disables Approve", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) =>
        (m === "GET"
          ? { ...view, locked_until: "2099-11-20T11:00:00Z", tries_left: 0 }
          : {}) as any,
    )
    await mk()
    const hhmm = shortTime("2099-11-20T11:00:00Z")
    expect(body()).toContain(`Pairing is locked until ${hhmm} after too many wrong codes.`)
    expect(disabled(button("Approve"))).toBe(true)
  })

  it("runs one approve for two Enter keys", async () => {
    let answer!: (v: boolean) => void
    vi.mocked(confirm).mockReturnValue(new Promise<boolean>((r) => (answer = r)))
    const { user } = await mk()
    await user.type(code(), "k7q2mxrp")
    await user.keyboard("{Enter}{Enter}")
    expect(disabled(button("Approve"))).toBe(true)
    answer(true)
    await flushPromises()
    expect(confirm).toHaveBeenCalledTimes(1)
    expect(api).toHaveBeenCalledTimes(3)
  })

  it("enables Approve at the end of the lockout", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    try {
      const until = new Date(Date.now() + 30_000).toISOString()
      vi.mocked(api).mockImplementation(
        async (m: string) => (m === "GET" ? { ...view, locked_until: until } : {}) as any,
      )
      await mk()
      expect(disabled(button("Approve"))).toBe(true)
      vi.advanceTimersByTime(31_000)
      await flushPromises()
      expect(disabled(button("Approve"))).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it("hides the count when the tries left are unknown", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, tries_left: null } : {}) as any,
    )
    await mk()
    expect(body()).toContain("5 wrong codes lock pairing for 1 hour.")
    expect(body()).not.toContain("left)")
  })

  it("denies a pending request with the revoke route", async () => {
    const { user } = await mk()
    await user.click(button("Deny"))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/revoke", {
      platform: "telegram",
      user_id: "58210993",
    })
    expect(toast).toHaveBeenCalledWith("Denied Mira K..")
  })

  it("lists the revoked people with no button", async () => {
    await mk()
    const sec = screen.getByRole("region", { name: "Revoked" })
    expect(sec.textContent).toContain("Old phone")
    expect(sec.textContent).toContain("They can send /start again.")
    expect(within(sec).queryAllByRole("button")).toHaveLength(0)
  })

  it("labels the code input and approves on Enter", async () => {
    const { user } = await mk()
    expect(code().getAttribute("autocapitalize")).toBe("characters")
    expect(code().getAttribute("autocomplete")).toBe("off")
    await user.type(code(), "k7q2mxrp{Enter}")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/approve", {
      platform: "telegram",
      code: "K7Q2MXRP",
    })
  })

  it("shows the empty approved list and reloads on a sign-in card", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, approved: [] } : {}) as any,
    )
    await mk()
    expect(screen.getByText("Nobody is approved yet.")).toBeTruthy()
    vi.mocked(api).mockClear()
    emitEvents([
      {
        name: "card.done",
        data: { id: "c1", kind: "approval", header: "", preview: "", session_id: "", at: "" },
      },
    ])
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
    emitEvents([
      {
        name: "card.done",
        data: { id: "c2", kind: "signin", header: "", preview: "", session_id: "", at: "" },
      },
    ])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/pairing")
  })

  it.each([
    ["pending", "access-pending", "Pending requests"],
    ["approved", "access-approved", "Approved people"],
    ["revoked", "access-revoked", "Revoked people"],
  ] as const)("pages the %s list with its own stored size", async (list, key, label) => {
    localStorage.setItem(`tether.rows.${key}`, "10")
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, [list]: people(list, 11) } : {}) as any,
    )
    const { user } = await mk()
    const nav = screen.getByRole("navigation", { name: `${label} pages` })
    expect(screen.getByText("1–10 of 11")).toBeTruthy()
    expect(screen.getByText("Person 1")).toBeTruthy()
    expect(screen.queryByText("Person 11")).toBeNull()
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    expect(screen.getByText("11–11 of 11")).toBeTruthy()
    expect(screen.getByText("Person 11")).toBeTruthy()
  })
})

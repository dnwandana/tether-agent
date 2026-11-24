import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
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

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/access", name: "access", component: AccessPage },
      { path: "/start", name: "start", component: Blank },
    ],
  })
  await router.push("/access")
  const w = mount(AccessPage, { global: { plugins: [router] } })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

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
  it("shows the tries left from the view", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, tries_left: 2 } : {}) as any,
    )
    const w = await mk()
    expect(w.text()).toContain("(2 tries left)")
  })

  it("approves a code after the owner confirm", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/pairing")
    expect(w.text()).toContain("5 wrong codes lock pairing for 1 hour (5 tries left).")
    await w.find("input[aria-label='Code of Mira K.']").setValue("k7q2mxrp")
    await btn(w, "Approve").trigger("click")
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
    const w = await mk()
    await w.find("input[aria-label='Code of Mira K.']").setValue("K7Q2")
    await btn(w, "Approve").trigger("click")
    expect(toast).toHaveBeenCalledWith("The code has 8 characters.", "error")
    expect(api).toHaveBeenCalledTimes(1)
  })

  it("revokes an approved person", async () => {
    const w = await mk()
    await btn(w, "Revoke").trigger("click")
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
    const w = await mk()
    expect(w.text()).toContain("Pairing needs Telegram.")
    expect(w.text()).toContain("telegram.enabled: true")
    expect(w.findAll("table")).toHaveLength(0)
    expect(w.findAll("button").some((b) => b.text() === "Approve")).toBe(false)
  })

  it("shows the lockout and disables Approve", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) =>
        (m === "GET"
          ? { ...view, locked_until: "2099-11-20T11:00:00Z", tries_left: 0 }
          : {}) as any,
    )
    const w = await mk()
    const hhmm = new Date("2099-11-20T11:00:00Z").toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    })
    expect(w.text()).toContain(`Pairing is locked until ${hhmm} after too many wrong codes.`)
    expect(btn(w, "Approve").attributes("disabled")).toBeDefined()
  })

  it("runs one approve for two Enter keys", async () => {
    let answer!: (v: boolean) => void
    vi.mocked(confirm).mockReturnValue(new Promise<boolean>((r) => (answer = r)))
    const w = await mk()
    const input = w.find("input[aria-label='Code of Mira K.']")
    await input.setValue("k7q2mxrp")
    await input.trigger("keydown", { key: "Enter" })
    await input.trigger("keydown", { key: "Enter" })
    expect(btn(w, "Approve").attributes("disabled")).toBeDefined()
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
      const w = await mk()
      expect(btn(w, "Approve").attributes("disabled")).toBeDefined()
      vi.advanceTimersByTime(31_000)
      await flushPromises()
      expect(btn(w, "Approve").attributes("disabled")).toBeUndefined()
    } finally {
      vi.useRealTimers()
    }
  })

  it("hides the count when the tries left are unknown", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string) => (m === "GET" ? { ...view, tries_left: null } : {}) as any,
    )
    const w = await mk()
    expect(w.text()).toContain("5 wrong codes lock pairing for 1 hour.")
    expect(w.text()).not.toContain("left)")
  })

  it("denies a pending request with the revoke route", async () => {
    const w = await mk()
    await btn(w, "Deny").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/pairing/revoke", {
      platform: "telegram",
      user_id: "58210993",
    })
    expect(toast).toHaveBeenCalledWith("Denied Mira K..")
  })

  it("lists the revoked people with no button", async () => {
    const w = await mk()
    const sec = w.findAll(".revoked")[0]
    expect(sec.text()).toContain("Old phone")
    expect(sec.text()).toContain("They can send /start again.")
    expect(sec.findAll("button")).toHaveLength(0)
  })

  it("labels the code input and approves on Enter", async () => {
    const w = await mk()
    const input = w.find("input[aria-label='Code of Mira K.']")
    expect(input.attributes("autocapitalize")).toBe("characters")
    expect(input.attributes("autocomplete")).toBe("off")
    await input.setValue("k7q2mxrp")
    await input.trigger("keydown", { key: "Enter" })
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
    const w = await mk()
    expect(w.text()).toContain("Nobody is approved yet.")
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
})

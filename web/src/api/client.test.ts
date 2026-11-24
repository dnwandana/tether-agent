import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ApiError, api, onSignedOut, postForm, runCommand, setSessionHeader } from "./client"

function res(status: number, body?: unknown) {
  return { ok: status >= 200 && status < 300, status, statusText: "", json: async () => body }
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.clear()
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
})
afterEach(() => vi.unstubAllGlobals())

describe("api", () => {
  it("sends the session header and a JSON body", async () => {
    setSessionHeader("h1")
    fetchMock.mockResolvedValue(res(200, { ok: true }))
    await expect(api("POST", "/api/cards/c1", { answer: "once" })).resolves.toEqual({ ok: true })
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe("/api/cards/c1")
    expect(init.method).toBe("POST")
    expect(init.credentials).toBe("same-origin")
    expect(init.headers["X-Tether-Session"]).toBe("h1")
    expect(init.headers["Content-Type"]).toBe("application/json")
    expect(init.body).toBe('{"answer":"once"}')
    expect(localStorage.getItem("tether.session")).toBe("h1")
  })

  it("sends no header and no body when there is none", async () => {
    fetchMock.mockResolvedValue(res(200, []))
    await api("GET", "/api/jobs")
    const [, init] = fetchMock.mock.calls[0]
    expect(init.headers["X-Tether-Session"]).toBeUndefined()
    expect(init.body).toBeUndefined()
  })

  it("throws ApiError with the server text", async () => {
    fetchMock.mockResolvedValue(res(409, { error: "This card is already answered." }))
    const err = await api("POST", "/api/cards/c1", { answer: "deny" }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.message).toBe("This card is already answered.")
    expect(err.serverText).toBe("This card is already answered.")
  })

  it("uses the status when the body has no error text", async () => {
    fetchMock.mockResolvedValue({
      ok: false,
      status: 502,
      statusText: "",
      json: async () => {
        throw new SyntaxError("x")
      },
    })
    const err = await api("GET", "/api/usage").catch((e) => e)
    expect(err.message).toBe("The gateway failed. See the log on System operations.")
    expect(err.serverText).toBeNull()
  })

  it("turns a network failure into an ApiError with status 0", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"))
    const err = await api("GET", "/api/jobs").catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(0)
    expect(err.serverText).toBeNull()
    expect(err.message).toBe("Cannot reach the gateway. Check the network, then try again.")
    const form = await postForm("/api/messages", new FormData()).catch((e) => e)
    expect(form.status).toBe(0)
  })

  it("keeps the fixed text on a 401 of a sign-in path with no JSON body", async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 401, statusText: "", json: async () => null })
    const err = await api("POST", "/auth/signin", { code: "K7QF-2M9X" }).catch((e) => e)
    expect(err.message).toBe("The code is not valid, or it expired.")
  })

  it("ends the session on a 401 of an API path", async () => {
    setSessionHeader("h1")
    const out = vi.fn()
    onSignedOut(out)
    fetchMock.mockResolvedValue(res(401, { error: "no session" }))
    const err = await api("GET", "/api/jobs").catch((e) => e)
    expect(err.status).toBe(401)
    expect(err.message).toBe("Your session ended. Request access again.")
    expect(out).toHaveBeenCalledTimes(1)
    expect(localStorage.getItem("tether.session")).toBeNull()
  })

  it("keeps the session on a 401 of a sign-in path", async () => {
    setSessionHeader("h1")
    const out = vi.fn()
    onSignedOut(out)
    fetchMock.mockResolvedValue(res(401, { error: "The code is not valid, or it expired." }))
    const err = await api("POST", "/auth/signin", { code: "K7QF-2M9X" }).catch((e) => e)
    expect(err.message).toBe("The code is not valid, or it expired.")
    expect(out).not.toHaveBeenCalled()
    expect(localStorage.getItem("tether.session")).toBe("h1")
  })
})

describe("runCommand", () => {
  it("posts the text and the button, and returns the 202 request id", async () => {
    fetchMock.mockResolvedValue(res(202, { request_id: "r1" }))
    await expect(runCommand("/jobs pause 12", 0)).resolves.toEqual({ request_id: "r1" })
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe("/api/command")
    expect(init.body).toBe('{"text":"/jobs pause 12","button":0}')
  })

  it("leaves out an absent button", async () => {
    fetchMock.mockResolvedValue(
      res(200, { request_id: "r2", text: "Stopped.", buttons: [], edit: false }),
    )
    await runCommand("/stop")
    expect(fetchMock.mock.calls[0][1].body).toBe('{"text":"/stop"}')
  })
})

describe("postForm", () => {
  it("sends the form with no content type", async () => {
    setSessionHeader("h1")
    fetchMock.mockResolvedValue(res(200, { ok: true }))
    const form = new FormData()
    form.append("text", "hi")
    await postForm("/api/messages", form)
    const [, init] = fetchMock.mock.calls[0]
    expect(init.body).toBe(form)
    expect(init.headers["Content-Type"]).toBeUndefined()
    expect(init.headers["X-Tether-Session"]).toBe("h1")
  })
})

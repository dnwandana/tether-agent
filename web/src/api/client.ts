import { friendlyError } from "../lib/errors"
import type { ReplyView } from "./types.gen"

const STORAGE_KEY = "tether.session"
const SIGNED_OUT_TEXT = "Your session ended. Request access again."

// ApiError carries the HTTP status and the error text of the server.
export class ApiError extends Error {
  status: number
  // The JSON error sentence of the server, or null.
  serverText: string | null
  constructor(status: number, message: string, serverText: string | null = null) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.serverText = serverText
  }
}

const signedOutHandlers: (() => void)[] = []

// Adds a function that runs when the session ends.
export const onSignedOut = (f: () => void): void => {
  signedOutHandlers.push(f)
}

// Returns the stored session header value, or null.
export function sessionHeader(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}

export function setSessionHeader(v: string | null): void {
  try {
    if (v === null) localStorage.removeItem(STORAGE_KEY)
    else localStorage.setItem(STORAGE_KEY, v)
  } catch {
    // A private window can refuse storage.
  }
}

function baseHeaders(): Record<string, string> {
  const headers: Record<string, string> = { Accept: "application/json" }
  const h = sessionHeader()
  if (h) headers["X-Tether-Session"] = h
  return headers
}

async function handle<T>(r: Response, path: string): Promise<T> {
  if (r.status === 401 && path.startsWith("/api/")) {
    setSessionHeader(null)
    for (const f of signedOutHandlers) f()
    throw new ApiError(401, SIGNED_OUT_TEXT, null)
  }
  if (!r.ok) {
    let serverText: string | null = null
    try {
      const body = await r.json()
      if (body && typeof body.error === "string" && body.error !== "") serverText = body.error
    } catch {
      // The body is not JSON. The status table gives the text.
    }
    throw new ApiError(
      r.status,
      friendlyError({ status: r.status, serverText, message: "" }, path),
      serverText,
    )
  }
  if (r.status === 204) return undefined as T
  return (await r.json()) as T
}

// Calls fetch. A network failure (a TypeError) becomes an ApiError with status 0.
async function send(path: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(path, init)
  } catch (e) {
    if (e instanceof TypeError) throw new ApiError(0, friendlyError(e, path), null)
    throw e
  }
}

export async function api<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  const headers = baseHeaders()
  const init: RequestInit = { method, headers, credentials: "same-origin" }
  if (body !== undefined) {
    headers["Content-Type"] = "application/json"
    init.body = JSON.stringify(body)
  }
  return handle<T>(await send(path, init), path)
}

export async function postForm<T>(path: string, form: FormData): Promise<T> {
  const init: RequestInit = {
    method: "POST",
    headers: baseHeaders(),
    credentials: "same-origin",
    body: form,
  }
  return handle<T>(await send(path, init), path)
}

export function runCommand(
  text: string,
  button?: number,
): Promise<ReplyView | { request_id: string }> {
  return api("POST", "/api/command", button === undefined ? { text } : { text, button })
}

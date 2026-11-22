import type { ReplyView } from "./types.gen"

const STORAGE_KEY = "tether.session"
const SIGNED_OUT_TEXT = "Your session ended. Request access again."

// ApiError carries the HTTP status and the error text of the server.
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
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
    throw new ApiError(401, SIGNED_OUT_TEXT)
  }
  if (!r.ok) {
    let text = `HTTP ${r.status}`
    try {
      const body = await r.json()
      if (body && typeof body.error === "string" && body.error !== "") text = body.error
    } catch {
      // The body is not JSON. Keep the status text.
    }
    throw new ApiError(r.status, text)
  }
  if (r.status === 204) return undefined as T
  return (await r.json()) as T
}

export async function api<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  const headers = baseHeaders()
  const init: RequestInit = { method, headers, credentials: "same-origin" }
  if (body !== undefined) {
    headers["Content-Type"] = "application/json"
    init.body = JSON.stringify(body)
  }
  return handle<T>(await fetch(path, init), path)
}

export async function postForm<T>(path: string, form: FormData): Promise<T> {
  const init: RequestInit = {
    method: "POST",
    headers: baseHeaders(),
    credentials: "same-origin",
    body: form,
  }
  return handle<T>(await fetch(path, init), path)
}

export function runCommand(
  text: string,
  button?: number,
): Promise<ReplyView | { request_id: string }> {
  return api("POST", "/api/command", button === undefined ? { text } : { text, button })
}

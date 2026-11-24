export const TEXT_OFFLINE = "Cannot reach the gateway. Check the network, then try again."
export const TEXT_SIGNED_OUT = "Your session ended. Request access again."
export const TEXT_BAD_CODE = "The code is not valid, or it expired."
export const TEXT_TOO_LARGE = "The upload is too large."
export const TEXT_TOO_MANY = "Too many requests. Wait, then try again."
export const TEXT_GATEWAY_FAILED = "The gateway failed. See the log on System operations."
export const TEXT_MIC_BLOCKED = "The browser blocked the microphone."
export const TEXT_NO_MIC = "No microphone found."

type ApiShape = { status: number; serverText: string | null; message?: string }

// Reads the error by shape, so this module does not import the API client.
function isApiShape(e: unknown): e is ApiShape {
  if (typeof e !== "object" || e === null) return false
  const o = e as Record<string, unknown>
  return typeof o.status === "number" && (o.serverText === null || typeof o.serverText === "string")
}

// Returns the sentence that the page shows for the error e. path is the request path, when
// the error came from a request. A JSON error sentence of the server wins over the table.
export function friendlyError(e: unknown, path?: string): string {
  if (isApiShape(e)) {
    if (e.serverText) return e.serverText
    const s = e.status
    if (s === 0) return TEXT_OFFLINE
    if (s === 401) return path?.startsWith("/auth/signin") ? TEXT_BAD_CODE : TEXT_SIGNED_OUT
    if (s === 413) return TEXT_TOO_LARGE
    if (s === 429) return TEXT_TOO_MANY
    if (s >= 500 && s <= 599) return TEXT_GATEWAY_FAILED
    return e.message ? e.message : `HTTP ${s}`
  }
  if (e instanceof TypeError) return TEXT_OFFLINE
  if (typeof e === "object" && e !== null) {
    const name = (e as { name?: unknown }).name
    if (name === "NotAllowedError") return TEXT_MIC_BLOCKED
    if (name === "NotFoundError") return TEXT_NO_MIC
  }
  if (e instanceof Error && e.message) return e.message
  return String(e)
}

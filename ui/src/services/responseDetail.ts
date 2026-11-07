/** The longest server error text shown as is; longer ones are cut. */
const MAX_DETAIL = 300;

/** bodyDetail is what of a failed response's body to show the user: the
 * server's own short error (plain text, or a JSON object's error or
 * message), or "" when the body has none worth showing, such as a proxy's
 * HTML error page. */
export function bodyDetail(body: string, contentType: string): string {
  const text = body.trim();
  if (!text) return "";
  if (/json/i.test(contentType) || text.startsWith("{")) {
    try {
      const payload = JSON.parse(text) as { error?: unknown; message?: unknown };
      const detail = [payload.error, payload.message].find(
        (value): value is string => typeof value === "string" && !!value.trim(),
      );
      return detail ? bodyDetail(detail, "text/plain") : "";
    } catch {
      if (/json/i.test(contentType)) return "";
    }
  }
  if (/html|xml/i.test(contentType) || text.startsWith("<")) return "";
  const line = text.replace(/\s+/g, " ");
  return line.length > MAX_DETAIL ? `${line.slice(0, MAX_DETAIL - 1)}…` : line;
}

/** readDetail reads a failed response: the server's own error (see
 * bodyDetail) and its status. The full body of anything not shown goes to
 * the console. */
async function readDetail(response: Response): Promise<{ detail: string; status: string }> {
  const body = await response.text().catch(() => "");
  const detail = bodyDetail(body, response.headers.get("Content-Type") ?? "");
  const status = `HTTP ${response.status}${response.statusText ? ` ${response.statusText}` : ""}`;
  if (body.trim() && (!detail || detail.endsWith("…")))
    console.warn(`${response.url || "request"} failed: ${status}`, body);
  return { detail, status };
}

/** responseDetail says why a failed response failed, briefly: the server's
 * own error, else its status. */
export async function responseDetail(response: Response): Promise<string> {
  const { detail, status } = await readDetail(response);
  return detail || status;
}

/** ownError is a failed response's error as the server words it, alone,
 * else prefix and its status. */
export async function ownError(response: Response, prefix: string): Promise<Error> {
  const { detail, status } = await readDetail(response);
  return new Error(detail || `${prefix}: ${status}`);
}

// The one module that calls fetch. It speaks the daemon's contract
// (api/openapi.yaml) and turns every failure into an ApiError that keeps the
// daemon's own sentence. Every URL is relative, so a request can only go to
// the origin that served the page.

/** A failed request: the daemon's problem, an unreachable daemon, or an answer that is neither. */
export class ApiError extends Error {
  /**
   * @param {object} f
   * @param {"problem"|"network"|"protocol"} f.kind
   * @param {string} f.detail the sentence to show, the daemon's own for a problem
   * @param {number} [f.status]
   * @param {string} [f.reason] a code of the closed set, for a problem
   * @param {string} [f.traceId]
   * @param {object} [f.details] the problem's structured details
   * @param {object} [f.store] the store object of a store_unavailable
   */
  constructor({ kind, detail, status = 0, reason = "", traceId = "", details = null, store = null }) {
    super(detail);
    this.name = "ApiError";
    this.kind = kind;
    this.detail = detail;
    this.status = status;
    this.reason = reason;
    this.traceId = traceId;
    this.details = details;
    this.store = store;
  }

  /** The request may or may not have been applied: the daemon was unreachable, or the store said it does not know. */
  get outcomeUnknown() {
    return this.kind === "network" || (this.reason === "store_unavailable" && this.store?.state !== "read_only");
  }

  /** The store is read-only; only a restart clears it. */
  get readOnly() {
    return this.reason === "store_unavailable" && this.store?.state === "read_only";
  }
}

const UNREACHABLE = "The service could not be reached, so the outcome of this request is unknown.";

function traceIdOf(headers, problem) {
  if (problem && typeof problem.trace_id === "string" && problem.trace_id) return problem.trace_id;
  const tr = headers?.get?.("traceresponse") ?? "";
  const parts = tr.split("-");
  return parts.length >= 4 ? parts[1] : "";
}

/**
 * @param {object} [o]
 * @param {typeof fetch} [o.fetch]
 * @param {string} [o.base] a path prefix, empty for the page's own origin
 */
export function createApi({ fetch: fetchImpl = globalThis.fetch?.bind(globalThis), base = "" } = {}) {
  async function request(method, path, body) {
    const headers = { Accept: "application/json, application/problem+json", "X-Client": "web" };
    const init = { method, headers, cache: "no-store", credentials: "omit" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetchImpl(base + path, init);
    } catch {
      throw new ApiError({ kind: "network", detail: UNREACHABLE });
    }
    let text;
    try {
      text = await res.text();
    } catch {
      throw new ApiError({ kind: "network", detail: UNREACHABLE, status: res.status });
    }
    let json = null;
    try {
      json = text === "" ? null : JSON.parse(text);
    } catch {
      json = null;
    }
    if (res.ok) {
      if (json === null || typeof json !== "object") {
        throw new ApiError({
          kind: "protocol",
          status: res.status,
          detail: `The service answered ${res.status} with something that is not JSON.`,
        });
      }
      return json;
    }
    if (json && typeof json.reason === "string" && typeof json.detail === "string") {
      throw new ApiError({
        kind: "problem",
        status: res.status,
        reason: json.reason,
        detail: json.detail,
        traceId: traceIdOf(res.headers, json),
        details: json.details ?? null,
        store: json.store ?? json.details?.store ?? null,
      });
    }
    throw new ApiError({
      kind: "protocol",
      status: res.status,
      traceId: traceIdOf(res.headers, null),
      detail: `The service answered ${res.status} with something that is not a problem document.`,
    });
  }

  return {
    getState: () => request("GET", "/api/v1/state"),
    getConfig: () => request("GET", "/api/v1/config"),
    /**
     * @param {{view?: string, from?: string, to?: string, types?: string[]}} [q]
     */
    getEvents(q = {}) {
      const p = new URLSearchParams();
      p.set("view", q.view ?? "corrected");
      if (q.from) p.set("from", q.from);
      if (q.to) p.set("to", q.to);
      if (q.types && q.types.length > 0) p.set("type", q.types.join(","));
      return request("GET", `/api/v1/events?${p.toString()}`);
    },
    /** POST a JSON body to an API path, which is one of the mutation endpoints. */
    post: (path, body) => request("POST", path, body ?? {}),
  };
}

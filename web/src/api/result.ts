import { webWords } from "./hints";

/** Why a request failed, in words a person can act on. */
export type Failure = {
  /** The API's error code, or network_error when it couldn't be reached. */
  code: string;
  message: string;
  hint: string;
  /** The HTTP status; 0 when there was no response. */
  status: number;
  /** The error's details, for codes that have them. */
  details?: Record<string, unknown>;
};

export type Result<T> = { ok: true; data: T } | { ok: false; failure: Failure };

type Response_ = { data?: unknown; error?: unknown; response: Response };

/**
 * Runs one openapi-fetch call and turns its answer into a Result, so pages
 * handle network errors and API errors the same way.
 */
export async function attempt<R extends Response_>(
  call: () => Promise<R>,
): Promise<Result<Exclude<R["data"], undefined> | undefined>> {
  let res: R;
  try {
    res = await call();
  } catch {
    return {
      ok: false,
      failure: {
        code: "network_error",
        message: "Can't reach mailgoose right now.",
        hint: "Check your connection and try again.",
        status: 0,
      },
    };
  }
  if (res.response.ok) {
    return { ok: true, data: res.data as Exclude<R["data"], undefined> | undefined };
  }
  return { ok: false, failure: failureFrom(res.error, res.response.status) };
}

function failureFrom(body: unknown, status: number): Failure {
  if (typeof body === "object" && body !== null && "error" in body) {
    const e = (body as { error: Partial<Failure> & { details?: unknown } }).error;
    if (typeof e?.message === "string") {
      const details = typeof e.details === "object" && e.details !== null ? (e.details as Record<string, unknown>) : undefined;
      const code = e.code ?? "error";
      // The API's hints are written for the CLI; the web app words its own (MG-121).
      const words = webWords(code, e.message, e.hint ?? "");
      return { code, ...words, status, ...(details ? { details } : {}) };
    }
  }
  return {
    code: "unexpected_response",
    message: `Something went wrong (HTTP ${status}).`,
    hint: "Try again in a moment.",
    status,
  };
}

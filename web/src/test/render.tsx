import { cleanup, render } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach } from "vitest";

import { AppRoutes } from "../App";
import { createApiClient } from "../api/client";
import { ApiProvider } from "../api/context";
import { SessionProvider } from "../session";
import { expectAccessible } from "./axe";

// Every page a test renders is checked with axe as the test leaves it
// (WEB-3), so each state the tests reach is covered, not one per page.
let rendered = false;
afterEach(async () => {
  try {
    if (rendered) await expectAccessible();
  } finally {
    rendered = false;
    cleanup();
  }
});

/** A JSON body, or with html set, an HTML page. */
type Reply = { status: number; body?: unknown; html?: string };
/** Answers one request; gets the parsed JSON body, if any, and the URL. */
type Route = (body: unknown, url: URL) => Reply;

export type Call = { method: string; path: string; body: unknown };

/**
 * A fake API: routes keyed "METHOD /path" answer requests, and every request
 * is recorded in calls. Anything unrouted is a 404, like the real API.
 */
export function fakeApi(routes: Record<string, Route>) {
  const calls: Call[] = [];
  const fetch = async (req: Request): Promise<Response> => {
    const url = new URL(req.url);
    const body = await requestBody(req);
    const key = `${req.method} ${url.pathname}`;
    calls.push({ method: req.method, path: url.pathname, body });
    const route = routes[key];
    const reply = route ? route(body, url) : apiError(404, "not_found", "No such endpoint.");
    if (reply.html !== undefined) {
      return new Response(reply.html, { status: reply.status, headers: { "Content-Type": "text/html; charset=utf-8" } });
    }
    return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
      status: reply.status,
      headers: reply.body === undefined ? {} : { "Content-Type": "application/json" },
    });
  };
  return { fetch, calls };
}

/**
 * A request's body: parsed JSON, or for a multipart upload an object of its
 * parts, where a file part is the File itself.
 */
async function requestBody(req: Request): Promise<unknown> {
  if (req.headers.get("Content-Type")?.startsWith("multipart/form-data")) {
    const parts: Record<string, FormDataEntryValue | FormDataEntryValue[]> = {};
    for (const [name, value] of (await req.formData()).entries()) {
      const had = parts[name];
      parts[name] = had === undefined ? value : [...(Array.isArray(had) ? had : [had]), value];
    }
    return parts;
  }
  const text = await req.text();
  return text ? JSON.parse(text) : undefined;
}

export function apiError(status: number, code: string, message: string, hint = ""): Reply {
  return { status, body: { error: { code, message, hint } } };
}

export const session = {
  account: {
    id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
    name: "Pat's Bakery",
    status: "active",
    plan: "free",
    created_at: "2026-11-18T10:00:00Z",
    suspension: null,
    in_review: false,
    sending_mode: "full",
  },
  user: { id: "0b6f5f7e-2a43-4c8e-8d35-1b8a7c6e1a11", email: "pat@example.com", created_at: "2026-11-18T10:00:00Z" },
  expires_at: "2026-12-18T10:00:00Z",
};

export const signedIn: Record<string, Route> = {
  "GET /v1/sessions/current": () => ({ status: 200, body: session }),
};

export const signedOut: Record<string, Route> = {
  "GET /v1/sessions/current": () => apiError(401, "unauthenticated", "Sign in, or send an API key."),
};

function Where() {
  const loc = useLocation();
  return <div data-testid="location">{loc.pathname + loc.search}</div>;
}

/**
 * Renders the whole app at path against a fake API. A path can carry
 * router state, as a link that passes state would.
 */
export function renderApp(path: string | { pathname: string; state: unknown }, fetch: (req: Request) => Promise<Response>) {
  rendered = true;
  const api = createApiClient({ baseUrl: "http://mailgoose.test", fetch });
  return render(
    <ApiProvider client={api}>
      <SessionProvider>
        <MemoryRouter initialEntries={[path]}>
          <AppRoutes />
          <Where />
        </MemoryRouter>
      </SessionProvider>
    </ApiProvider>,
  );
}

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { startedAgo } from "./pages/Device";
import { navPages } from "./pages/nav";
import { apiError, fakeApi, renderApp, session, signedIn, signedOut } from "./test/render";

const location = () => screen.getByTestId("location").textContent;

describe("signing in", () => {
  it("emails a code, then signs in and goes on to ?next=", async () => {
    let current = signedOut["GET /v1/sessions/current"];
    const api = fakeApi({
      "GET /v1/sessions/current": (b, u) => current(b, u),
      "POST /v1/sign-in": () => ({ status: 202, body: { status: "code_sent", message: "Check your email." } }),
      "POST /v1/sessions": (body) => {
        const { code } = body as { code: string };
        if (code !== "042917") return apiError(422, "invalid_code", "That code is wrong or has expired.");
        current = signedIn["GET /v1/sessions/current"];
        return { status: 201, body: session };
      },
    });
    const user = userEvent.setup();
    renderApp("/sign-in?next=%2Femails", api.fetch);

    await user.type(await screen.findByLabelText("Email"), "pat@example.com");
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    expect(await screen.findByText("pat@example.com")).toBeTruthy();

    await user.type(screen.getByLabelText("Code"), "111 111");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect((await screen.findByRole("alert")).textContent).toContain("That code is wrong or has expired.");

    await user.clear(screen.getByLabelText("Code"));
    await user.type(screen.getByLabelText("Code"), "042-917");
    // The browser's own checks take a code pasted with a dash (MG-123).
    expect((screen.getByLabelText("Code") as HTMLInputElement).checkValidity()).toBe(true);
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(location()).toBe("/emails"));
    expect(screen.getByRole("heading", { name: "Emails" })).toBeTruthy();

    expect(api.calls.filter((c) => c.method === "POST")).toEqual([
      { method: "POST", path: "/v1/sign-in", body: { email: "pat@example.com" } },
      { method: "POST", path: "/v1/sessions", body: { email: "pat@example.com", code: "111111" } },
      { method: "POST", path: "/v1/sessions", body: { email: "pat@example.com", code: "042917" } },
    ]);
  });

  it("says how long to wait in minutes when there are too many code requests", async () => {
    const api = fakeApi({
      ...signedOut,
      "POST /v1/sign-in": () => ({
        ...apiError(429, "rate_limited", "Too many code requests.", "Wait 3412 seconds (see Retry-After) and try again."),
        headers: { "Retry-After": "3412" },
      }),
    });
    const user = userEvent.setup();
    renderApp("/sign-in", api.fetch);
    await user.type(await screen.findByLabelText("Email"), "pat@example.com");
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Too many code requests.");
    expect(alert.textContent).toContain("Try again in 57 minutes.");
    expect(alert.textContent).not.toContain("seconds");
    expect(alert.textContent).not.toContain("Retry-After");
  });

  it("words a bad code and too many wrong codes for the web", async () => {
    let wrong = 0;
    const api = fakeApi({
      ...signedOut,
      "POST /v1/sign-in": () => ({ status: 202, body: { status: "code_sent", message: "Check your email." } }),
      "POST /v1/sessions": () =>
        ++wrong === 1
          ? apiError(422, "invalid_code", "That code didn't work.", "Request a new code and use the one in the newest email.")
          : {
              ...apiError(429, "too_many_wrong_codes", "Too many wrong codes for this address.", "Wait 59 seconds (see Retry-After), then request a new code and try again."),
              headers: { "Retry-After": "59" },
            },
    });
    const user = userEvent.setup();
    renderApp("/sign-in", api.fetch);
    await user.type(await screen.findByLabelText("Email"), "pat@example.com");
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    await user.type(await screen.findByLabelText("Code"), "111111");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    let alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("That code didn't work.");
    expect(alert.textContent).toContain("Send a new code and use the one in the newest email.");

    await user.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Too many wrong codes for this address."));
    alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("Try again in a minute.");
    expect(alert.textContent).not.toContain("seconds");
  });

  it("ignores a ?next= that leaves the site", async () => {
    const api = fakeApi(signedIn);
    renderApp("/sign-in?next=https%3A%2F%2Fevil.example%2F", api.fetch);
    await waitFor(() => expect(location()).toBe("/"));
  });

  it("says so when the API can't be reached", async () => {
    const fetch = () => Promise.reject(new TypeError("Failed to fetch"));
    renderApp("/", fetch);
    expect((await screen.findByRole("alert")).textContent).toContain("Can't reach mailgoose right now.");
  });
});

describe("signing up", () => {
  it("sends the account name and terms, then signs in with the code", async () => {
    let current = signedOut["GET /v1/sessions/current"];
    const api = fakeApi({
      "GET /v1/sessions/current": (b, u) => current(b, u),
      "POST /v1/signup": () => ({ status: 202, body: { status: "code_sent", message: "Check your email." } }),
      "POST /v1/sessions": () => {
        current = signedIn["GET /v1/sessions/current"];
        return { status: 201, body: session };
      },
    });
    const user = userEvent.setup();
    renderApp("/signup", api.fetch);

    await user.type(await screen.findByLabelText("Email"), " pat@example.com ");
    await user.type(screen.getByLabelText("Account name"), "Pat's Bakery");
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    await user.type(await screen.findByLabelText("Code"), "042917");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    await waitFor(() => expect(location()).toBe("/"));
    expect(screen.getByRole("heading", { name: "Dashboard" })).toBeTruthy();
    expect(api.calls.find((c) => c.path === "/v1/signup")?.body).toEqual({
      email: "pat@example.com",
      account_name: "Pat's Bakery",
      accepted_terms: true,
    });
  });

  it("says why an account name is refused", async () => {
    const message =
      "An account name can't include \"PayPal\", a well-known brand. Emails sent from mailgoose's shared domain go out under your account's name, so it can't pass for a company or a bank's or shop's own messages. Choose another name.";
    const api = fakeApi({
      ...signedOut,
      "POST /v1/signup": () => ({ status: 422, body: { error: { code: "account_name_not_allowed", message } } }),
    });
    const user = userEvent.setup();
    renderApp("/signup", api.fetch);
    await user.type(await screen.findByLabelText("Email"), "pat@example.com");
    await user.type(screen.getByLabelText("Account name"), "PayPal Support");
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    expect(await screen.findByText(message)).toBeTruthy();
    expect(screen.queryByLabelText("Code")).toBeNull();
  });

  it("won't send the form until the terms are accepted", async () => {
    const api = fakeApi(signedOut);
    const user = userEvent.setup();
    renderApp("/signup", api.fetch);
    await user.type(await screen.findByLabelText("Email"), "pat@example.com");
    await user.type(screen.getByLabelText("Account name"), "Pat's Bakery");
    await user.click(screen.getByRole("button", { name: "Email me a code" }));
    expect(api.calls.some((c) => c.path === "/v1/signup")).toBe(false);
  });
});

describe("signed-in layout", () => {
  it("sends signed-out visitors to sign in and back", async () => {
    renderApp("/contacts", fakeApi(signedOut).fetch);
    await waitFor(() => expect(location()).toBe("/sign-in?next=%2Fcontacts"));
  });

  it("links to every MVP page", async () => {
    renderApp("/", fakeApi(signedIn).fetch);
    const nav = await screen.findByRole("navigation", { name: "Main" });
    const user = userEvent.setup();
    for (const page of navPages) {
      await user.click(within(nav).getByRole("link", { name: page.label }));
      expect(location()).toBe(page.path);
      expect(screen.getByRole("heading", { level: 1, name: page.label })).toBeTruthy();
    }
    expect(screen.getByText("Pat's Bakery")).toBeTruthy();
  });

  it("shows a not-found page for unknown paths", async () => {
    renderApp("/nowhere", fakeApi(signedIn).fetch);
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeTruthy();
  });

  it("signs out", async () => {
    const api = fakeApi({ ...signedIn, "DELETE /v1/sessions/current": () => ({ status: 204 }) });
    const user = userEvent.setup();
    renderApp("/emails", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(location()).toBe("/sign-in"));
    expect(api.calls.some((c) => c.method === "DELETE" && c.path === "/v1/sessions/current")).toBe(true);
  });
});

describe("CLI login approval", () => {
  const login = {
    user_code: "BCDF-GHJK",
    client_name: "pat-laptop",
    created_at: "2026-11-18T10:00:00Z",
    expires_at: "2026-11-18T10:10:00Z",
  };

  it("asks signed-out visitors to sign in first, keeping the code", async () => {
    renderApp("/device?code=BCDF-GHJK", fakeApi(signedOut).fetch);
    await waitFor(() => expect(location()).toBe("/sign-in?next=%2Fdevice%3Fcode%3DBCDF-GHJK"));
  });

  it("says when the login started", () => {
    const now = new Date("2026-11-18T10:05:00Z");
    expect(startedAgo("2026-11-18T10:04:30Z", now)).toBe("just now");
    expect(startedAgo("2026-11-18T10:02:00Z", now)).toMatch(/3 minutes ago/);
    expect(startedAgo("2026-11-18T08:05:00Z", now)).toMatch(/2 hours ago/);
  });

  it("shows the code, where and when it started, and who it signs in as, then approves", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/device-logins/BCDF-GHJK": () => ({ status: 200, body: { ...login, created_at: new Date().toISOString() } }),
      "POST /v1/device-logins/BCDF-GHJK/approve": () => ({ status: 204 }),
    });
    const user = userEvent.setup();
    renderApp("/device?code=BCDF-GHJK", api.fetch);
    expect((await screen.findByLabelText("Code")).textContent).toBe("BCDF-GHJK");
    expect(screen.getByText("pat-laptop")).toBeTruthy();
    expect(screen.getByText("just now")).toBeTruthy();
    expect(screen.getByText("pat@example.com")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect((await screen.findByRole("status")).textContent).toContain("The CLI is signed in.");
  });

  it("denies", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/device-logins/BCDF-GHJK": () => ({ status: 200, body: login }),
      "POST /v1/device-logins/BCDF-GHJK/deny": () => ({ status: 204 }),
    });
    const user = userEvent.setup();
    renderApp("/device?code=BCDF-GHJK", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Deny" }));
    expect((await screen.findByRole("status")).textContent).toContain("Nothing was signed in.");
    expect(api.calls.some((c) => c.path.endsWith("/approve"))).toBe(false);
  });

  it("explains an expired or used code", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/device-logins/BCDF-GHJK": () => apiError(404, "device_login_not_found", "No such login."),
    });
    renderApp("/device?code=BCDF-GHJK", api.fetch);
    expect(await screen.findByText("That code has expired or was already used.")).toBeTruthy();
  });

  it("asks for the code when the link has none", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/device-logins/BCDF-GHJK": () => ({ status: 200, body: login }),
    });
    const user = userEvent.setup();
    renderApp("/device", api.fetch);
    await user.type(await screen.findByRole("textbox", { name: "Code" }), "BCDF-GHJK");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await waitFor(() => expect(location()).toBe("/device?code=BCDF-GHJK"));
    expect(await screen.findByRole("button", { name: "Approve" })).toBeTruthy();
  });
});

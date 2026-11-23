/// <reference types="node" />
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { namesCli, waitHint, webWords, WEB_HINTS, WEB_MESSAGES, withoutCli } from "./hints";

const internal = join(__dirname, "../../../internal");

/** Every Go file of the server that can answer with an error, tests and the CLI left out. */
function serverSources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return ["cli", "apidocs", "apigen"].includes(name) ? [] : serverSources(path);
    return name.endsWith(".go") && !name.endsWith("_test.go") ? [path] : [];
  });
}

/** The error codes the API answers with, found where the server builds its errors. */
function apiErrorCodes(): string[] {
  const codes = new Set<string>();
  const ctor = /\b(?:BadRequest|NotFound|Forbidden|Unauthorized|Unprocessable|Conflict|TooManyRequests|Gone|PaymentRequired|WriteError)\(\s*"([a-z][a-z_]+)"/g;
  const literal = /\bCode:\s*"([a-z][a-z_]+)"/g;
  const constant = /^\s*([A-Z][A-Za-z]*)\s*=\s*"([a-z][a-z_]+)"$/gm;
  for (const file of serverSources(internal)) {
    const src = readFileSync(file, "utf8");
    for (const re of [ctor, literal]) for (const m of src.matchAll(re)) codes.add(m[1]);
    if (/sending\/(launch|prepare)\.go$/.test(file)) for (const m of src.matchAll(constant)) codes.add(m[2]);
  }
  return [...codes].sort();
}

describe("the web app's words for the API's errors", () => {
  it("spots CLI commands, flags and API routes", () => {
    for (const text of [
      "Send one with `mailgoose campaign test abc` and check it.",
      "Set it with `mailgoose campaign update abc --send-at now`.",
      "Use `--send-at now`.",
      "Verify it (`POST /v1/domains`) first.",
    ]) {
      expect(namesCli(text), text).toBe(true);
    }
    for (const text of ["Send a test first.", "Verify a domain on the Domains page.", "It takes a day — or two."]) {
      expect(namesCli(text), text).toBe(false);
    }
  });

  it("drops only the sentences that name the CLI", () => {
    expect(withoutCli("The domain isn't verified. Run `mailgoose domain verify x` to check it. Try again later.")).toBe(
      "The domain isn't verified. Try again later.",
    );
    expect(withoutCli("Nothing to remove.")).toBe("Nothing to remove.");
  });

  it("words the untested campaign's problem for the web, whichever message the API gave", () => {
    for (const message of ["The campaign hasn't been tested yet.", "The campaign changed since its last test."]) {
      expect(webWords("untested", message, "Send one with `mailgoose campaign test abc` and check it looks right.")).toEqual({
        message: "Send a test and check it looks right before you launch.",
        hint: "",
      });
    }
  });

  it("drops a hint it has no wording for when it names the CLI, and keeps one that doesn't", () => {
    expect(webWords("something_new", "It failed.", "Fix it with `mailgoose thing update --flag`.")).toEqual({ message: "It failed.", hint: "" });
    expect(webWords("something_new", "It failed.", "Wait a minute and try again.")).toEqual({ message: "It failed.", hint: "Wait a minute and try again." });
  });

  it("keeps the CLI out of a message too, and never shows an empty one", () => {
    expect(webWords("x", "Connect again: `mailgoose import mailchimp connect`.", "").message).toBe("That didn't work.");
    expect(webWords("x", "It ended. Run `mailgoose a b` to see.", "").message).toBe("It ended.");
  });

  it("has wording that itself names no CLI", () => {
    for (const [code, hint] of Object.entries({ ...WEB_HINTS, ...WEB_MESSAGES })) {
      expect(namesCli(hint), `${code}: ${hint}`).toBe(false);
    }
  });

  it("shows nothing that names the CLI for any error code the API has", () => {
    const codes = apiErrorCodes();
    expect(codes.length).toBeGreaterThan(100);
    const api = "Fix it with `mailgoose campaign update abc --send-at <time>`, or `--send-at now`. See `POST /v1/campaigns`.";
    for (const code of codes) {
      const shown = webWords(code, "Something to fix. Run `mailgoose email check abc` for the details.", api);
      expect(namesCli(shown.message), `${code} message: ${shown.message}`).toBe(false);
      expect(namesCli(shown.hint), `${code} hint: ${shown.hint}`).toBe(false);
      expect(shown.message.includes("--") || shown.hint.includes("--"), code).toBe(false);
    }
  });

  it("says how long to wait in whole minutes, never seconds", () => {
    expect(waitHint("3412")).toBe("Try again in 57 minutes.");
    expect(waitHint("60")).toBe("Try again in a minute.");
    expect(waitHint("1")).toBe("Try again in a minute.");
    expect(waitHint("61")).toBe("Try again in 2 minutes.");
    expect(waitHint(null)).toBe("Try again in an hour.");
    expect(waitHint("soon")).toBe("Try again in an hour.");
  });

  it("has no wording for a code the API doesn't have", () => {
    const codes = new Set(apiErrorCodes());
    const stale = [...Object.keys(WEB_HINTS), ...Object.keys(WEB_MESSAGES)].filter((c) => !codes.has(c));
    expect(stale).toEqual([]);
  });
});

import { describe, expect, it } from "vitest";

import { safeNext } from "./session";

describe("safeNext", () => {
  it("keeps paths on this site", () => {
    expect(safeNext("/emails")).toBe("/emails");
    expect(safeNext("/device?code=BCDF-GHJK")).toBe("/device?code=BCDF-GHJK");
    expect(safeNext("/campaigns/1/report#opens")).toBe("/campaigns/1/report#opens");
  });

  it("sends everything else home", () => {
    for (const raw of [
      null,
      "",
      "emails",
      "https://evil.example/",
      "//evil.example/",
      "/\\evil.example/",
      "javascript:alert(1)",
      "/\t/evil.example/",
    ]) {
      expect(safeNext(raw), String(raw)).toBe("/");
    }
  });
});

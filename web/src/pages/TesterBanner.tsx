import { useCallback } from "react";
import { useLocation } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import { useLoad } from "../components";

/** "Dec 21, 2026", the way the CLI writes a tester account's end. */
function day(iso: string) {
  return new Date(iso).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
}

/**
 * Shown across the app on a tester account (MG-86): how many of its
 * credits are left and when it ends. It reads the account again on each
 * page, so the count follows the sends.
 */
export function TesterBanner() {
  const api = useApi();
  const { pathname } = useLocation();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/account")),
    // pathname: look again when the page changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, pathname],
  );
  const { state } = useLoad(load);
  const t = state.status === "ok" ? state.data.tester : undefined;
  if (!t) return null;
  if (t.ended) {
    return (
      <div className="notice notice-warn tester-banner" role="status">
        <p>
          <strong>Tester account.</strong> It ended on {day(t.until)}, so it can't send any more. Thanks for testing
          mailgoose.
        </p>
      </div>
    );
  }
  return (
    <div className="notice tester-banner" role="status">
      <p>
        <strong>
          Tester account: {t.left} of {t.credits} credits left.
        </strong>{" "}
        Campaigns and tests both use them, until {day(t.until)}. Sends to SES simulator addresses, such as
        success@simulator.amazonses.com, use none.
      </p>
    </div>
  );
}

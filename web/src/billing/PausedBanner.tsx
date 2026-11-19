import { useCallback } from "react";
import { Link, useLocation } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import { useLoad } from "../components";
import { pausedStatuses } from "./Billing";

/**
 * Shown across the app while sending is paused because the paid plan's last
 * payment didn't go through. It checks again on each page, so it goes away
 * once the payment does.
 */
export function PausedBanner() {
  const api = useApi();
  const { pathname } = useLocation();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/plan")),
    // pathname: look again when the page changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, pathname],
  );
  const { state } = useLoad(load);
  if (state.status !== "ok" || !state.data.status || !pausedStatuses.includes(state.data.status)) return null;
  return (
    <div className="notice notice-error paused-banner" role="alert">
      <p>
        Sending is paused: your plan's last payment didn't go through. Your contacts and emails are kept.{" "}
        {pathname === "/credits" || pathname === "/billing" ? "Update your card below." : <Link to="/credits">Update your card</Link>}
      </p>
    </div>
  );
}

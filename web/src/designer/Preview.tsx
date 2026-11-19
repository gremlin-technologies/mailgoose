import { useCallback } from "react";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import { LoadFailed, Loading, useLoad } from "../components";

export type Width = "desktop" | "mobile";

/**
 * The email as the server renders it (MG-25), with sample merge tag values.
 * It's shown in a sandboxed frame that runs no scripts.
 */
export function Preview({ emailId, width, version }: { emailId: string; width: Width; version: number }) {
  const api = useApi();
  const load = useCallback(
    () =>
      attempt(() =>
        api.GET("/v1/emails/{email_id}/preview", {
          params: { path: { email_id: emailId }, query: { width } },
          parseAs: "text",
        }),
      ),
    // version: a saved change shows on the next look.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, emailId, width, version],
  );
  const { state, reload } = useLoad<string>(load);
  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const label = width === "desktop" ? "Desktop preview" : "Mobile preview";
  return (
    <div className={`preview preview-${width}`}>
      <p className="muted small">Filled in with sample details: Alex Rivera, alex@example.com.</p>
      <iframe title={label} sandbox="" srcDoc={state.data} className="preview-frame" />
    </div>
  );
}

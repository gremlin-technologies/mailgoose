// "Save and exit" for the email editors (MG-83). The editors save as you go,
// but leaving should be a clear step: this saves anything still unsaved at
// once, then goes back to where the editor was opened from.
import { useState } from "react";
import { useLocation, useNavigate } from "react-router";

/** What a link into an editor carries so the editor can go back there. */
export type EditorFrom = { from: string };

/**
 * Where leaving the editor goes: the campaign it was opened from, when a
 * campaign's page opened it, otherwise the email list.
 */
export function useReturnTo(): string {
  const { state } = useLocation();
  const from = (state as Partial<EditorFrom> | null)?.from;
  return typeof from === "string" && /^\/campaigns\/[^/?#]+$/.test(from) ? from : "/emails";
}

/** Names where the editor's back link goes. */
export function backLabel(to: string): string {
  return to === "/emails" ? "All emails" : "Back to the campaign";
}

/**
 * The editor's primary button. It waits for the save; when something isn't
 * saved it stays, and the editor shows why, with the edits kept.
 */
export function SaveAndExit({ saveNow }: { saveNow: () => Promise<boolean> }) {
  const navigate = useNavigate();
  const to = useReturnTo();
  const [leaving, setLeaving] = useState(false);

  async function leave() {
    setLeaving(true);
    const saved = await saveNow();
    if (saved) void navigate(to);
    else setLeaving(false);
  }

  return (
    <button type="button" className="button primary small" onClick={() => void leave()} disabled={leaving} aria-busy={leaving}>
      Save and exit
    </button>
  );
}

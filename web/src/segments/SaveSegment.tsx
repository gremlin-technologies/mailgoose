// "Save as segment" on the contacts page (MG-116): names the filters set
// now and keeps them as a segment.
import { useState, type FormEvent } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import { FailureNotice } from "../components";
import { isEmpty, type Conditions, type Segment } from "./conditions";

export function SaveSegment({ conditions, searched }: { conditions: Conditions; searched: boolean }) {
  const api = useApi();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [saved, setSaved] = useState<Segment>();
  const none = isEmpty(conditions);

  async function save(e?: FormEvent) {
    e?.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/segments", { body: { name: name.trim(), conditions } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setSaved(res.data);
    setOpen(false);
    setName("");
  }

  return (
    <>
      <button
        type="button"
        className="button"
        onClick={() => {
          setOpen((o) => !o);
          setSaved(undefined);
        }}
        aria-expanded={open}
        disabled={none}
        title={none ? "Set a filter first" : undefined}
      >
        Save as segment
      </button>
      {saved && (
        <p className="small" role="status">
          Saved the segment <Link to={`/segments/${saved.id}`}>{saved.name}</Link>.
        </p>
      )}
      {open && !none && (
        // Not a nested form: the contacts search is the form around this.
        <div className="panel stack save-segment" role="group" aria-label="Save as segment">
          <label className="field">
            <span>Segment name</span>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  if (name.trim()) void save();
                }
              }}
              maxLength={100}
              placeholder="Engaged subscribers"
              autoFocus
            />
          </label>
          <p className="muted small">
            Saves the filters, which are worked out each time the segment is used.
            {searched && " The search box isn't part of a segment."}
          </p>
          <FailureNotice failure={failure} />
          <div className="row">
            <button type="button" className="button primary" onClick={() => void save()} disabled={busy || !name.trim()}>
              {busy ? "Saving…" : "Save segment"}
            </button>
            <button type="button" className="button" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </>
  );
}

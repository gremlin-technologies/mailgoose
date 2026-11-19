// Autosave for the email editors. Edits are kept as drafts over the saved
// email, so they show at once, and are sent a moment after the last one.
// Every request (a save or a block operation) runs one at a time with the
// version the previous one returned, so the editor never conflicts with
// itself. When someone else saved first (a 409), the edits are dropped and
// the email is reloaded.
import { useEffect, useRef, useState } from "react";

import type { ApiClient } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";

type Email = components["schemas"]["Email"];
export type Fields = Record<string, unknown>;

/** The key an editor saves the email's own name and settings under; blocks go by id. */
export const EMAIL = "email";

/** How long autosave waits after the last edit. */
export const saveDelay = 800;

export type SaveState = "saved" | "pending" | "saving" | "failed";
export const saveText: Record<SaveState, string> = {
  saved: "All changes saved",
  pending: "Unsaved changes",
  saving: "Saving…",
  failed: "Not saved",
};

export const conflictNotice: Failure = {
  code: "version_conflict",
  message: "This email was changed somewhere else, so it has been reloaded.",
  hint: "Make your change again on the email as it is now.",
  status: 409,
};

type Options = {
  email: Email;
  replace: (e: Email) => void;
  reload: () => void;
  /** Saves one target's edits (a block id, or a key of the editor's own) at a version. */
  send: (key: string, fields: Fields, version: number) => Promise<Result<Email | undefined>>;
  /** Returns true when the editor shows a failed save itself, so no notice is needed. */
  handled?: (key: string, failure: Failure) => boolean;
};

export function useAutosave({ email, replace, reload, send, handled }: Options) {
  const [failure, setFailure] = useState<Failure>();
  const [busy, setBusy] = useState(false);
  // Edits not saved yet, by key; the editor shows them over the saved email.
  const [drafts, setDrafts] = useState<Record<string, Fields>>({});
  const [save, setSave] = useState<SaveState>("saved");

  const version = useRef(email.version);
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const unsent = useRef<Record<string, Fields>>({});
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const latest = useRef({ send, handled });
  const flushRef = useRef<() => void>(() => {});

  useEffect(() => {
    latest.current = { send, handled };
  });
  useEffect(() => {
    version.current = email.version;
  }, [email]);
  // Leaving the editor saves what's left.
  useEffect(() => () => flushRef.current(), []);
  useEffect(() => {
    if (save !== "pending" && save !== "saving") return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [save]);

  function enqueue<T>(f: () => Promise<T>): Promise<T> {
    const p = queue.current.then(f, f);
    queue.current = p.catch(() => undefined);
    return p;
  }

  /** Someone else saved the email: drop what's unsaved here and show theirs. */
  function conflicted() {
    clearTimeout(timer.current);
    unsent.current = {};
    setDrafts({});
    setSave("saved");
    setFailure(conflictNotice);
    reload();
  }

  /** Takes one answer: its email replaces the one shown. */
  function settle(res: Result<Email | undefined>, key?: string): boolean {
    if (res.ok) {
      if (res.data) {
        version.current = res.data.version;
        replace(res.data);
      }
      return true;
    }
    if (res.failure.code === "version_conflict") conflicted();
    else if (!(key !== undefined && latest.current.handled?.(key, res.failure))) setFailure(res.failure);
    return false;
  }

  /** Sends every unsaved edit now. */
  function flush(): Promise<unknown> {
    clearTimeout(timer.current);
    const batch = unsent.current;
    unsent.current = {};
    const keys = Object.keys(batch);
    if (keys.length === 0) return queue.current;
    setSave("saving");
    return enqueue(async () => {
      let ok = true;
      for (const key of keys) {
        const fields = batch[key];
        const res = await latest.current.send(key, fields, version.current);
        if (!res.ok && res.failure.code === "version_conflict") return conflicted();
        if (!settle(res, key)) {
          ok = false;
          continue;
        }
        // What was saved is in the email now; keep only edits made since.
        setDrafts((d) => {
          const left = { ...d[key] };
          for (const f of Object.keys(fields)) if (left[f] === fields[f]) delete left[f];
          const out = { ...d };
          if (Object.keys(left).length) out[key] = left;
          else delete out[key];
          return out;
        });
      }
      if (Object.keys(unsent.current).length) setSave("pending");
      else setSave(ok ? "saved" : "failed");
    });
  }

  useEffect(() => {
    flushRef.current = () => void flush();
  });

  /** Records an edit and saves it once editing pauses. */
  function edit(key: string, fields: Fields) {
    setFailure(undefined);
    setDrafts((d) => ({ ...d, [key]: { ...d[key], ...fields } }));
    unsent.current = { ...unsent.current, [key]: { ...unsent.current[key], ...fields } };
    setSave("pending");
    clearTimeout(timer.current);
    timer.current = setTimeout(() => void flush(), saveDelay);
  }

  /** Runs one operation after any unsaved edits, with the version they leave. */
  async function run(call: (v: number) => Promise<Result<Email | undefined>>) {
    setBusy(true);
    setFailure(undefined);
    void flush();
    const ok = await enqueue(async () => settle(await call(version.current)));
    setBusy(false);
    return ok;
  }

  return { failure, setFailure, busy, drafts, save, edit, flush, run };
}

/** Saves an email's name and settings, given together as fields. */
export function saveSettings(api: ApiClient, emailId: string, fields: Fields, version: number): Promise<Result<Email | undefined>> {
  const { name, ...settings } = fields;
  return attempt(() =>
    api.PATCH("/v1/emails/{email_id}", {
      params: { path: { email_id: emailId } },
      body: {
        version,
        ...(name !== undefined ? { name: name as string } : {}),
        ...(Object.keys(settings).length ? { settings } : {}),
      },
    }),
  );
}

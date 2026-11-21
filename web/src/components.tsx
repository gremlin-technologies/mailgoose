import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";

import type { Failure, Result } from "./api/result";
import { useTheme } from "./theme";

/**
 * The logo: the goose's head and the word (docs/brand.md), with light text in
 * dark mode. `size` sets its height in CSS: "menu" for the side menu, "page"
 * above the sign-in and policy cards.
 */
export function Wordmark({ size }: { size: "menu" | "page" }) {
  const dark = useTheme() === "dark";
  const [width, height] = size === "menu" ? [116, 28] : [149, 36];
  return (
    <img
      className={`wordmark wordmark-${size}`}
      src={dark ? "/brand/wordmark-light.png" : "/brand/wordmark.png"}
      width={width}
      height={height}
      alt="mailgoose"
    />
  );
}

/** The centred card the sign-up, sign-in and CLI approval pages use. */
export function AuthCard({ title, children, footer }: { title: string; children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="auth">
      <header>
        <Link to="/" className="brand-logo">
          <Wordmark size="page" />
        </Link>
      </header>
      <main className="card">
        <h1>{title}</h1>
        {children}
      </main>
      {footer && <footer className="auth-footer">{footer}</footer>}
    </div>
  );
}

/** Shows what went wrong and what to do about it. */
export function FailureNotice({ failure }: { failure: Failure | undefined }) {
  if (!failure) return null;
  return (
    <div className="notice notice-error" role="alert">
      <p>{failure.message}</p>
      {failure.hint && <p className="hint">{failure.hint}</p>}
    </div>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <p className="loading" role="status">
      {label}
    </p>
  );
}

/** A page's title and a line about it. */
export function PageHeader({ title, about, children }: { title: ReactNode; about?: ReactNode; children?: ReactNode }) {
  return (
    <header className="page-head">
      <div>
        <h1>{title}</h1>
        {about && <p className="muted">{about}</p>}
      </div>
      {children}
    </header>
  );
}

export type Tone = "neutral" | "good" | "warn" | "bad" | "info";

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

/** Copies value to the clipboard; label says what it copies, for screen readers. */
export function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const [failed, setFailed] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setFailed(false);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      setFailed(true);
    }
  }
  return (
    <button type="button" className="button small" onClick={copy} aria-label={`Copy ${label}`}>
      {copied ? "Copied" : failed ? "Select and copy" : "Copy"}
    </button>
  );
}

/** A date and time in the viewer's own time zone. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "";
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export type Loaded<T> = { status: "loading" } | { status: "ok"; data: T } | { status: "failed"; failure: Failure };

const emptyAnswer: Failure = { code: "empty_response", message: "mailgoose sent an empty answer.", hint: "Try again.", status: 200 };

/**
 * Runs load when the page opens and again on reload(). load must be stable
 * (wrap it in useCallback). replace() swaps in data a later request returned.
 */
export function useLoad<T>(load: () => Promise<Result<T | undefined>>) {
  const [state, setState] = useState<Loaded<T>>({ status: "loading" });
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let current = true;
    void load().then((res) => {
      if (!current) return;
      if (!res.ok) setState({ status: "failed", failure: res.failure });
      else if (res.data === undefined) setState({ status: "failed", failure: emptyAnswer });
      else setState({ status: "ok", data: res.data });
    });
    return () => {
      current = false;
    };
  }, [load, tick]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  const replace = useCallback((data: T) => setState({ status: "ok", data }), []);
  return { state, reload, replace };
}

/** What a page shows while its data loads, or when it couldn't load. */
export function LoadFailed({ failure, retry }: { failure: Failure; retry: () => void }) {
  return (
    <div className="stack">
      <FailureNotice failure={failure} />
      <div>
        <button type="button" className="button" onClick={retry}>
          Try again
        </button>
      </div>
    </div>
  );
}

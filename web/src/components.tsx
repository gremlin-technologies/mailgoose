import type { ReactNode } from "react";
import { Link } from "react-router";

import type { Failure } from "./api/result";

/** The centred card the sign-up, sign-in and CLI approval pages use. */
export function AuthCard({ title, children, footer }: { title: string; children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="auth">
      <Link to="/" className="brand auth-brand">
        mailgoose
      </Link>
      <main className="card">
        <h1>{title}</h1>
        {children}
      </main>
      {footer && <p className="auth-footer">{footer}</p>}
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

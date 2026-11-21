import { LogOut } from "lucide-react";
import { useState } from "react";
import { NavLink, Outlet } from "react-router";

import type { Failure } from "../api/result";
import { PausedBanner } from "../billing/PausedBanner";
import { FailureNotice } from "../components";
import { useSession } from "../session";
import { navGroups } from "./nav";
import { ReviewBanner } from "./ReviewBanner";
import { TermsGate } from "./TermsGate";
import { TestModeBanner } from "./TestAddresses";

/** The signed-in app: navigation on the side, the page beside it. */
export function AppLayout() {
  const { state, signOut } = useSession();
  const [failure, setFailure] = useState<Failure>();
  const session = state.status === "signed-in" ? state.session : undefined;

  async function onSignOut() {
    setFailure(undefined);
    // RequireSession moves to the sign-in page once the session is gone.
    setFailure(await signOut());
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <NavLink to="/" className="brand-logo">
          <picture>
            <source srcSet="/brand/wordmark-light.png" media="(prefers-color-scheme: dark)" />
            <img src="/brand/wordmark.png" width={116} height={28} alt="mailgoose" />
          </picture>
        </NavLink>
        {session && (
          <p className="account" title={session.user.email}>
            {session.account.name}
          </p>
        )}
        <nav aria-label="Main">
          {navGroups().map((g) => (
            <div key={g.label ?? "top"}>
              {g.label && <p className="nav-label">{g.label}</p>}
              <ul>
                {g.pages.map((p) => (
                  <li key={p.path}>
                    <NavLink to={p.path} end={p.path === "/"}>
                      <p.icon className="nav-icon" size={18} strokeWidth={1.75} aria-hidden="true" />
                      {p.label}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>
        <div className="sidebar-foot">
          {session && <p className="muted small email">{session.user.email}</p>}
          <button type="button" className="link" onClick={onSignOut}>
            <LogOut size={16} strokeWidth={1.75} aria-hidden="true" />
            Sign out
          </button>
          <FailureNotice failure={failure} />
        </div>
      </aside>
      <main className="content">
        <PausedBanner />
        <ReviewBanner />
        <TestModeBanner />
        <TermsGate>
          <Outlet />
        </TermsGate>
      </main>
    </div>
  );
}

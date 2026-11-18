import { useState } from "react";
import { NavLink, Outlet } from "react-router";

import type { Failure } from "../api/result";
import { FailureNotice } from "../components";
import { useSession } from "../session";
import { navPages } from "./nav";

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
        <NavLink to="/" className="brand">
          mailgoose
        </NavLink>
        {session && (
          <p className="account" title={session.user.email}>
            {session.account.name}
          </p>
        )}
        <nav aria-label="Main">
          <ul>
            {navPages.map((p) => (
              <li key={p.path}>
                <NavLink to={p.path} end={p.path === "/"}>
                  {p.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <div className="sidebar-foot">
          {session && <p className="muted small">{session.user.email}</p>}
          <button type="button" className="link" onClick={onSignOut}>
            Sign out
          </button>
          <FailureNotice failure={failure} />
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}

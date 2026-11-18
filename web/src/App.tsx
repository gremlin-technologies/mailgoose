import type { ReactNode } from "react";
import { Route, Routes } from "react-router";

import { RequireSession, SignedOutOnly } from "./guards";
import { ApiKeys } from "./pages/ApiKeys";
import { AppLayout } from "./pages/AppLayout";
import { Device } from "./pages/Device";
import { DomainDetail, Domains } from "./pages/Domains";
import { navPages } from "./pages/nav";
import { NotFound, Placeholder } from "./pages/Placeholder";
import { Settings } from "./pages/Settings";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";

/** Pages that are built; the rest of the menu shows a placeholder. */
const built: Record<string, ReactNode> = {
  "/domains": <Domains />,
  "/api-keys": <ApiKeys />,
  "/settings": <Settings />,
};

/** Every route in the app. main.tsx puts it in a browser router. */
export function AppRoutes() {
  return (
    <Routes>
      <Route
        path="/sign-in"
        element={
          <SignedOutOnly>
            <SignIn />
          </SignedOutOnly>
        }
      />
      <Route
        path="/signup"
        element={
          <SignedOutOnly>
            <SignUp />
          </SignedOutOnly>
        }
      />
      <Route
        path="/device"
        element={
          <RequireSession>
            <Device />
          </RequireSession>
        }
      />
      <Route
        element={
          <RequireSession>
            <AppLayout />
          </RequireSession>
        }
      >
        {navPages.map((p) =>
          p.path === "/" ? (
            <Route key={p.path} index element={<Placeholder page={p} />} />
          ) : (
            <Route key={p.path} path={built[p.path] ? p.path : `${p.path}/*`} element={built[p.path] ?? <Placeholder page={p} />} />
          ),
        )}
        <Route path="/domains/:id" element={<DomainDetail />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

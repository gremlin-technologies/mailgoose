import { Route, Routes } from "react-router";

import { RequireSession, SignedOutOnly } from "./guards";
import { AppLayout } from "./pages/AppLayout";
import { Device } from "./pages/Device";
import { navPages } from "./pages/nav";
import { NotFound, Placeholder } from "./pages/Placeholder";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";

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
            <Route key={p.path} path={`${p.path}/*`} element={<Placeholder page={p} />} />
          ),
        )}
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";

import { AppRoutes } from "./App";
import { createApiClient } from "./api/client";
import { ApiProvider } from "./api/context";
import { SessionProvider } from "./session";
import { startTheme } from "./theme";
import "@fontsource-variable/inter/wght.css";
import "./styles.css";

// The API serves this app from its own origin, so requests go to /v1 here and
// the session cookie comes along.
const api = createApiClient({ baseUrl: window.location.origin });

// Light unless dark mode was turned on in Settings, set before the first paint.
startTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ApiProvider client={api}>
      <SessionProvider>
        <BrowserRouter>
          <AppRoutes />
        </BrowserRouter>
      </SessionProvider>
    </ApiProvider>
  </StrictMode>,
);

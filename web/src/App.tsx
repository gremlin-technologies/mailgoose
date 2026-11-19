import type { ReactNode } from "react";
import { Route, Routes } from "react-router";

import { RequireSession, SignedOutOnly } from "./guards";
import { ApiKeys } from "./pages/ApiKeys";
import { AppLayout } from "./pages/AppLayout";
import { Billing } from "./billing/Billing";
import { CampaignDetail } from "./campaigns/CampaignDetail";
import { CampaignReport } from "./campaigns/Report";
import { Campaigns, NewCampaign } from "./pages/Campaigns";
import { ContactDetail, Contacts } from "./pages/Contacts";
import { Dashboard } from "./pages/Dashboard";
import { Device } from "./pages/Device";
import { Designer } from "./designer/Designer";
import { DomainDetail, Domains } from "./pages/Domains";
import { Emails } from "./pages/Emails";
import { FormEditor } from "./forms/FormEditor";
import { Forms } from "./pages/Forms";
import { ImportContacts, ImportDetail } from "./pages/Imports";
import { navPages } from "./pages/nav";
import { NotFound, Placeholder } from "./pages/Placeholder";
import { PolicyPage, policies } from "./pages/Policies";
import { Settings } from "./pages/Settings";
import { SignIn } from "./pages/SignIn";
import { SignUp } from "./pages/SignUp";
import { Tags } from "./pages/Tags";

/** Pages that are built; the rest of the menu shows a placeholder. */
const built: Record<string, ReactNode> = {
  "/": <Dashboard />,
  "/emails": <Emails />,
  "/campaigns": <Campaigns />,
  "/forms": <Forms />,
  "/credits": <Billing />,
  "/contacts": <Contacts />,
  "/tags": <Tags />,
  "/domains": <Domains />,
  "/api-keys": <ApiKeys />,
  "/settings": <Settings />,
};

/** Every route in the app. main.tsx puts it in a browser router. */
export function AppRoutes() {
  return (
    <Routes>
      {policies.map((p) => (
        <Route key={p.path} path={p.path} element={<PolicyPage policy={p} />} />
      ))}
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
            <Route key={p.path} index element={built[p.path] ?? <Placeholder page={p} />} />
          ) : (
            <Route key={p.path} path={built[p.path] ? p.path : `${p.path}/*`} element={built[p.path] ?? <Placeholder page={p} />} />
          ),
        )}
        <Route path="/contacts/import" element={<ImportContacts />} />
        <Route path="/contacts/imports/:id" element={<ImportDetail />} />
        <Route path="/contacts/:id" element={<ContactDetail />} />
        <Route path="/domains/:id" element={<DomainDetail />} />
        <Route path="/emails/:id" element={<Designer />} />
        <Route path="/campaigns/new" element={<NewCampaign />} />
        <Route path="/campaigns/:id" element={<CampaignDetail />} />
        <Route path="/campaigns/:id/report" element={<CampaignReport />} />
        <Route path="/forms/:id" element={<FormEditor />} />
        <Route path="/billing" element={<Billing />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

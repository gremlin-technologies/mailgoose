// The account's contact fields, which are merge tags too (MG-133), for the
// editors' merge tag menus and live previews.
import { createContext, useContext, type ReactNode } from "react";

import { useContactFields, type ContactField } from "../pages/Fields";

const none: ContactField[] = [];
const MergeFields = createContext<ContactField[]>(none);

/** The account's contact fields, in the order made: empty until they load, or if they can't. */
export function useMergeFields(): ContactField[] {
  return useContext(MergeFields);
}

/** Loads the account's contact fields once, for every merge tag menu inside it. */
export function MergeFieldsProvider({ children }: { children: ReactNode }) {
  const { state } = useContactFields();
  return <MergeFields.Provider value={state.status === "ok" ? state.data : none}>{children}</MergeFields.Provider>;
}

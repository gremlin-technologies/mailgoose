import {
  ClipboardList,
  CreditCard,
  Globe,
  KeyRound,
  LayoutDashboard,
  Mail,
  Send,
  Settings,
  Tag,
  Users,
  type LucideIcon,
} from "lucide-react";

/**
 * Every MVP page in the signed-in app, in navigation order. Pages next to each
 * other with the same group sit under one heading in the side menu; a page
 * with no group has no heading.
 */
export type NavPage = { path: string; label: string; about: string; icon: LucideIcon; group?: string };

export const navPages: NavPage[] = [
  { path: "/", label: "Dashboard", about: "Your getting-started checklist and recent campaigns.", icon: LayoutDashboard },
  { path: "/emails", label: "Emails", about: "Design emails with blocks, or bring your own HTML.", icon: Mail, group: "Marketing" },
  {
    path: "/campaigns",
    label: "Campaigns",
    about: "Send an email to your contacts, test it first, and see each campaign's report.",
    icon: Send,
    group: "Marketing",
  },
  { path: "/contacts", label: "Contacts", about: "Add, import and export the people on your list.", icon: Users, group: "Marketing" },
  { path: "/tags", label: "Tags", about: "Group your contacts, and send to a group.", icon: Tag, group: "Marketing" },
  { path: "/forms", label: "Signup forms", about: "Make a signup form and copy its embed code.", icon: ClipboardList, group: "Marketing" },
  { path: "/domains", label: "Domains", about: "Verify the domain your emails come from.", icon: Globe, group: "Account" },
  { path: "/api-keys", label: "API keys", about: "Keys for the CLI, your scripts and your AI agent.", icon: KeyRound, group: "Account" },
  {
    path: "/credits",
    label: "Credits and billing",
    about: "Your plan, your credit balance, buying credits and your history.",
    icon: CreditCard,
    group: "Account",
  },
  { path: "/settings", label: "Settings", about: "Your account name, sending defaults, postal address and dark mode.", icon: Settings, group: "Account" },
];

/** navPages split into the runs the side menu shows under each heading. */
export function navGroups(pages: NavPage[] = navPages): { label?: string; pages: NavPage[] }[] {
  const out: { label?: string; pages: NavPage[] }[] = [];
  for (const p of pages) {
    const last = out[out.length - 1];
    if (last && last.label === p.group) last.pages.push(p);
    else out.push({ label: p.group, pages: [p] });
  }
  return out;
}

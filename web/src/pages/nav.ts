/** Every MVP page in the signed-in app, in navigation order. */
export type NavPage = { path: string; label: string; about: string };

export const navPages: NavPage[] = [
  { path: "/", label: "Dashboard", about: "Your getting-started checklist and recent campaigns." },
  { path: "/emails", label: "Emails", about: "Design emails with blocks, or bring your own HTML." },
  { path: "/campaigns", label: "Campaigns", about: "Send an email to your contacts, test it first, and see each campaign's report." },
  { path: "/contacts", label: "Contacts", about: "Add, import and export the people on your list." },
  { path: "/tags", label: "Tags", about: "Group your contacts, and send to a group." },
  { path: "/forms", label: "Signup forms", about: "Make a signup form and copy its embed code." },
  { path: "/domains", label: "Domains", about: "Verify the domain your emails come from." },
  { path: "/api-keys", label: "API keys", about: "Keys for the CLI, your scripts and your AI agent." },
  { path: "/credits", label: "Credits", about: "Your credit balance, buying credits and your billing history." },
  { path: "/settings", label: "Settings", about: "Your account name, sending defaults and signing out everywhere." },
];

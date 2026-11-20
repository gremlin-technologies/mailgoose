import { Link } from "react-router";

import { currentPolicyVersion, policyChangesPath, policyVersions } from "../policyVersions.gen";

/**
 * The legal pages (CMP-10). Their text is a placeholder outline until
 * Gremlin Technologies supplies reviewed legal text: each page says so at the
 * top, and each section lists what the final text must cover. The anti-spam
 * points describe rules the app already enforces.
 */
export type Policy = {
  path: string;
  title: string;
  /** One line on what the page is for. */
  summary: string;
  sections: { heading: string; points: string[] }[];
};

export const policies: Policy[] = [
  {
    path: "/terms",
    title: "Terms of service",
    summary: "The agreement between Gremlin Technologies and anyone with a mailgoose account.",
    sections: [
      {
        heading: "Who the agreement is with",
        points: [
          "Gremlin Technologies' legal name and address, and the law that governs the agreement.",
          "Accounts are for businesses and people sending to their own subscribers; one person per account.",
        ],
      },
      {
        heading: "Using mailgoose",
        points: [
          "Everyone sending with mailgoose must follow the anti-spam policy.",
          "What we may do when an account breaks it: hold an import, pause sending, or close the account.",
          "Agents and the CLI act for the account holder, who stays responsible for what they send.",
        ],
      },
      {
        heading: "Plans, credits and payment",
        points: [
          "The free plan: 500 contacts and 1,000 emails a month.",
          "The paid plan is billed monthly or yearly by card through Stripe; credits are bought in packs and last 24 months.",
          "Refunds, failed payments, price changes and how much notice we give.",
        ],
      },
      {
        heading: "Your content and data",
        points: [
          "Customers own their emails, contacts and forms; we process contacts for them under the DPA.",
          "Export and deletion, and what happens to data and unused credits when an account closes.",
        ],
      },
      {
        heading: "The service itself",
        points: [
          "Availability, changes to the service, liability limits, and how these terms change.",
        ],
      },
    ],
  },
  {
    path: "/privacy",
    title: "Privacy policy",
    summary: "What personal data mailgoose collects about its own customers, and why.",
    sections: [
      {
        heading: "What we collect",
        points: [
          "Account details: your email address, account name, postal address and time zone.",
          "Sign-in records: when codes were sent and used, and the IP address and browser of each sign-in.",
          "Billing details are held by Stripe; mailgoose keeps only the plan, payments and credit history.",
          "One session cookie for the web app. No advertising or analytics cookies.",
        ],
      },
      {
        heading: "Your subscribers' data",
        points: [
          "Contacts belong to the customer who collected them; the DPA covers how we process them.",
          "For each contact: consent records (time, IP address, form and wording), and opens and clicks unless the sender turned tracking off.",
        ],
      },
      {
        heading: "Who else handles it",
        points: [
          "Amazon Web Services hosts mailgoose and sends its email; Stripe takes payments. The full list is in the DPA.",
        ],
      },
      {
        heading: "Your rights",
        points: [
          "How to see, correct, export or delete your data, and who to contact.",
          "How long each kind of data is kept.",
        ],
      },
    ],
  },
  {
    path: "/anti-spam",
    title: "Anti-spam policy",
    summary: "Who you may email with mailgoose. Signing up means agreeing to it.",
    sections: [
      {
        heading: "Only people who asked",
        points: [
          "Email only people who asked to hear from you, and only about what they signed up for.",
          "No bought, rented, shared or scraped lists, and no addresses gathered for some other purpose.",
          "Signup forms ask new subscribers to confirm by email, unless you turn that off and take responsibility for consent.",
        ],
      },
      {
        heading: "In every email",
        points: [
          "A working unsubscribe link, which mailgoose adds and honors at once.",
          "Your postal address in the footer, and a From address on a domain you have verified.",
          "A subject line that matches what's inside.",
        ],
      },
      {
        heading: "What mailgoose checks",
        points: [
          "Imports with signs of a bought list are held for review before anyone on them gets email.",
          "Bounce and complaint rates are watched, and sending is paused when they climb too high.",
        ],
      },
      {
        heading: "Reporting abuse",
        points: ["Where to report spam sent through mailgoose, and what happens next."],
      },
    ],
  },
  {
    path: "/dpa",
    title: "Data processing agreement",
    summary: "How mailgoose processes your contacts' personal data on your behalf (GDPR Article 28).",
    sections: [
      {
        heading: "Roles",
        points: [
          "The customer is the controller of their contacts' data; Gremlin Technologies is the processor.",
          "We process it only to send the customer's email, run their forms and report on their campaigns.",
        ],
      },
      {
        heading: "Subprocessors",
        points: [
          "Amazon Web Services (hosting, storage and sending email), in the US (us-east-2).",
          "Stripe (payments; holds the account holder's billing details, not contacts).",
          "How customers hear about a new subprocessor and can object.",
        ],
      },
      {
        heading: "Safeguards",
        points: [
          "Security measures, staff confidentiality, and breach notice times.",
          "Transfers outside the EU and UK, and the clauses that cover them.",
          "Help with subscribers' requests to see or delete their data, and deletion when the agreement ends.",
        ],
      },
    ],
  },
];

/** A policy date, YYYY-MM-DD, as "November 19, 2026". */
export function formatPolicyDate(day: string): string {
  const d = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return day;
  return d.toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });
}

/** The version line at the top of each policy page (MG-68). */
function VersionLine() {
  return (
    <p className="muted small policy-version">
      Version {currentPolicyVersion.version}, effective {formatPolicyDate(currentPolicyVersion.effective)}.{" "}
      <Link to={policyChangesPath}>What changed</Link>
    </p>
  );
}

/** One legal page. Anyone can read it, signed in or not. */
export function PolicyPage({ policy }: { policy: Policy }) {
  return (
    <div className="policy">
      <header>
        <Link to="/" className="brand auth-brand">
          mailgoose
        </Link>
      </header>
      <main className="card policy-card">
        <h1>{policy.title}</h1>
        <VersionLine />
        <div className="notice notice-warn" role="note">
          <p>
            <strong>Placeholder.</strong> This isn't the final {policy.title.toLowerCase()}. Gremlin Technologies
            will replace this outline with reviewed legal text before mailgoose launches.
          </p>
        </div>
        <p>{policy.summary}</p>
        {policy.sections.map((s) => (
          <section key={s.heading}>
            <h2>{s.heading}</h2>
            <ul>
              {s.points.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          </section>
        ))}
      </main>
      <PolicyNav current={policy.path} />
    </div>
  );
}

function PolicyNav({ current }: { current: string }) {
  return (
    <nav aria-label="Policies" className="policy-nav">
      <ul>
        {policies.map((p) => (
          <li key={p.path}>
            {p.path === current ? <span aria-current="page">{p.title}</span> : <Link to={p.path}>{p.title}</Link>}
          </li>
        ))}
      </ul>
    </nav>
  );
}

/**
 * What changed in each version of the policies, newest first. The four pages
 * share one version. When the terms change, people are asked to accept them
 * again, with a link here.
 */
export function PolicyChangesPage() {
  return (
    <div className="policy">
      <header>
        <Link to="/" className="brand auth-brand">
          mailgoose
        </Link>
      </header>
      <main className="card policy-card">
        <h1>What changed in our policies</h1>
        <p>
          The terms of service, privacy policy, anti-spam policy and data processing agreement share one version. When
          a new version takes effect, everyone with an account is asked to accept it.
        </p>
        {policyVersions.map((v) => (
          <section key={v.version}>
            <h2>
              Version {v.version}, effective {formatPolicyDate(v.effective)}
              {v.version === currentPolicyVersion.version ? " (current)" : ""}
            </h2>
            <ul>
              {v.changes.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
          </section>
        ))}
      </main>
      <PolicyNav current={policyChangesPath} />
    </div>
  );
}

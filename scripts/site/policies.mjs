// Writes the public site's policy pages (site/<slug>/index.html) from the
// app's own policy pages, web/src/pages/Policies.tsx, and the current policy
// version. Run it from the repository root after changing either:
//
//   node scripts/site/policies.mjs
//
// web/src/pages/sitePolicies.test.ts fails when the two drift apart.
import fs from "node:fs";
const [src, ver, out] = ["web/src/pages/Policies.tsx", "web/src/policyVersions.gen.ts", "site"];
const t = fs.readFileSync(src, "utf8");
const start = t.indexOf("export const policies: Policy[] = ") + "export const policies: Policy[] = ".length;
const end = t.indexOf("];", start) + 1;
const policies = eval("(" + t.slice(start, end) + ")");
const v = JSON.parse(fs.readFileSync(ver, "utf8").match(/policyVersions: PolicyVersion\[\] = (\[[\s\S]*?\n\]);/)[1])[0];
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const date = (d) => new Date(d + "T00:00:00Z").toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });
for (const p of policies) {
  const slug = p.path.slice(1);
  const nav = policies.map((q) => q.path === p.path
    ? `        <li><span aria-current="page">${esc(q.title)}</span></li>`
    : `        <li><a href="${q.path}/">${esc(q.title)}</a></li>`).join("\n");
  const sections = p.sections.map((s) => `    <section>
      <h2>${esc(s.heading)}</h2>
      <ul>
${s.points.map((x) => `        <li>${esc(x)}</li>`).join("\n")}
      </ul>
    </section>`).join("\n");
  const html = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${esc(p.title)} | mailgoose</title>
  <meta name="description" content="${esc(p.summary)}">
  <link rel="canonical" href="https://mailgoose.app${p.path}/">
  <link rel="icon" href="/assets/favicon-32.png" sizes="32x32" type="image/png">
  <link rel="icon" href="/assets/icon-192.png" sizes="192x192" type="image/png">
  <link rel="apple-touch-icon" href="/assets/apple-touch-icon.png">
  <link rel="stylesheet" href="/assets/tokens.css">
  <link rel="stylesheet" href="/assets/site.css">
</head>
<body>
  <a class="skip" href="#main">Skip to content</a>
  <!-- Copied from web/src/pages/Policies.tsx, the app's own policy pages.
       Keep the two in step: web/src/pages/sitePolicies.test.ts checks it. -->
  <header class="site-header">
    <div class="wrap">
      <a class="brand" href="/"><img src="/assets/brand/wordmark.png" width="133" height="32" alt="mailgoose"></a>
    </div>
  </header>
  <main class="policy" id="main">
    <h1>${esc(p.title)}</h1>
    <p class="policy-version">Version ${v.version}, effective ${date(v.effective)}.</p>
    <div class="notice" role="note">
      <p><strong>Placeholder.</strong> This isn't the final ${esc(p.title.toLowerCase())}. Gremlin Technologies will replace this outline with reviewed legal text before mailgoose launches.</p>
    </div>
    <p>${esc(p.summary)}</p>
${sections}
    <nav aria-label="Policies" class="policy-nav">
      <ul>
${nav}
      </ul>
    </nav>
  </main>
  <footer class="site-footer">
    <div class="wrap">
      <p>&copy; 2026 Gremlin Technologies LLC. <a href="/">Back to mailgoose</a></p>
    </div>
  </footer>
</body>
</html>
`;
  fs.mkdirSync(`${out}/${slug}`, { recursive: true });
  fs.writeFileSync(`${out}/${slug}/index.html`, html);
  console.log("wrote", slug);
}

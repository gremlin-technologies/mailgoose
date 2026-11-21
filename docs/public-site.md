# The public website at mailgoose.app (site/)

Plain HTML and CSS with no build step: a landing page that says what mailgoose
is and how it keeps email wanted, and static copies of the four policy pages.
`.github/workflows/pages.yml` publishes `site/` to GitHub Pages on every
push to `main` that changes it. It needs no AWS access, so it isn't held up by
the CloudFront or OIDC blocks (`docs/decisions.md`).

- `site/index.html`: the landing page.
- `site/assets/tokens.css`: the web app's design tokens and Inter, generated
  from `web/src/styles.css`. Don't edit it by hand; after changing the app's
  tokens, run `node scripts/site/tokens.mjs` from the repository root.
  `web/src/pages/siteTokens.test.ts` fails if they drift. `site.css` lays the
  page out with them, following `docs/design-system.md`.
- `site/terms/`, `site/privacy/`, `site/anti-spam/`, `site/dpa/`: generated from the app's own
  policy pages (`web/src/pages/Policies.tsx`). Don't edit them by hand; change
  `Policies.tsx`, then run `node scripts/site/policies.mjs` from the repository
  root. `web/src/pages/sitePolicies.test.ts` fails if they drift. Their text is
  still the placeholder outline, marked as such on each page.
- `site/assets/`: stylesheets, favicon, Inter (`fonts/`, with its licence) and
  two downsized copies of `docs/brand/mascot.png`.

Preview locally: `cd site && python3 -m http.server 8000`, then open
http://localhost:8000.

## One-time setup (the owner)

GitHub Pages is free for this repository because it's public.

1. **Pages source.** In the repository on GitHub: Settings > Pages > Build and
   deployment > Source: **GitHub Actions**.
2. **Merge the PR** that adds `site/`. The "Public site" workflow runs and
   publishes it (Actions tab shows a green run).
3. **GoDaddy DNS** for mailgoose.app (My Products > mailgoose.app > DNS):
   - Delete the `A` record(s) for `@` that point to GoDaddy's parking page
     (value "Parked" or a GoDaddy address). If Domain > Forwarding is set,
     remove it too.
   - Add four `A` records, name `@`: `185.199.108.153`, `185.199.109.153`,
     `185.199.110.153`, `185.199.111.153`.
   - Add four `AAAA` records, name `@`: `2606:50c0:8000::153`,
     `2606:50c0:8001::153`, `2606:50c0:8002::153`, `2606:50c0:8003::153`.
   - Set the `CNAME` record named `www` to `gremlin-technologies.github.io`
     (edit it if it exists, add it if not).
   - **Leave everything else alone**, in particular: the `MX` record, the
     `TXT` records on `@` (SPF and the Microsoft verification), the `CNAME`s
     `autodiscover`, `lyncdiscover`, `sip`, `enterpriseregistration`,
     `enterpriseenrollment` and any `selector1._domainkey` /
     `selector2._domainkey`, the `SRV` records, any `_dmarc` record, the `NS`
     records for `@` and the `NS` records for `staging` (the Route 53
     delegation). Those keep Microsoft 365 mail and staging working.
4. **Custom domain.** Settings > Pages > Custom domain: `mailgoose.app`, Save.
   Wait for the DNS check to pass.
5. **HTTPS.** Tick **Enforce HTTPS** once it's clickable (it stays greyed out
   until GitHub has issued the certificate).
6. **Optional, recommended:** verify the domain so no other GitHub account can
   claim it. In the GitHub account settings of `gremlin-technologies` (not the
   repository): Pages > Add a domain > `mailgoose.app`. Add the `TXT` record
   it shows at GoDaddy, then click Verify.

Check it's live:

```sh
dig +short mailgoose.app A      # the four 185.199.x.153 addresses
dig +short mailgoose.app MX     # unchanged: still the Microsoft 365 host
curl -sI https://mailgoose.app | head -1   # HTTP/2 200
```

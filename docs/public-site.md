# The public website at mailgoose.app

Since Nov 21 the site lives in its own public repository,
[gremlin-technologies/mailgoose-public](https://github.com/gremlin-technologies/mailgoose-public),
so this repository can be private (GitHub Pages on a free account serves only
public repositories). Its README covers the layout, local preview, publishing
and the DNS records. The knowledge base will go there too, under `site/docs/`
(mailgoose.app/docs/).

Two kinds of page there are generated from this repository, so the site says
what the app says:

- `site/assets/tokens.css`, from the light tokens in `web/src/styles.css`, by
  `scripts/site/tokens.mjs`.
- The four policy pages (`site/terms/`, `site/privacy/`, `site/anti-spam/`,
  `site/dpa/`), from `web/src/pages/Policies.tsx` and the current policy
  version, by `scripts/site/policies.mjs`.

After changing either source, with the site repository checked out next to
this one (`../mailgoose-public`; set `SITE_DIR` to point at another checkout's
`site/` folder):

```sh
node scripts/site/tokens.mjs
node scripts/site/policies.mjs
```

Then open a PR in mailgoose-public with the result. `node
scripts/site/tokens.mjs --check` exits 1 if the site's tokens are out of date.

CLI downloads are published as GitHub Releases on mailgoose-public too; see
`docs/releasing.md`.

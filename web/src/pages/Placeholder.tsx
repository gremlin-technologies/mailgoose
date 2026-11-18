import type { NavPage } from "./nav";

/** Stands in for a page until its ticket builds it. */
export function Placeholder({ page }: { page: NavPage }) {
  return (
    <section className="page">
      <h1>{page.label}</h1>
      <p>{page.about}</p>
      <p className="muted">This page is on its way.</p>
    </section>
  );
}

export function NotFound() {
  return (
    <section className="page">
      <h1>Page not found</h1>
      <p>There's nothing at this address. Pick a page from the menu.</p>
    </section>
  );
}

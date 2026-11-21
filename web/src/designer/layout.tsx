import { columnLayouts, layoutLabel, layoutWidths, type ColumnLayout } from "./blocks";

/** A small drawing of a layout's columns at their proportions. */
export function LayoutGlyph({ layout }: { layout: string }) {
  return (
    <span className="layout-glyph" aria-hidden="true">
      {layoutWidths(layout).map((w, i) => (
        <span key={i} style={{ flexGrow: w }} />
      ))}
    </span>
  );
}

/**
 * A columns block's layout, picked from a tile per layout. Going to fewer
 * columns works only while the columns that would go are empty; the API
 * says so if not.
 */
export function LayoutField({ value, onChange }: { value: string; onChange: (layout: ColumnLayout) => void }) {
  return (
    <fieldset className="field layout-field">
      <legend>Layout</legend>
      <div className="layout-options">
        {columnLayouts.map((layout) => (
          <label key={layout} className="layout-option" title={layoutLabel(layout)}>
            <input type="radio" name="layout" value={layout} checked={value === layout} onChange={() => onChange(layout)} />
            <LayoutGlyph layout={layout} />
            <span className="visually-hidden">{layoutLabel(layout)}</span>
            <span aria-hidden="true">{layout}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

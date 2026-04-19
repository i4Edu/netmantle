# NetMantle UI Style Guide

This guide documents the design tokens that drive every page of the embedded
web UI. The tokens themselves live in
[`internal/web/static/app.tokens.css`](../internal/web/static/app.tokens.css);
component CSS in `app.css` consumes them via CSS custom properties so that
new themes can be added by replacing only the variable values, not any
selectors.

The UI is intentionally framework-free: vanilla JS, no build step, no CDN
dependencies. Anything we add must keep that property — assets ship inside
the Go binary via `embed`.

## Themes

Three modes are supported, in priority order:

1. **Manual override** — when a user clicks the *Theme* button in the top
   bar, the choice (`light` or `dark`) is persisted in `localStorage` under
   the key `netmantle.theme` and reflected as `<html data-theme="…">`.
2. **`prefers-color-scheme`** — if no manual override is set, the
   `@media (prefers-color-scheme: dark)` block in `app.tokens.css` swaps
   the variables to their dark values.
3. **Light defaults** — the `:root, [data-theme='light']` rule defines the
   baseline.

Both themes consume the same set of variable names. Adding a third theme
means adding a new `[data-theme='name'] { … }` block; no component CSS
changes.

## Color tokens

| Role | Variable | Usage |
|------|----------|-------|
| Page background | `--surface-page` | Body |
| Card background | `--surface-card` | Cards, modals, the inventory split panes |
| Sunken background | `--surface-sunken` | Hovers, disabled inputs, form chrome |
| Body text | `--text-default` | Default text |
| Heading text | `--text-strong` | `h1`–`h3`, table headers |
| Muted text | `--text-muted` | Captions, secondary metadata |
| Border | `--border-default` | Card / table borders |
| Strong border | `--border-strong` | Inputs, hover state of cards |
| Brand accent | `--accent` | Primary buttons, focus ring, active sidebar mark |
| Compliant / success | `--status-ok`, `--status-ok-soft` | Green dot/badge |
| Drift / pending | `--status-warn`, `--status-warn-soft` | Amber dot/badge |
| Violation / failure | `--status-bad`, `--status-bad-soft` | Red dot/badge |
| Informational | `--status-info`, `--status-info-soft` | Blue dot/badge |

Status colors are paired (`--status-X` for the foreground and
`--status-X-soft` for the background of badges/pills). Pick a pair instead
of inventing new colors so screens stay scannable.

## Typography

| Token | Default value | Use for |
|-------|---------------|---------|
| `--font-sans` | `system-ui, …` | All UI text |
| `--font-mono` | `ui-monospace, …` | Code, diffs, action keys (`device.create`) |
| `--font-size-xs` | 0.75rem | Captions, badges |
| `--font-size-sm` | 0.85rem | Secondary metadata, table cells |
| `--font-size-md` | 0.95rem | Body |
| `--font-size-lg` | 1.1rem | Section headings |
| `--font-size-xl` | 1.4rem | Page title |

## Spacing

A 4 px base scale: `--space-1` (4 px) through `--space-6` (32 px). Always
prefer the scale over hand-rolled values to keep rhythms consistent.

## Radii, shadows, motion

* `--radius-sm` (4 px) — inputs, badges, dense controls
* `--radius-md` (6 px) — cards, modals
* `--radius-lg` (10 px) — overlay sheets
* `--shadow-sm` — resting cards
* `--shadow-md` — hovered cards
* `--transition-fast` (120 ms ease) — hover/focus feedback

## Components

The shared CSS recipes live in `app.css` and are consumed by name in
`app.js`:

* `.card`, `.card-grid`, `.device-card` — modular cards used on Inventory
  and (in PR #5) the Dashboard.
* `.status-dot`, `.badge` (`.ok` / `.warn` / `.bad` / `.info`) — quick
  visual status without text-heavy tables.
* `.filter-bar` — the top-of-page filter controls used by the Audit page
  and (in follow-up PRs) by Approvals and Compliance.
* `table.data` — the standard data table with hover highlight.
* `.placeholder` — the dashed card used by views that are not yet built so
  that the sidebar layout stays consistent through the in-flight PRs.

## Layout shell

* **Top bar** holds the brand title, the current user, the API-docs link,
  the theme toggle and Log out. Reserved for global actions.
* **Sidebar** holds the seven primary sections (Inventory, Backups,
  Compliance, Topology, Approvals, Audit, Settings). Each item is a small
  inline-SVG icon plus a label; the active item gets the accent border-left
  marker. The sidebar collapses to icons-only on screens narrower than
  720 px and exposes `data-collapsed='true'` for a future manual toggle.
* **Content area** is rendered by `app.js` based on the URL hash
  (`#/inventory`, `#/audit`, …). Each view module is a single function
  that takes the root element and renders into it.

## Adding a new view

1. Add a sidebar entry (icon + `data-route="<name>"`) in `index.html`.
2. Append `<name>` to the `ROUTES` array in `app.js`.
3. Define `views.<name> = (root) => { … }` in `app.js`.

Use the existing `el(tag, attrs, …children)` helper to build markup so
escaping is automatic.

# UI screenshots

This directory holds screenshots referenced from the top‑level
[`README.md`](../../README.md). The README intentionally references files
that may not exist yet — the broken‑image links are honest placeholders, not
fabricated UI captures.

## Why placeholders?

NetMantle is pre‑1.0 and the embedded UI evolves rapidly. We refuse to
generate or commit AI‑synthesised screenshots that misrepresent the
product. Real captures must come from a maintainer running the actual build.

## How to contribute screenshots

1. Run NetMantle locally (`make run`) or against a real fleet.
2. Capture the views below at **1440×900** (or 2× for retina) using the
   browser's responsive tools.
3. Save as PNG with the **exact** filenames listed below — the README links
   to them by name.
4. Open a PR that adds the images and updates this README if you add new
   views.

## Expected filenames

| File | View |
|------|------|
| `dashboard.png` | Landing dashboard / device inventory |
| `device-detail.png` | Single device with backup history & diff |
| `compliance.png` | Compliance findings list |
| `terminal.png` | In‑app CLI / web terminal |
| `topology.png` | LLDP/CDP topology view |

## Conventions

- Use the seeded demo tenant; do not include real customer data.
- Crop chrome (browser tabs/URL bar) out unless it adds context.
- Optimise PNGs (e.g. `oxipng -o 4`) before committing.
- Keep individual files under ~500 KB.

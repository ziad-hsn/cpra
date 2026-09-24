# CPRa brand assets

The mark is a restart loop with a heartbeat running through it: CPRa keeps
taking the pulse of a service and, when it flatlines, runs the recovery you
configured. The loop is gold because a sun makes the same circuit every day.

## Files

Everything under `dist/` is generated from the masters in `src/` and is safe
to link to or copy. Consumers should never reference `src/`.

| File | Use it for |
| --- | --- |
| `dist/svg/cpra-mark-color.svg` | The mark on light backgrounds. Default choice. |
| `dist/svg/cpra-mark-dark.svg` | The mark on dark backgrounds. Gold ring, off-white pulse. |
| `dist/svg/cpra-mark-mono.svg` | Single-colour mark. Uses `currentColor`, so it only works when the SVG is inlined (React `?react` import, MkDocs icon include), not through `<img src>`. |
| `dist/svg/cpra-mark-small.svg` | Tuned cut for 16–24 px: heavier ring and pulse, pulse starts inside the ring. Use this, not a scaled `-color`, anywhere the mark renders under 24 px. |
| `dist/svg/cpra-mark-small-dark.svg` | Same, for dark backgrounds. |
| `dist/svg/cpra-horizontal-{color,dark,mono}.svg` | Mark plus wordmark. Wordmark is outlined paths; no font required. |
| `dist/png/cpra-mark-{128,256,512}.png` | Registries, package listings, avatars. Square source; keep the mark inside a centred circle for round crops. |
| `dist/png/cpra-horizontal-1024.png` | Slides, places that cannot take SVG. |
| `dist/png/social-preview.png` | GitHub social preview, 1280×640. Uploaded manually under Settings → General → Social preview; GitHub does not read it from the repository. |
| `dist/favicon/` | The favicon set and `manifest.webmanifest`. See "Favicon" below. |

## Clear space and minimum size

- Clear space on all sides equals the ring's stroke width (7 units on the
  64-unit grid, i.e. 11% of the mark's width).
- Mark alone: 16 px minimum, using `cpra-mark-small.svg` below 24 px.
- Horizontal lockup: 120 px wide minimum. Below that, use the mark alone.

## Colour

The approved interface palette is neutral white and graphite. `palette.json` is
the source of truth; `dist/palette.css` is generated from it and shared by the
dashboard and documentation. Keep large surfaces neutral and use gold sparingly.

| Role | Light | Dark | Use |
| --- | --- | --- | --- |
| `canvas` | `#F7F8FA` | `#15181D` | Page background |
| `surface` | `#FFFFFF` | `#1D2229` | Reading area, navigation, cards and menus |
| `inset` | `#F0F2F5` | `#252B33` | Code, inputs and neutral selections |
| `text` | `#1A262E` | `#E7EBF0` | Body text and headings |
| `secondary` | `#53606C` | `#BAC3CE` | Supporting labels |
| `muted` | `#606E7C` | `#9AA7B5` | Metadata and code comments |
| `border` | `#DDE2E7` | `#343D48` | Decorative dividers |
| `control` | `#7B8794` | `#728091` | Essential input boundaries |
| `accent` | `#8A5A00` | `#E6BC61` | Underlined links and focus indicators |
| `gold` | `#E5A51F` | `#E5A51F` | Original logo ring and primary button fill |
| `onGold` | `#1A262E` | `#1A262E` | Text on gold buttons |
| `success` | `#18704A` | `#6BC69C` | Operational status |
| `danger` | `#BD223C` | `#FF8E9D` | Critical status |
| `warning` | `#965B00` | `#EFC46D` | Degraded status |
| `info` | `#156E96` | `#84C7EF` | Information and verification status |

Body text on the reading surface measures 15.43:1 in light mode and 13.36:1
in dark mode. Ink labels on the original gold measure 7.15:1. The logo gold
against white is only 2.16:1, so essential indicators and small links use the
separate `accent` color. Contrast figures use WCAG 2.x relative luminance.

The transparent logo files retain their original ink (`#1a262e`), off-white
(`#f4f2ee`), and gold (`#e5a51f`). These artwork colors are separate from UI text.
Opaque social previews, Apple touch icons and maskable icons use the neutral
light canvas. Keep the mark and wordmark geometry unchanged.

Use opaque layered surfaces, underlined body links, neutral selections, and
visible focus outlines. Pair status colors with labels. Avoid tinted page
backgrounds, gold glows, and white labels on gold buttons. Both interfaces
start with the system appearance and remember explicit Light or Dark choices.

## Typography

Wordmark: Roboto Slab Bold, tracking −0.6 units at cap height 44 on the
64-unit grid, baseline at y=55. Apache License 2.0; the font is vendored in
`fonts/` with its licence so the wordmark can be regenerated. Shipped lockups
are outlined, so nothing downstream needs the font installed.

## Do and don't

- Do use the supplied variants. Don't recolour the ring or the pulse.
- Don't stretch, rotate, outline, add shadows or gradients.
- Don't place the colour mark on a busy photograph; use the mono mark on a
  solid plate.
- Don't rebuild the wordmark in another typeface or change its casing. The
  name is written `CPRa`.

## Favicon

`dist/favicon/` contains `favicon.ico`
(32 and 16 px entries, served from the site root), `icon.svg` (carries a
`prefers-color-scheme` block), `apple-touch-icon.png` (180 px, opaque),
`icon-192.png`, `icon-512.png`, and `icon-mask.png` (maskable, art inside the
409 px safe circle), plus `manifest.webmanifest`. For the documentation at
`/cpra/`, copy the set to the generated site root and link them as:

```html
<link rel="icon" href="/cpra/favicon.ico" sizes="16x16 32x32">
<link rel="icon" href="/cpra/icon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/cpra/apple-touch-icon.png">
<link rel="manifest" href="/cpra/manifest.webmanifest">
```

Use your deployment's base path for other sites. Manifest icon paths are
relative to the manifest so they also work under a project prefix.

## Regenerating

Generate the shared CSS and the dashboard's initial theme assets with:

```sh
python3 scripts/brand/generate.py
python3 scripts/brand/generate.py --check
```

To rebuild opaque images, install `cairosvg==2.8.2` in a Python virtual environment
and run `python scripts/brand/generate.py --images`. To synchronize these assets
into a documentation checkout, add `--docs-root /path/to/gh-pages` and then run
that checkout's `_sources/rebuild.py`. Commit the generated CSS, startup assets,
PNG files, and rebuilt dashboard/site output with their sources. `make
dashboard-build` checks that generated theme text is current.


Masters carry `class="sun"` and `class="ink"` on every path so variants can
be produced by token substitution. `svgo.config.js` is the optimiser config
used for everything in `dist/svg/`; it keeps `viewBox`, `<title>`, authored
ids, and `role`/`aria-*` attributes. Raster outputs were produced with
cairosvg from the `dist/svg/` files; `favicon.ico` was packed with ImageMagick
from 32 and 16 px renders of `cpra-mark-small.svg`.

## Licence and use of the name

TODO: the artwork licence has not been decided. Until it is, the files in
this directory are copyright Ziad Hassan, all rights reserved, and are not
covered by the MIT licence that applies to the source code. You may use the
unmodified mark to refer to or link to CPRa. The recommended default, once
decided, is CC BY 4.0 for the artwork with a short trademark note.

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

| Token | Light | Dark | Notes |
| --- | --- | --- | --- |
| `--cpra-paper` | `#fbf3df` | `#15161a` | Background |
| `--cpra-ink` | `#1a262e` | `#f4f2ee` | Pulse, wordmark, body text |
| `--cpra-sun` | `#e5a51f` | `#e5a51f` | The ring. 1.95:1 on light paper, 8.4:1 on dark. On light backgrounds the ink pulse carries the mark's legibility; do not use `--cpra-sun` for text on light backgrounds. |
| `--cpra-sun-text` | `#875f10` | `#e6bc61` | Gold for links and small text where contrast is required (5.2:1 / 10.1:1 on their papers). |

Contrast figures are WCAG 2.x relative luminance.

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

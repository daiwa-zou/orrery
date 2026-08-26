# Brand

<p align="center">
  <img src="assets/orrery-banner.svg" width="720"
       alt="Orrery — multi-cluster Kubernetes console">
</p>

The mark is an orrery — concentric orbits with a body riding the inner ring —
drawn monoline so it holds together from a favicon to a banner. It is defined
once for the console in
[`web/src/components/Logo.tsx`](../web/src/components/Logo.tsx) and mirrored as
static assets for everything outside it.

## Assets

| Asset | Use |
| --- | --- |
| [`assets/orrery-mark.svg`](assets/orrery-mark.svg) | The mark alone — favicon, Helm chart icon, avatars |
| [`assets/orrery-banner.svg`](assets/orrery-banner.svg) | Mark, wordmark and tagline on the console's own ground |

Both are plain geometry with no external references, so they render the same
whether they are opened as a document or served as an `<img>`.

## Palette

The console's own tokens, defined as custom properties in
[`web/src/index.css`](../web/src/index.css). Dark is the default and the
identity; light is a second theme, not the inverse of the first.

| Token | Dark | Light | Role |
| --- | --- | --- | --- |
| `--color-canvas` | `#14181e` | `#f4f6f8` | Page ground |
| `--color-surface` | `#1a2028` | `#ffffff` | Sidebar, headers, cards |
| `--color-surface-2` | `#222a34` | `#eaeef3` | Inputs, inset tiles |
| `--color-raised` | `#1f2630` | `#ffffff` | Dropdowns, modals, toasts |
| `--color-accent` | `#5980a6` | `#3f6f9f` | Fills, bars, active rules |
| `--color-accent-text` | `#94bce3` | `#2c5c88` | Links and active text |
| `--color-ink` | `#e7eaee` | `#161b22` | Primary text |
| `--color-ink-muted` | `#a7afba` | `#4b5563` | Secondary text |
| `--color-ink-faint` | `#737d8a` | `#6b7684` | Labels, timestamps |

Status colours follow the same ramp:

| Token | Dark | Light |
| --- | --- | --- |
| `--color-ok` | `#63bd8c` | `#2f7d55` |
| `--color-warn` | `#d9a94e` | `#8a6318` |
| `--color-danger` | `#e0705c` | `#a8402e` |
| `--color-idle` | `#8b95a3` | `#6b7684` |

The light column is a re-derivation rather than an inversion: the accent is
deepened so it holds contrast on a white ground, and the status ramp is
re-picked because `#63bd8c` and `#d9a94e` wash out on paper.

Two surfaces stay dark in both themes — `--color-code` `#10141a` and
`--color-term` `#0c1015`. The terminal renders ANSI colours chosen for a dark
ground and the code pane keeps the editor's own syntax theme; inverting either
would cost legibility to buy consistency.

The theme is applied with a `data-theme` attribute on `:root`, set by
`web/index.html` before first paint, so a manual toggle can override the
system preference without a flash of the wrong palette. Corner radius is `0`
on every scale — the blueprint look; dots and orbits are the only circles.

## Type

Display type is Barlow Condensed, body text Barlow, code in the system mono
stack.

The banner's wordmark is drawn as geometry rather than set in Barlow
Condensed. A logo has to render identically for everyone, and the webfont is
not installed on the machines that read a README — set as live text it falls
back to whatever grotesque is available, which is neither condensed nor
contained. The letterforms are monoline condensed caps (cap height 100,
stroke 13, letter box 46, pitch 76) chosen to pair with the monoline orbits of
the mark.

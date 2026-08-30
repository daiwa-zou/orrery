# Handoff: Orrery — dark blueprint console redesign

> **Archived — this redesign shipped.** Kept as the record of where the
> console's visual language came from, and for the prototype and reference
> screenshots. It is a snapshot of the brief as written, so it describes the
> work in the future tense and names the stack as it stood then (React 18; the
> app is on 19). **Do not treat it as current.** The live palette is
> `web/src/index.css` — which has since gained a light theme this brief does
> not mention; frontend conventions are in
> [web/README.md](../../web/README.md).

## Overview
A full UI redesign of **Orrery**, the multi-cluster Kubernetes dashboard in this repo (`orrery/web`). It restyles the existing SPA in an "Industry" dark-blueprint visual language (steel-blue accent, Barlow Condensed/Barlow, square corners, hairline borders, `+` corner registration marks) and adds a fleet orbital diagram, an incident walkthrough, and several new states. Structure and copy were derived from the real source in `orrery/web/src` — this is a re-skin plus additions, not a rebuild.

## About the Design Files
The files in this bundle are **design references created in HTML** (`Orrery Console.dc.html` is an interactive prototype). They are not production code. The task is to **recreate this design inside the existing codebase** — React 18 + react-router + TanStack Query + Tailwind 4 (`orrery/web`) — by editing the existing components, primarily the design tokens in `src/index.css` and the components listed per screen below. Do not port the prototype's inline-style approach; express everything through the existing Tailwind token system.

## Fidelity
**High-fidelity.** Colors, typography, spacing and copy in the prototype are final. Recreate pixel-perfectly using the codebase's existing component structure.

## Source-file map (this repo)
The app already implements almost every screen; the redesign mostly changes tokens and adds elements.

| Screen in prototype | Existing source | Work |
| --- | --- | --- |
| Login | `src/pages/Login.tsx` | Restyle; new logo mark; blueprint card |
| Fleet | `src/pages/Fleet.tsx` | Restyle cards; **add orbital diagram (new)**; add status legend |
| App shell / sidebar / header | `src/components/AppShell.tsx` | Restyle; logo mark; `?` shortcuts button (new) |
| Overview | `src/pages/Overview.tsx` | Restyle; add "Top pods by usage" + "Control plane" cards (new) |
| Resource lists | `src/pages/ResourceList.tsx`, `DataTable.tsx` | Restyle; CPU/Memory **usage bars** in pod rows (new render for metrics columns) |
| Resource detail | `src/pages/ResourceDetail.tsx` | Restyle; container/conditions tables per prototype |
| Logs | `src/components/LogViewer.tsx` | Restyle; stderr lines in danger color; "previous instance" banner (new) |
| Terminal | `src/components/Terminal.tsx` | Restyle chrome only (xterm stays) |
| YAML | `src/components/YamlEditor.tsx` | Restyle; staged-change gutter dot + "n staged change(s)" chip |
| Events | `src/pages/Events.tsx` | Restyle |
| Create | `src/pages/CreateResource.tsx` | Restyle; blueprint frame around editor |
| Command palette | `src/components/CommandPalette.tsx` | Restyle |
| Modals / toasts | `src/components/primitives.tsx`, `Toast.tsx` | Restyle; drain dry-run result panel (new) |
| Shortcuts overlay | — | **New** small modal (see prototype) |
| Incident walkthrough | — | **New, optional**: floating stepper card; skip if out of scope |

## Design Tokens
Replace the `@theme` block in `src/index.css` with:

```css
--color-canvas:  #14181e;   /* page ground */
--color-surface: #1a2028;   /* sidebar, headers, cards */
--color-surface-2:#222a34;  /* inputs, inset tiles (code panes use #10141a, terminal #0c1015) */
--color-border:  rgba(231,234,238,.12);   /* hairlines */
--color-border-strong: rgba(231,234,238,.22);
--color-ink:       #e7eaee;
--color-ink-muted: #a7afba;
--color-ink-faint: #737d8a;
--color-accent:      #5980a6;  /* fills, bars, active rules */
--color-accent-soft: rgba(89,128,166,.16);
/* accent text steps (from the Industry ramp): #94bce3 (links/active), #b5d9fd (hover) */
--color-ok:     #63bd8c;  --color-warn: #d9a94e;
--color-danger: #e0705c;  --color-info: #94bce3;  --color-idle: #8b95a3;
```

- **Type**: headings `'Barlow Condensed', sans-serif` weight 600 (google font, weights 400/600); body `'Barlow', sans-serif` (400/500/700); mono `ui-monospace,'SF Mono',Menlo,monospace`. Base body size 14px. H1 sizes: fleet 30px, overview 24px, list toolbar 17px, detail 18px. Section headings inside cards: 11px, 600, letter-spacing .1em, uppercase, ink-faint.
- **Radius: 0 everywhere.** No rounded corners — badges, buttons, cards, inputs, dropdowns are all square. (Only dots/orbits are circles.)
- **Badges**: 11px, padding 2px 8px, `background: <tone> at ~12% alpha`, `color: <tone>`, `border: 1px solid <tone> at ~30% alpha`, `white-space: nowrap`. Tone mapping identical to the existing `toneFor()` in `src/lib/format.ts`.
- **Blueprint frames**: key cards (fleet cards, overview cards, login card, metadata/containers/status cards, shortcuts modal, walkthrough) get a 1px `--color-border` border plus four `+` corner registration marks: 11px × 11px crosses centered on each corner (offset −6px), color `rgba(231,234,238,.55)` at 1px stroke. See `.blueprint > .corner` in the bundled `styles.css`.
- **Usage bars**: track `#14181e` with 1px border hairline; fill `--color-accent`, switching to `--color-warn` ≥75% and `--color-danger` ≥90%. Table cell bars: 52×4px + mono value right-aligned. Overview capacity bars: 7px tall; node utilisation: 5px.
- **Shadows**: dropdowns/modals `0 16px 40px rgba(0,0,0,.6)` with `--color-border-strong` border.
- **Focus**: `outline: 2px solid var(--color-accent); outline-offset: 2px`.
- **Icons**: Lucide, stroke-width 1.5, 13px in toolbars.
- **Motion**: overlays fade in 140ms ease-out + 3px rise (existing `.animate-in`); live dots pulse 2s; orbital rings animate `stroke-dashoffset` (6/9/13s linear infinite); honor `prefers-reduced-motion`.

## New elements (not in the current app)

### Logo mark
Concentric-orbit SVG: outer circle r15 stroke `#5980a6`; (login only) middle dashed orbit r9.5 stroke `rgba(148,188,227,.5)`; center dot r2.8 fill `#94bce3`; small "planet" dot on the outer ring fill `#5980a6`. Wordmark "ORRERY" in Barlow Condensed 600, letter-spacing .1–.12em.

### Fleet orbital diagram
Full-width blueprint-framed panel above the cluster cards (`viewBox 0 0 900 320`):
- Kicker row: `FLEET ORBIT — LIVE PROBES` (10px mono, accent) left; `probe interval 15s · click a body to open` right (ink-faint).
- Center hub at (450,160): crosshair + dot r3.4 `#94bce3`, label "orrery" beneath.
- Three dashed elliptical rings (rx 58/98/138, ry = rx × .72), stroke hairline, dash `4 4`, animated dashoffset.
- One body per cluster: dot r4.5 filled by health tone with a 1.5px canvas-color ring, 9px mono label beneath (abbreviate `prod-`→`p-`, `staging-`→`s-`). Unhealthy bodies pulse. Click navigates to the cluster. Distribute 3/4/5 bodies across the rings at even angles.

### Incident walkthrough (optional)
Floating blueprint card, fixed bottom-left, 330px: mono kicker `INCIDENT WALKTHROUGH · STEP n/5`, ✕ dismiss, condensed 17px title, 12.5px muted body, one primary button that navigates to the next stop (fleet → overview → pod → previous logs → deployment YAML → apply).

### Other additions
- **Shortcuts overlay**: `?` opens; rows of label + `<kbd>` (⌘K, ?, esc, ↑↓, ⏎); note that keystrokes inside xterm belong to the shell.
- **Previous-logs banner** (LogViewer, when `previous` is on): info-tinted line "Showing the previous container instance — this is the run that exited 137 (OOMKilled) at …".
- **Drain dry-run**: drain modal gains a "Dry run" secondary action; result panel lists pods that would be evicted (DaemonSet pods marked "ignored") plus a warn line for PDB-blocked pods.
- **Overview extras**: "Top pods by usage" card (name / CPU / memory rows, linking to pod detail) fed from metrics-server; "Control plane" info card (API server URL, version, platform, CNI, auth mode, authz cache stats from `/stats`).

## Interactions & Behavior
All existing behavior (routing, live lists, RBAC dimming, palette ranking, log coalescing) stays exactly as implemented in `orrery/web/src` — the prototype mirrors it. Notables to preserve while restyling:
- Active nav row = 2px left accent rule + `accent-soft` tint (not a filled block); denied rows dim to 40% with explanatory `title`.
- Buttons: primary = solid accent with dark (canvas) text; secondary = hairline border, text ink; danger = danger tint bg + danger border/text; ghost = accent text. All `white-space: nowrap`.
- Row hover: `rgba(231,234,238,.045)`. Clickable rows only where a detail exists.
- Toasts: 300px, surface bg, 2px left border in tone color, bottom-right stack (cap 4), auto-dismiss ~5s.

## State Management
No new client state beyond: shortcuts-overlay open, walkthrough step/dismissed (persist dismissed in localStorage), drain dry-run result. Everything else uses existing hooks/state.

## Assets
- Google Fonts: Barlow (400/500/700) + Barlow Condensed (400/600).
- Lucide icons (existing `icons.tsx` can stay; render at stroke 1.5).
- Logo/orbital SVGs are inline in the prototype — copy the markup.
- Design-system reference: bundled `industry-styles.css` (token sheet + `.blueprint`/`.corner` implementation).

## Files
- `Orrery Console.dc.html` — the interactive prototype (open in a browser; requires `support.js` and `_ds/` from the design project — view it in the design tool for full behavior). All colors/spacing above were extracted from it.
- `industry-styles.css` — the Industry design-system stylesheet the prototype layers its dark tokens onto (source of `.blueprint`, `.btn`, `.table`, `.dialog` patterns).

## Screenshots
Reference captures of every screen are in `screenshots/`:
- `screenshots/01-fleet.png`
- `screenshots/02-overview.png`
- `screenshots/03-pods-list.png`
- `screenshots/04-pod-detail.png`
- `screenshots/05-pod-logs-previous.png`
- `screenshots/06-pod-terminal.png`
- `screenshots/07-deployment-yaml-staged.png`
- `screenshots/08-events-feed.png`
- `screenshots/09-command-palette.png`
- `screenshots/10-secrets-403.png`
- `screenshots/11-node-drain-dry-run.png`
- `screenshots/12-shortcuts-overlay.png`
- `screenshots/13-login.png`

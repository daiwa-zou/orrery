# The Orrery console

The single-page app the Go backend serves. React 19, Vite, Tailwind 4,
TanStack Query, React Router. Everything it renders comes from
`/api/v1` — there is no build-time knowledge of Kubernetes in here, which is
why a CRD installed a minute ago browses like a built-in kind.

Read this if you are working in `web/`. For the system as a whole start at the
[repository README](../README.md); for the endpoints this talks to, see
[docs/API.md](../docs/API.md).

## Running it

From the repository root:

```bash
make run       # the Go backend on :8080, against configs/orrery.dev.yaml
make web-dev   # this app on :5173
```

The dev server proxies `/api` — REST *and* WebSocket — to the backend, so the
browser sees one origin and cookies behave exactly as they do in production.
Point it somewhere else with `ORRERY_API` when :8080 is already taken:

```bash
ORRERY_API=http://127.0.0.1:8081 npm run dev
```

## Layout

```
src/api/        client.ts (fetch + WS URLs), hooks.ts (TanStack Query), types.ts
src/pages/      one file per route: Fleet, Overview, ResourceList,
                ResourceDetail, Events, CreateResource, Login
src/components/ AppShell, DataTable, SearchBar, CommandPalette, LogViewer,
                Terminal, YamlEditor, MetadataEditor, ExplainPanel, …
src/lib/        pure logic — search parsing, label handling, formatting,
                localStorage, selection. This is what the unit tests cover.
src/index.css   the design tokens, and the only place colours are defined
```

`src/lib/` is deliberately free of React: the tests (`npm test`) run in a plain
Node environment with no DOM, because the logic worth testing — query parsing,
palette ranking, nav partitioning — is pure.

## Conventions worth knowing

**Typecheck with `tsc -b`, never `tsc --noEmit`.** `tsconfig.json` is a
project-references root with an empty `files` list, so `--noEmit` typechecks
nothing at all and passes on anything. `make web-typecheck` and CI use `-b`.

**Colours come from tokens, never literals.** `src/index.css` defines the dark
palette on `:root` and the light one under `:root[data-theme='light']`; a
hard-coded hex breaks the light theme silently. Each token's role is named in a
comment beside it, and `contrast.test.ts` holds the pairings that matter to
4.5:1.

**Server state belongs to TanStack Query, not `useState`.** Lists then keep
their data while a filter changes (`keepPreviousData`) instead of flashing
empty, and the WebSocket layer in `hooks.ts` patches cached rows in place.

**Permission is rendered, not hidden.** The backend's `/access` response drives
dimming plus a tooltip explaining what is missing. Do not remove a control the
user lacks permission for — a control that vanishes is indistinguishable from a
feature that does not exist.

**Live updates are not all applied the same way.** `MODIFIED` is spliced into
the visible row so a status change is instant; `ADDED` and `DELETED` change
which objects belong on the page, so they trigger a debounced refetch that
keeps pagination and sorting honest.

**Persisted UI state goes through `src/lib/storage.ts`.** Recents, saved views,
per-resource extra label columns and the theme all live in `localStorage`, and
every access is guarded — Safari in private mode throws from `localStorage`
rather than returning null.

**Do not hand-tune chunks in `vite.config.ts`.** The editor and terminal are
reached through dynamic imports and the bundler splits them there by itself;
naming them in `manualChunks` used to drag shared modules along and made the
entry statically import ~450K nobody asked for.

## Commands

| Command | What it does |
| --- | --- |
| `npm run dev` | Vite dev server on :5173, proxying `/api` |
| `npm run build` | `tsc -b` then a production build into `dist/` |
| `npm test` | Vitest, `src/**/*.test.ts` |
| `npm run lint` | Oxlint |
| `npx tsc -b` | Typecheck only |

`make bundle` at the repository root builds this app and embeds `dist/` into
the Go binary, which is what release builds ship.

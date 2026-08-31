# Contributing

Thanks for looking. Setup, the dev clusters and the local OIDC flow are in the
[README](README.md#quick-start); this file covers only the things that are not
obvious from running the code.

Security bugs go through [SECURITY.md](SECURITY.md), not a pull request.

## The gate

```bash
make check
```

That is exactly what CI gates on — `go vet`, `go test ./... -race`, then the
frontend's typecheck, lint, tests and build, and a Helm lint. Running it before
pushing saves a round trip; CI runs the same thing plus the container build and
the security scans.

Three traps, all of which have cost a red CI run here:

- **`tsc -b`, not `tsc --noEmit`.** `web/tsconfig.json` uses project references
  with an empty `files` list, so `--noEmit` typechecks nothing and passes on
  anything. `make web-typecheck` uses the right one.
- **Do not run Prettier in `web/`.** There is no Prettier config and no
  `format` script — the style there (no semicolons, single quotes) is
  maintained by hand, and Prettier's defaults rewrite every file it touches.
  Oxlint does not check formatting, so nothing will catch it for you.
- **`gofmt` is enforced.** It runs locally, unlike some of the other checks, and
  it is the most likely thing to fail an otherwise-green PR. Note that inserting
  a comment into a struct splits its field-alignment group, so the fields around
  it need re-aligning.

## Pull requests

`main` is protected and requires the branch to be up to date with it, so a PR
that sat through someone else's merge has to have `main` merged in before it
will go green. `gh pr merge <n> --merge --auto` arms it to land by itself once
CI passes.

## Commit messages

A single evocative subject line, then a multi-paragraph prose body explaining
why the change is right — what the old behaviour actually did, what was
considered and rejected, and how it was verified. `git log` here reads as an
argument rather than a list, and that is deliberate: most of these changes are
about a distinction that is invisible in the diff.

Recent subjects give the flavour:

```
Hold Watch to the order it promises
Do not call a broken log stream a quiet container
Say how many matches the palette is not showing
Give each refreshed session its own groups
```

## Tests

New behaviour needs a test, and the test needs to have been *seen to fail*.
Revert the change, run it, check the failure message names the actual bug, then
restore. A test that passes with the fix reverted is not covering the fix — it
has happened here more than once, and it reads as cover while covering nothing.

The fake API server in `internal/api/fakecluster_test.go` has a knob per failure
mode the code is required to tell apart: a review that is *denied*
(`denyResource`) is not a review that *errored* (`failReviewResource`), and
neither is a resource whose cache cannot be built (`breakCacheResource`). Reach
for the existing knob rather than adding a fourth spelling for the same idea.

## The thing to know before changing behaviour

This codebase separates three answers that all look like an empty response:
**"you may not"**, **"we could not"**, and **"there is nothing"**. `countSummary`
carries `Forbidden` and `Unavailable` for exactly that reason, list and search
responses carry `warnings`, and the console renders each differently.

Collapsing them is the recurring bug here, and it is never visible in the diff —
it looks like a tidy-up. An empty list with no error is read as an answer, so a
scan that was refused, a cache that could not be built and a namespace that is
genuinely empty must not arrive at the reader as the same thing.

[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) explains the caching and
authorization design this rests on, and
[docs/DECISIONS.md](docs/DECISIONS.md) records what was deliberately not built.

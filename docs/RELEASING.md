# Releasing

Everything is driven by a `v*.*.*` tag. Two workflows fire off it
independently, and a third publishes continuously from `main`.

## Cutting a release

The chart version has to be bumped *before* the tag exists, because the
release fails on a mismatch (see [below](#why-the-chart-version-is-checked)).

1. Bump `version` and `appVersion` in
   [`deploy/helm/orrery/Chart.yaml`](../deploy/helm/orrery/Chart.yaml) to the
   version you are about to release, without the `v` — for `v1.2.3`, both
   become `1.2.3`.
2. Open a PR with that bump and merge it to `main`. (`main` is protected;
   direct pushes are rejected.)
3. Tag the merge commit and push the tag:

   ```bash
   git switch main && git pull
   git tag v1.2.3
   git push origin v1.2.3
   ```

That is the whole process. The tag must be pushed to the remote before the
binaries workflow will accept it — it runs `gh release create --verify-tag`.

## What a tag produces

**Container images** — [`release.yaml`](../.github/workflows/release.yaml)
publishes `ghcr.io/daiwa-zou/orrery` as `1.2.3`, `1.2` and `latest`.
Multi-arch (amd64 + arm64), distroless and nonroot, with a provenance
attestation pushed to the registry.

**Binaries** — [`release-binaries.yaml`](../.github/workflows/release-binaries.yaml)
builds the SPA, copies it under `internal/webfs/` and cross-compiles five
targets with `-tags bundleweb`, so each binary carries the whole dashboard and
needs nothing else installed:

```
linux/amd64  linux/arm64  darwin/amd64  darwin/arm64  windows/amd64
```

They are attached to a GitHub Release along with `SHA256SUMS`, with notes
generated from the commits since the previous tag.

## What `main` produces

Every push to `main` publishes `ghcr.io/daiwa-zou/orrery:main` and
`:sha-<short>`, and refreshes the coverage badge. No binaries and no GitHub
Release — those are tag-only.

## Version stamping

`main.version` is stamped at build time and surfaced by `orrery -version`:

| Build | Reports |
| --- | --- |
| Release binary | the tag, e.g. `v1.2.3` |
| Image from a tag | `1.2.3` |
| Image from `main` | `main` |
| Local `go build` / `make build` | `dev`, or `git describe` via the Makefile |

The container build takes it as a `VERSION` build argument, defaulted to `dev`
in the [`Dockerfile`](../Dockerfile), so a plain `docker build` still works.

## Why the chart version is checked

The chart is installed from a git checkout rather than a chart repository, so
`image.tag` resolves to whatever `Chart.yaml` says at that commit — it
defaults to `.Chart.AppVersion`. If `appVersion` disagrees with the tag being
released, the chart points at an image that release never published, and
`helm install` fails with `ImagePullBackOff`.

Stamping `Chart.yaml` during CI would not help: the workflow's checkout is
discarded, and users install from the repository. So the release instead
*verifies* the two agree and fails loudly if they do not, turning silent drift
into a build error.

## Publishing the chart

The chart is not published anywhere today — there is no `chart-releaser` and
no `helm push`. Installing means cloning the repository. Publishing it as an
OCI artifact to GHCR alongside the image would remove that step, and would
make the `Chart.yaml` bump above enforce itself naturally.

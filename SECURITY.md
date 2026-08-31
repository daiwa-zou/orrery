# Security policy

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub's
[private vulnerability reporting](https://github.com/daiwa-zou/orrery/security/advisories/new),
which opens a draft advisory visible only to you and the maintainers. It needs
no email address and no prior contact.

A useful report says which version or commit you tested, how the instance was
configured — the auth mode matters more than anything else here — and what an
attacker gets. A proof of concept is welcome but a clear description of the
mechanism is worth more than a working exploit.

Expect an acknowledgement within a few days. This is a small project without a
paid security team, so please allow reasonable time for a fix before disclosing
publicly; a fix and an advisory will be published together.

## What is in scope

Anything that lets someone see or change what the cluster's RBAC says they may
not. In particular:

- **Bypassing the access review.** Every read and every write is preceded by a
  `SubjectAccessReview`, and reads are served from caches populated with the
  dashboard's own credentials — so a path that reaches cached data without that
  check is the most serious class of bug this project has. See
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **Crossing between users.** Cached authorization verdicts, per-identity
  clients and session state are all keyed by subject; a key collision serves one
  user's answer to another.
- **Session and login flow.** Cookie handling, CSRF, the OIDC state and nonce,
  and open redirects through `returnTo`.
- **The workload proxy**, which relays into pods and services and is therefore
  the most direct route from a browser to something inside the cluster.
- **Escaping the read-only surface**, including anything that turns a `GET`
  into a write.

## What is not

- **The impersonation trade itself.** Under the default auth mode the
  dashboard holds one credential per cluster and adds the user's identity as
  headers, which means its service account can impersonate anyone. That is the
  design, it is stated plainly in the README, and the pod must be treated as a
  sensitive workload. Reports that this is dangerous are correct and not
  actionable; reports that some path *escapes* the impersonated identity are
  very much in scope.
- **Findings that require an already-compromised cluster credential**, or
  administrative access to the deployment.
- **Deployments that ignore the documented posture** — no TLS,
  `session.secure: false` outside local development, a hub service account
  granted far more than [docs/RBAC.md](docs/RBAC.md) asks for.
- **Scanner output without a demonstrated impact on this codebase.**

## Supported versions

Fixes land on `main` and go out in the next tagged release. There are no
maintained release branches, so "upgrade to the latest tag" is the whole
support policy.

## Hardening a deployment

[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) covers the production posture and
[docs/RBAC.md](docs/RBAC.md) covers narrowing what the dashboard can cache at
all — including keeping a resource family such as certificates or secrets out
of its caches entirely, which is the most effective control available to an
operator.

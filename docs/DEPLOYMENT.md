# Deployment

How to run Orrery in production, what actually needs to scale, and the
topologies that work. The [README](../README.md#deploying-it) has the
five-minute version; this is the rest.

## The shape of the workload

One container serves everything: the Go backend embeds the compiled SPA
(`gcr.io/distroless/static` base, no shell, runs as nonroot, read-only root
filesystem). Two listeners: `:8080` for the app and API, `:9090` for
Prometheus metrics, kept separate so the scrape endpoint never sits behind
auth.

Per-user state lives in exactly one place — the session store. Everything else
a pod holds (informer caches, authz verdict cache, discovery cache) is a
per-pod cache that any pod can rebuild from the API servers. Consequences:

- **Any pod can serve any request.** No sticky sessions, no session affinity,
  provided sessions are in Redis and the encryption key is shared.
- **Restarts are cheap but not free.** A new pod re-fills informer caches on
  first use; the first request for a resource pays the `cache.syncTimeout`
  fill. WebSocket clients reconnect automatically and the UI refetches.
- **Memory is the resource that matters.** Informer caches dominate; CPU is
  mostly JSON serialization and fan-out. Budget memory by the number of
  objects the dashboard is asked to hold — `GET /api/v1/clusters/{c}/stats`
  reports what is actually cached and for how long it has been idle.

## Prerequisites

The chart does not create secrets, and it does not ship Redis.

```bash
# 1. Session encryption key — shared by all replicas.
kubectl create secret generic orrery-session \
  --from-literal=encryptionKey="$(openssl rand -base64 32)"

# 2. OIDC client secret (when oidc.enabled).
kubectl create secret generic orrery-oidc \
  --from-literal=clientSecret='...'

# 3. A Redis instance. The chart's default expects one reachable at
#    redis://orrery-redis:6379/0 — deploy your own (any Redis chart or
#    operator works; persistence is optional, losing it only signs users out):
helm install orrery-redis oci://registry-1.docker.io/bitnamicharts/redis \
  --set architecture=standalone --set auth.enabled=false \
  --set fullnameOverride=orrery-redis
```

Then:

```bash
helm install orrery ./deploy/helm/orrery \
  --set publicURL=https://orrery.example.com \
  --set oidc.issuer=https://accounts.example.com \
  --set oidc.clientID=orrery \
  --set ingress.enabled=true --set ingress.host=orrery.example.com
```

For a single-replica installation you can skip Redis entirely:

```bash
--set replicaCount=1 --set session.store=memory \
--set podDisruptionBudget.enabled=false
```

but accept that every deploy and every pod restart signs everyone out.

## Recommended topologies

### Small (a handful of users, a few clusters)

One replica, `session.store: memory`, no Redis, 512Mi–1Gi memory. Fine for a
team dashboard where a rare re-login after a deploy is acceptable. This is the
lowest-operational-surface option and is genuinely enough for most internal
use.

### Standard (the default the chart aims at)

2–3 replicas, Redis sessions, PodDisruptionBudget `minAvailable: 1`, ingress
with long proxy timeouts. Zero-downtime deploys: rolling update replaces pods
one at a time, live sessions are unaffected (they live in Redis), and open
WebSocket streams on a terminating pod drop and reconnect to a survivor.

Spread replicas across nodes so one node loss does not take both:

```yaml
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: orrery
```

### Large fleets / multiple regions

Two workable shapes:

- **One central Orrery** with remote clusters registered via mounted
  kubeconfigs. Simplest for users (one URL, one login), and the shared caches
  keep API-server load flat as users are added. The costs: the central
  instance holds caches for every cluster (memory), every watch crosses the
  WAN, and the dashboard's network position must reach every API server.
- **One Orrery per region/environment** behind separate URLs, each managing
  its local clusters. Better failure isolation (a region's dashboard dies with
  the region, not the fleet) and keeps watch traffic local. The cost is N
  deployments and N OIDC clients (or one client with N redirect URIs).

Prefer per-region instances once WAN watch traffic or the memory footprint of
a central instance becomes noticeable; prefer central below that. Do not run
one Orrery per cluster — at that point the multi-cluster fleet view is lost
and you have bought pure overhead.

### Scaling out and what it buys

Replicas share nothing except Redis, so horizontal scaling is linear for
request fan-out — but each replica maintains **its own** informer caches and
watches. Ten replicas means ten watches per hot resource per cluster and ten
copies of the cache in memory. Adding replicas therefore:

- helps: request throughput, WebSocket fan-out capacity, availability;
- does not help: memory per pod (unchanged), API-server watch load (increases
  with replica count).

Keep the replica count modest (2–4) and scale **up** (memory) rather than out
when the driver is cluster size. If you enable the HPA, note it scales on CPU
utilization; the memory-dominated failure mode (huge cluster, many CRDs) is
better handled by raising limits and `cache.maxInformersPerCluster` than by
autoscaling.

## The proxy in front

Log follows, watches and exec sessions are WebSockets that stay open for
hours. The chart sets nginx-ingress read/send timeouts to 3600s; any other
layer (ALB idle timeout, Cloudflare, corporate proxy) needs the same
treatment or streams will be severed mid-session. The UI reconnects, but a
60-second idle timeout makes the terminal unusable.

`publicURL` must be the URL in the browser's address bar — it anchors the
OIDC redirect and the WebSocket `Origin` check. TLS terminates at the ingress;
Orrery itself speaks plain HTTP inside the cluster and marks its cookies
`Secure` (the chart's config pins `session.secure: true`).

## Security posture

Read [the RBAC template](../deploy/helm/orrery/templates/rbac.yaml) before
installing — the grants are commented and deliberate. It covers only the
cluster Orrery runs in; every remote cluster needs its own credential, and
[deploy/remote-cluster](../deploy/remote-cluster/) has ready-made manifests for
both auth modes plus a `preflight.sh` that verifies one before you register it.
The short version: the
dashboard's service account can read broadly (to fill shared caches; per-user
reads are gated by SubjectAccessReview above the cache) and can **impersonate
any user or group**. Treat the pod accordingly:

- Restrict who can `exec` into or read secrets in the release namespace as
  tightly as you restrict cluster-admin, because the pod's credential is a
  path to any identity.
- Add a NetworkPolicy that allows ingress only from your ingress controller
  and egress only to API servers, Redis and the OIDC issuer.
- Send the audit log somewhere: under impersonation every write is attributed
  to the real user, which is the point.
- Secret **values** are never cached (they are stripped before entering the
  informer cache, and opening a secret refetches it under the viewer's own
  identity), so a heap dump does not contain every credential in the fleet.
- Keep `/metrics` (`:9090`) unexposed; it is a separate listener precisely so
  the ingress never routes to it.

For clusters where impersonation is too strong a grant, register them in
`passthrough` mode instead — the dashboard then holds no privileged credential
for them and forwards each user's own token (see [OIDC.md](OIDC.md)).

## Configuration you will actually tune

| Setting | When to change it |
| --- | --- |
| `resources.limits.memory` | Big clusters, many CRDs, many concurrent viewers. Watch `orrery` cache gauges / `stats` endpoint. |
| `cache.maxInformersPerCluster` | Raise when users legitimately browse more distinct resource types than the cap; lower to bound memory harder. |
| `cache.idleTimeout` | Raise for snappier repeat visits at the cost of memory; lower on memory-constrained installs. |
| `authz.ttl` | Lower for faster revocation propagation, at the cost of more SubjectAccessReviews. 30s is a good default. |
| `session.ttl` / `idleTimeout` | Your org's session policy. |
| `clusters[].qps` / `burst` | Bound what one dashboard may put on one API server (defaults 50/100). |

## Upgrades

Config changes roll the pods automatically (the deployment carries a checksum
of the ConfigMap). Rolling updates are safe with Redis sessions; with the
memory store every upgrade is a logout. Watch consumers handle pod
replacement: the client reconnects and refetches, and a client that missed
events is told to reload rather than shown stale data.

## Known limitations of the current setup

An honest list, so you can decide whether any of them matter for your install:

- **Redis is a hard external dependency of the chart's defaults.** Install it
  or flip to the single-replica memory profile; the chart will not do it for
  you.
- **The HPA keys on CPU** while the binding resource is usually memory.
  Autoscaling is off by default for that reason.
- **Per-replica caches multiply watch load.** Inherent to the
  shared-nothing design; keep replica counts small.
- **No NetworkPolicy template in the chart** — bring your own, allowing
  ingress from the ingress controller and egress to API servers, Redis and
  the OIDC issuer only.
- **One Deployment serves API + static assets.** At very large user counts
  you could front the SPA with a CDN (`server.webRoot` empty disables static
  serving), but the SPA is small; do this only if you already operate a CDN.

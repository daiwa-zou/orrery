# Deployment

How to run Orrery in production, what actually needs to scale, and the
topologies that work. The [README](../README.md#deploying-it) has the
five-minute version; this is the rest.

## Three things that bite people

Each of these looks like an unrelated bug when you hit it, so they are worth
recognising by symptom:

- **Users are randomly signed out.** The session encryption key is not shared
  across replicas, so each pod minted its own at boot and a request landing on
  a different pod does not recognise the cookie. The server logs a warning
  when it generates one. See [Prerequisites](#prerequisites).
- **Requests look unauthenticated at random.** More than one replica with
  `session.store: memory`, which keeps sessions inside the pod. The chart
  defaults to Redis for exactly this reason; point `session.redisURL` at your
  instance.
- **Terminals and log follows die after a minute.** A proxy in front is
  timing out long-lived streams. See [The proxy in front](#the-proxy-in-front).

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
when the driver is cluster size. The HPA is off by default for that reason. If
you do enable it, the chart targets memory as well as CPU
(`autoscaling.targetMemoryUtilizationPercentage`, set either to `null` to drop
that metric) and defaults `scaleDown` to a ten-minute stabilisation window,
because a replica that goes away takes warm caches with it and its replacement
refills them from the API server on first use — without the window a busy
period becomes a sawtooth of cache rebuilds. Even so, the memory-dominated
failure mode (huge cluster, many CRDs) is better handled by raising limits and
`cache.maxInformersPerCluster` than by adding pods.

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

## Turning the HTTP proxy off

The console can relay GET and HEAD to a pod or service through the API
server's proxy subresource, under the caller's own identity — the browser's
answer to `kubectl port-forward` for HTTP workloads. It is gated on the
`pods/proxy` and `services/proxy` subresources like every other read.

Set `proxy.enabled: false` (chart) or `proxy: {enabled: false}` (config) to
remove it. That unregisters the route rather than hiding the button, so it
cannot be reached by typing the URL, and the console stops offering the
control instead of offering one that 404s. `-print-config` reports the
resolved value.

Reasons to: the workloads a cluster runs are not ones you want rendered inside
the console's origin, or policy says a dashboard may read the API server and
nothing behind it. Leaving it on is fine otherwise — it is read-only, and the
subresource check is the same authority as everything else here.

## Configuration you will actually tune

Every field has a default; `-print-config` shows what the server actually
resolved, with secrets masked.

| Setting | Default | What it controls, and when to change it |
| --- | --- | --- |
| `resources.limits.memory` | `2Gi` | Big clusters, many CRDs, many concurrent viewers. Watch `orrery` cache gauges / `stats` endpoint. |
| `cache.maxInformersPerCluster` | `64` | Ceiling on concurrent caches per cluster; the least recently used is retired past it. Raise when users legitimately browse more distinct resource types than the cap; lower to bound memory harder. |
| `cache.idleTimeout` | `10m` | How long an unwatched resource cache survives before being stopped. Raise for snappier repeat visits at the cost of memory; lower on memory-constrained installs. |
| `authz.ttl` | `30s` | How long an access-review verdict is cached. Lower for faster revocation propagation, at the cost of more SubjectAccessReviews. |
| `authz.namespaceScanLimit` | `200` | Bounds the per-namespace probe used for users without cluster-wide read. Truncation is reported to the UI, never hidden. |
| `session.ttl` / `idleTimeout` | `12h` / `2h` | Your org's session policy. |
| `clusters[].qps` / `burst` | `50` / `100` | Bound what one dashboard may put on one API server. |
| `proxy.enabled` | `true` | The read-only HTTP proxy into pods and services. `false` removes the route entirely — see [above](#turning-the-http-proxy-off). |
| `debug.image` | `busybox:1.37` | The image an ephemeral debug container runs. Deliberately the operator's choice, not the caller's: a console that took an image name from the browser would be a way to run arbitrary code inside another workload's namespaces. **Not exposed by the chart** — see [Known limitations](#known-limitations-of-the-current-setup). |

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
- **Autoscaling cannot fix the resource that binds.** The chart can now scale
  on memory as well as CPU, but adding replicas does not divide memory here —
  each one holds its own caches. It stays off by default; raising limits is
  still the first move. [DECISIONS.md](DECISIONS.md#autoscaling-on-cpu) has the
  longer version.
- **Per-replica caches multiply watch load.** Inherent to the
  shared-nothing design; keep replica counts small.
- **`debug.image` is not a chart value.** The ephemeral-container image is a
  config-file field the ConfigMap template does not render, so a Helm install
  gets the `busybox:1.37` default. Overriding it today means editing
  `templates/configmap.yaml` (or mounting your own config). Fine if busybox is
  what you want; a blocker if your registry policy forbids Docker Hub.
- **No NetworkPolicy template in the chart** — bring your own, allowing
  ingress from the ingress controller and egress to API servers, Redis and
  the OIDC issuer only.
- **One Deployment serves API + static assets.** At very large user counts
  you could front the SPA with a CDN (`server.webRoot` empty disables static
  serving), but the SPA is small; do this only if you already operate a CDN.

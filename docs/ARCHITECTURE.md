# Architecture

This document covers the decisions that are not obvious from the code, and the
trade-offs behind them.

## The problem with the obvious design

The naive multi-cluster dashboard proxies each browser request to the relevant
API server using the user's credentials. It is simple and correct, and it falls
over for two reasons:

1. **Every page view is a `LIST` against the API server.** Ten people watching a
   busy namespace with a five-second refresh is a sustained load that etcd, not
   the dashboard, ends up paying for.
2. **Every open tab is a `WATCH`.** Watches are cheap individually and
   expensive in aggregate; a hundred tabs is a hundred watch connections
   carrying the same data.

Orrery instead keeps one shared cache per resource per cluster, and puts
an authorization check in front of it.

## Read path

```
request ──► SubjectAccessReview (cached 30s) ──► shared informer cache ──► project ──► page
                    │                                     ▲
                    │                                     │
              API server                          one watch per resource,
           (RBAC is the authority)                  regardless of readers
```

A read never touches the API server on the hot path. What it does touch is an
access review, and those are cached, deduplicated with `singleflight`, and
batched by the UI into a single `/access` call per screen.

### Why this is safe

The informers run with the dashboard's own credentials, so the cache contains
data the requesting user may not be entitled to. The check in front of it is
therefore load-bearing, not decorative. Three properties keep it honest:

- The verdict comes from the API server's own authorizer via
  `SubjectAccessReview` — the dashboard never interprets a Role itself.
- The check happens before the cache is read, in the same function, with no
  path around it. `listResources`, `getResource` and `watchResources` each
  begin with it.
- Long-lived watches are **re-authorized every 60 seconds** and closed if
  access was revoked, so a removed RoleBinding does not leave a stream running
  until the user closes the tab. The same cycle re-validates the session —
  a sign-out or expired session closes the stream — and renews OIDC tokens
  that are about to expire, so a passthrough cluster's re-authorization never
  presents a stale credential.

RBAC has no per-object read filtering: a user who can `list` pods in a
namespace can see every pod in it. Orrery reproduces that exactly rather
than inventing a finer-grained model that would diverge from `kubectl`.

### Users without cluster-wide read

Asking "may you list pods everywhere?" answers most users in one round trip.
For the rest, the checker falls back to probing each namespace concurrently and
returns the subset that is permitted.

That scan is bounded by `authz.namespaceScanLimit`. When it truncates, the
response carries a warning and the UI displays it. Silently showing three of a
user's forty namespaces would look identical to those namespaces being empty,
which during an incident is actively dangerous.

## Cache lifecycle

A cluster with three hundred CRDs should not hold three hundred informers for
resources nobody looks at.

- Informers start **on first use**, not at boot.
- Each read stamps a last-used time. A cache idle beyond `cache.idleTimeout`
  with no WebSocket subscribers is stopped.
- A live subscriber pins its cache open regardless of read activity.
- Past `cache.maxInformersPerCluster`, the least recently used unwatched cache
  is retired to make room.
- An informer that fails to start — usually the dashboard's own RBAC missing a
  resource — is not left in place. The next request retries, so a transient
  failure during a CRD rollout heals on its own.

### What is stored

Objects are trimmed by an informer `TransformFunc` **before** they enter the
cache, because the saving is in what the cache holds, not in what a handler
copies:

- `managedFields` is dropped. On a busy cluster it is routinely a third of an
  object's bytes and is never rendered.
- `kubectl.kubernetes.io/last-applied-configuration` is dropped; it duplicates
  the whole spec as a string.
- **Secret values are replaced with their key names and sizes.** The dashboard
  therefore does not hold every credential in the cluster in its heap. Opening
  a secret refetches it live under the viewer's own identity, so the feature
  still works and the blast radius of a heap dump is much smaller.

## Write path

Writes do not go near the cache. They are issued to the API server with the
caller's identity — impersonated, or their own bearer token in passthrough
mode — so:

- Admission controllers and validating webhooks run as they normally would.
- Conflict detection via `resourceVersion` behaves normally.
- The audit log records the human, not the dashboard.

That last property is the main argument for impersonation over a shared service
account. A dashboard whose writes are all attributed to `system:serviceaccount:
orrery:orrery` makes an incident review much harder than it needs to
be.

Actions use the right mechanism rather than the easy one: scaling goes through
the `scale` subresource (so it works for any CRD that declares one), restart
stamps the same annotation `kubectl rollout restart` uses, drain goes through
the **eviction API** so PodDisruptionBudgets are respected, and an ephemeral
debug container is a `patch` of `pods/ephemeralcontainers` — the same call,
and the same permission, `kubectl debug` uses.

A write can also be asked for with `dryRun=All`, which the editor issues before
offering to apply. The API server then runs admission, mutating webhooks and
validation and returns the object it *would* have written, without writing it.
That puts the cluster's own opinion of an edit in front of the user while they
can still change it, and it is the cluster's opinion rather than a schema check
the dashboard invented and would eventually get wrong.

## Streaming

One `Broadcaster` per informer fans events out to every subscriber. A hundred
tabs on the same namespace is still one upstream watch.

The interesting case is a slow consumer. A subscriber whose buffer fills is
**dropped and told to reload** rather than being allowed to block the
publisher. Stalling the shared cache for everyone because one browser tab is
wedged would be the worse failure, and a client that has missed changes cannot
be trusted to know it — so it is told explicitly (`OVERFLOW`) instead of
quietly drifting out of date.

Log follows are the one stream that is not fed by a cache — they are held open
against the API server directly. A merged feed over a workload's pods is
therefore capped at 20, since each pod is a separate upstream stream held by
one browser tab, and **every pod is authorized before the socket opens**. A
caller who may read some of a workload's pods but not others is refused
outright rather than shown a partial feed, because a partial feed and a
complete one look identical, and during an incident the difference is the
whole answer.

The frontend applies `MODIFIED` events directly to visible rows, so a status
change is instant. `ADDED` and `DELETED` change which objects belong on the
current page, so those trigger a debounced refetch instead of being spliced in
locally — keeping pagination and sorting honest rather than approximately
right.

## Column projection

List responses ship projected rows, not objects. A page of fifty pods is a few
kilobytes instead of a megabyte.

Three sources, in order:

1. **Hand-tuned tables** for well-known kinds, including the derived values
   people actually want. Pod status is computed the way `kubectl` computes it —
   surfacing `ImagePullBackOff` from the container status rather than reporting
   the `Pending` phase, which explains nothing.
2. **The CRD's own `additionalPrinterColumns`**, compiled to JSONPath and
   evaluated against cached objects. An operator's carefully chosen columns
   appear in the dashboard for free.
3. A generic name/namespace/age fallback.

Formatting stays in the browser. Ages ship as timestamps and are rendered
relative on the client, which keeps rows cacheable and sidesteps clock skew
between the dashboard and the viewer.

## Failure behaviour

The recurring principle: degrade visibly, never silently.

| Situation | Behaviour |
| --- | --- |
| A cluster is unreachable at boot | The other clusters still start; it is listed as unreachable and retried every 60s. |
| Discovery partially fails (a broken aggregated API) | Stale discovery is served rather than blanking the navigation. |
| A resource is missing from discovery | One rate-limited refresh is forced before giving up, so a freshly installed CRD is browsable immediately. |
| metrics-server absent | `{available: false}` with a plain explanation, not a 500 that reads as a dashboard bug. |
| The user may only see some namespaces | The list is returned with an explicit scope and a banner naming them. |
| A watch falls behind | The client is told to reload rather than shown drifting data. |

## Sessions

The session cookie is `HttpOnly` and AES-GCM encrypted, carrying only an opaque
ID; tokens live server-side. The CSRF cookie is deliberately script-readable so
the SPA can echo it in `X-CSRF-Token` — double-submit, paired with
`SameSite=Lax`.

WebSocket handshakes cannot carry custom headers, so the `Origin` check in the
upgrader stands in for CSRF on streaming endpoints.

Login state — the OAuth state, nonce and PKCE verifier — is carried in a
short-lived encrypted cookie rather than server memory, so any replica can
complete a login another replica started.

That leaves the session store as the only shared state in the request path.
It is an interface with two implementations: an in-process map for single-node
and development, and Redis for anything with more than one replica. Nothing
else in the backend holds per-user state, so switching that one setting is the
whole of what horizontal scaling requires — no sticky sessions, no session
affinity at the load balancer.

## Things deliberately not done

The ones that needed a longer argument — TCP forwarding, sharing informer
caches between replicas — live in [DECISIONS.md](DECISIONS.md) with the
conditions that would change the answer.

- **No custom RBAC model.** Tempting, and it would make some screens faster.
  It would also mean the dashboard could disagree with the cluster about who
  may do what, and the dashboard would be wrong.
- **No client-side filtering of large lists.** Filtering, sorting and
  pagination happen server-side against the cache so the browser never receives
  data the user then cannot see anyway.
- **No write-through cache.** After a write, the cache is updated by the watch
  like everything else. Optimistically patching it would show state the API
  server may have rejected.
- **No cross-cluster aggregated views.** Each cluster is queried and authorized
  independently. A single "all pods everywhere" table would need a coherent
  answer for partial failure and partial permission, and the honest version of
  that is the per-cluster view this already provides.

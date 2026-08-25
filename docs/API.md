# API

The whole surface is one shape. `{group}` is `core` for the legacy group and
`{namespace}` is `_` for cluster-scoped resources, so a single route serves
every group/version/resource the cluster advertises — built-in kinds and
custom resources alike.

```
GET    /api/v1/clusters
GET    /api/v1/clusters/{c}/discovery
GET    /api/v1/clusters/{c}/overview
GET    /api/v1/clusters/{c}/resources/{group}/{version}/{resource}
GET    /api/v1/clusters/{c}/resources/{group}/{version}/{resource}/{namespace}/{name}
POST   /api/v1/clusters/{c}/resources/{group}/{version}/{resource}
PUT    /api/v1/clusters/{c}/resources/{group}/{version}/{resource}/{namespace}/{name}
PATCH  /api/v1/clusters/{c}/resources/{group}/{version}/{resource}/{namespace}/{name}
DELETE /api/v1/clusters/{c}/resources/{group}/{version}/{resource}/{namespace}/{name}
POST   /api/v1/clusters/{c}/access
POST   /api/v1/clusters/{c}/actions/{scale|restart|cordon|drain|evict}
GET    /api/v1/clusters/{c}/ws/watch/{group}/{version}/{resource}
GET    /api/v1/clusters/{c}/ws/logs
GET    /api/v1/clusters/{c}/ws/exec
GET    /api/v1/clusters/{c}/stats
```

Every read and every write is preceded by a `SubjectAccessReview` against the
cluster in question — see [How permission works](../README.md#how-permission-works)
in the README, and [ARCHITECTURE.md](ARCHITECTURE.md) for the caching design
behind it.

## Listing

List endpoints take `namespace`, `q`, `labelSelector`, `fieldSelector`,
`sort`, `order`, `page`, `pageSize` and `view=table|full`.

`q` is free text matched against name, namespace and labels (`app=web` works).
Unsupported `fieldSelector` fields are rejected with a 400 naming the
supported set, rather than silently matching nothing.

Well-known kinds get hand-tuned tables; custom resources get their own
`additionalPrinterColumns` — the same columns `kubectl get` would show.

## Watching

The watch endpoint accepts the same `q` / `labelSelector` / `fieldSelector` as
listing, and translates edits across the filter boundary into ADDED/DELETED,
so a filtered page only hears about the objects it shows.

## Facets

`GET .../resources/{g}/{v}/{r}/facets` returns the distinct label keys and
values, plus low-cardinality field values, on the objects the caller may list.
This is the vocabulary behind the search bar's autocomplete.

The UI exposes all of it as one search input: bare words are free text,
`key=value` / `key!=value` / `!key` / `key in (a,b)` are label terms, and
dotted keys like `status.phase=Running` are field terms.

## Sessions

Sessions are cookie-based. The session cookie is `HttpOnly` and encrypted, and
mutating requests carry a double-submit CSRF token in `X-CSRF-Token`.

## Observability

`/metrics` on the metrics listener exposes request rate and latency by route —
labelled by pattern, so cardinality stays bounded — plus live gauges for cache
size and subscriber counts per cluster and resource.

`GET /api/v1/clusters/{c}/stats` shows exactly which caches are running, how
many objects each holds, and how long they have been idle. It is the first
place to look when memory use surprises you.

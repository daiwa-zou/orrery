# Decisions

Things considered and deliberately not built, with the reasoning that led
there. [ARCHITECTURE.md](ARCHITECTURE.md) has a shorter list of these; this is
where the ones that needed an argument live, so that revisiting them starts
from what was already weighed rather than from scratch.

Each records the conditions under which the answer should change. None of them
are permanent.

## TCP port-forwarding

**Not built. HTTP forwarding covers the browser-reachable case; `kubectl`
covers the rest.**

[`proxy.go`](../internal/api/proxy.go) relays GET and HEAD to a pod or service
through the API server's proxy subresource, under the caller's own identity.
That covers what people usually reach for port-forward to do from a browser:
read a health page, a queue depth, an internal dashboard. Writes are excluded
on purpose — the proxied page renders inside the console's origin, and a
state-changing proxy would let any site drive writes into cluster workloads
with the viewer's session.

What it does not cover is the other half: pointing `psql` at a database or a
gRPC client at a service. That needs a real TCP tunnel, which from a browser
means a WebSocket-to-TCP relay plus a local client to terminate it — and that
client is not the browser, so the console would be shipping a second program
to make its own feature work.

It also inherits every question the read-only proxy sidesteps. A generic TCP
relay authenticated by a session cookie is a way to reach arbitrary ports on
arbitrary pods from anywhere the cookie travels, and the blast radius of a
mistake is the whole cluster network rather than one HTTP GET.

**Revisit if** the console gains a first-party desktop or CLI component. The
relay stops being a browser problem at that point, and the trust boundary
moves somewhere it can be reasoned about.

## Sharing informer caches between replicas

**Not built. Scale up rather than out until per-pod memory is the wall.**

Replicas share nothing but Redis, so *n* replicas mean *n* watches per hot
resource and *n* copies of the data. [DEPLOYMENT.md](DEPLOYMENT.md) records
this as a known limitation and recommends 2–4 replicas.

The obvious fix is the wrong shape. Reads return pointers into the informer
store and page down to fifty rows, so the input is enormous and the output is
tiny. A shared object store — Redis, or a cache tier — inverts that: every
list becomes "ship fifty thousand objects across the network and deserialize
them", which breaks the property the whole read path is built on, that a read
never touches the network. Sharing would have to route *queries to data*, not
data to queries: the pod that holds a resource runs the whole pipeline and
returns the finished page.

That is buildable. `InformerManager` is already keyed by GVR, and the read
pipeline is effectively a pure function over the object slice. What makes it
not worth doing yet:

- **The win is bounded by replica count.** At the recommended 2–4 replicas it
  is a 2–4× reduction in watch load and memory, against a large jump in
  complexity.
- **It trades steady-state efficiency for worse failure behaviour.** A pod
  leaving reassigns its resources, and the new owner refills from scratch —
  a full list, with requests for those resources stalling meanwhile. Today a
  pod dying affects only its own users; everyone else keeps hitting warm
  caches. On a large cluster, refilling a fifty-thousand-object informer is
  exactly when this hurts most.
- **Hashing by resource balances badly.** Pods and Events dominate any real
  cluster, so whichever pod owns `pods` carries the memory and the fan-out.
  Fixing that means sharding within a resource by namespace, which then
  complicates cluster-scoped lists and cross-namespace queries.

**The trap, if it is ever built:** authorization must stay on the edge pod or
travel with the query. Every read is gated by a `SubjectAccessReview` under
the user's identity, and a routing layer that trusts an upstream pod's verdict
introduces a new trust boundary in the one place this architecture is careful
not to have one.

**Revisit if** per-pod memory becomes the binding constraint and scaling up is
exhausted. The cleaner shape then is not sharding these pods but splitting
tiers: a watcher tier (stateful, sharded, scaled on memory) behind stateless
API pods (scaled on CPU) — which also dissolves the autoscaling mismatch
below, since each tier would scale on what actually binds it.

## Autoscaling on CPU

**Partly addressed. The chart can now scale on memory, and still ships with
autoscaling off.**

The HPA keyed on CPU while memory is what runs out — a symptom of the
per-replica caches above rather than a bug in the chart. The chart now
supports a memory target alongside CPU, and defaults `scaleDown` to a ten
minute stabilisation window, because a replica that goes away takes warm
caches with it and the replacement refills them from the API server on first
use. Without that window, a busy period becomes a sawtooth of cache rebuilds.

It remains **off by default**, because adding replicas does not divide the
work here: it relieves request throughput and WebSocket fan-out, and does
nothing for the memory that binds. Raising limits is still the first move.

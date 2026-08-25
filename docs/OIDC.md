# OIDC setup

Orrery signs users in with any OpenID Connect provider via the
authorization-code flow with PKCE. This document covers every knob, the claim
mapping rules (which deliberately mirror the kube-apiserver's own OIDC flags),
provider-specific walkthroughs, and the failure modes people actually hit.

If you only want the short version: give Orrery an issuer, a client ID and a
secret, register `https://<your-orrery-host>/api/v1/auth/callback` as the
redirect URI at the provider, and make sure the username/groups mapping matches
what your RBAC bindings expect. Everything else has a sensible default.

## How the flow works

1. The browser hits `/api/v1/auth/login`. Orrery generates a `state`, a
   `nonce` and a PKCE verifier, seals them into a short-lived (10 minute)
   encrypted cookie, and redirects to the provider's authorization endpoint.
   Because the in-flight login lives in the cookie rather than server memory,
   **any replica can complete a login another replica started** — no sticky
   sessions needed.
2. The provider redirects back to `/api/v1/auth/callback`. Orrery verifies
   the state, exchanges the code (with the PKCE verifier), verifies the ID
   token's signature, audience and nonce, applies the claim mapping and access
   rules below, and creates a session.
3. The session is referenced by an `HttpOnly`, AES-GCM-encrypted cookie.
   Tokens (ID, access, refresh) are stored server-side in the session store,
   never in the browser.
4. When `offlineAccess` is on (the default), Orrery requests a refresh token
   and renews tokens shortly before expiry, so a 12-hour session survives a
   provider that issues 5-minute ID tokens. Concurrent refreshes of one
   session collapse into a single provider round trip, so token-rotation
   providers (which invalidate the whole token family if a refresh token is
   replayed) are safe across concurrent requests. Long-lived streams — watches,
   log follows, terminals — renew on their 60-second re-authorization cycle
   too, and end when the session ends, so a sign-out closes them. A transient
   provider failure (5xx, throttling) never signs the user out; only a
   definitive `invalid_grant` does.

## Configuration reference

All fields live under `oidc:` in the config file. Environment variables
`ORRERY_OIDC_ISSUER`, `ORRERY_OIDC_CLIENT_ID`, `ORRERY_OIDC_CLIENT_SECRET`,
`ORRERY_OIDC_REDIRECT_URL` and `ORRERY_OIDC_ENABLED` override the file, and
`${VAR}` references inside the file are expanded — use one of those two
mechanisms for the client secret rather than writing it into the file.

| Field | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Turn the whole flow on. Off means no authentication — development only. |
| `autoLogin` | `false` | Send unauthenticated visitors straight to the provider instead of showing the sign-in button first (`ORRERY_OIDC_AUTOLOGIN` also works). Sign-out and login errors still land on the login page, so signing out does not bounce straight back in. |
| `issuer` | — (required) | The provider's issuer URL. Discovery is fetched from `<issuer>/.well-known/openid-configuration` at startup; the server will not start if it is unreachable. |
| `clientID` | — (required) | OAuth2 client ID registered at the provider. |
| `clientSecret` | — | Client secret. Prefer `ORRERY_OIDC_CLIENT_SECRET`. |
| `redirectURL` | `<publicURL>/api/v1/auth/callback` | Must byte-for-byte match a redirect URI registered at the provider. Only set it explicitly if the default is wrong for your topology. |
| `scopes` | `[openid, profile, email, groups]` | Scopes to request. `offline_access` is appended automatically when `offlineAccess` is true. Drop `groups` for providers that reject unknown scopes (e.g. Google). |
| `usernameClaim` | `email` | Which ID-token claim becomes the username. |
| `groupsClaim` | `groups` | Which claim carries group membership. Accepts an array or a single string. |
| `usernamePrefix` | *(empty)* | Prefix applied to the username. See [claim mapping](#claim-mapping-getting-identities-to-match-rbac). |
| `groupsPrefix` | `oidc:` | Prefix applied to every group. |
| `requiredClaims` | `{}` | Map of claim → exact required value. Login is refused unless every listed claim matches (e.g. `hd: example.com` to pin a Google Workspace domain). |
| `allowedGroups` | `[]` | When non-empty, only members of at least one listed group may sign in. Compared against the *unprefixed* claim values. |
| `offlineAccess` | `true` | Request a refresh token so sessions outlive short ID-token lifetimes. |
| `insecureSkipIssuerVerify` | `false` | Tolerate a discovery document whose `issuer` differs from the URL it was fetched from. Needed for some multi-tenant providers; see [Entra ID](#microsoft-entra-id-azure-ad). |
| `endSessionURL` | *(from discovery)* | RP-initiated logout endpoint. Auto-detected from the discovery document's `end_session_endpoint` when present. |

Related, outside the `oidc:` block:

- `server.publicURL` anchors the default redirect URL and must be the URL the
  browser actually uses.
- `session.secure` (default `true`) marks cookies `Secure`. Set it to `false`
  only for plain-HTTP local development — the login flow will otherwise appear
  to succeed and immediately forget the session.
- `session.ttl` / `session.idleTimeout` bound how long a login lasts.
- `session.store: redis` is required for more than one replica.

`-print-config` prints the fully resolved configuration with secrets masked —
the fastest way to confirm what the server is actually running with.

## Claim mapping: getting identities to match RBAC

This is the part that decides whether sign-in *works* and the part that decides
whether users can *see anything* — they are separate failures.

Under the default `impersonation` mode, every request to a cluster carries
`Impersonate-User: <mapped username>` and one `Impersonate-Group` per mapped
group. RBAC is evaluated against those exact strings. If your ClusterRoleBinding
names `oidc:jane@example.com` but Orrery impersonates `jane@example.com`,
Jane signs in fine and then sees an empty, permission-denied dashboard.

The mapping rules mirror `kube-apiserver --oidc-username-claim /
--oidc-username-prefix / --oidc-groups-claim / --oidc-groups-prefix` exactly:

- A **configured prefix always applies**, whatever the claim.
- A prefix of `-` explicitly disables prefixing.
- With **no prefix configured**, the default depends on the claim:
  - `usernameClaim: email` → the bare address (`jane@example.com`).
  - any other username claim → `<issuer>#<value>`
    (`https://accounts.example.com#jane`), so usernames from different issuers
    cannot collide.
  - groups default to the `oidc:` prefix in Orrery's shipped defaults.

**Recommendation:** decide the prefixes once, and configure the kube-apiserver
(or your managed equivalent — GKE/EKS/AKS OIDC config) and Orrery with the
same four values. Then an identity means the same thing to `kubectl`, to the
audit log, and to the dashboard, and one set of RBAC bindings serves all three.

Example — the shipped dev configuration maps Dex's mock user to
`oidc:kilgore@kilgore.trout` in group `oidc:authors`, so this grants read
access:

```bash
kubectl create clusterrolebinding demo-view \
  --clusterrole=view --group='oidc:authors'
```

### Groups that arrive as IDs, not names

Some providers (notably Entra ID) emit group **object IDs** rather than names
in the `groups` claim. That works — RBAC bindings just have to name the IDs —
but it is unreadable in the audit log. Prefer configuring the provider to emit
names where possible, and see the provider notes below.

## Gating who may sign in

Authentication and authorization are separate layers:

- `requiredClaims` and `allowedGroups` gate the **login** — who can establish a
  session at all. Use them to keep a company-wide IdP from letting the whole
  company into the dashboard of a sensitive fleet.
- What a signed-in user can **see and do** is decided per cluster by RBAC via
  `SubjectAccessReview` — Orrery never makes that decision itself. An empty
  dashboard after a successful login is an RBAC/claim-mapping issue, not a
  login issue.

## Provider walkthroughs

Register the redirect URI `https://<orrery-host>/api/v1/auth/callback` in
every case. Local development uses `http://localhost:5173/api/v1/auth/callback`
(the Vite dev server proxies `/api` to the backend).

### Dex (local development)

The repo ships a working setup — see [Quick start](../README.md#quick-start)
in the README, `deploy/dev/dex.yaml` and `configs/orrery.oidc.yaml`. Dex is also a
good production choice as a *federating* layer when your upstream is LDAP,
SAML or GitHub: point Orrery (and your API servers) at Dex, and Dex at the
upstream.

### Keycloak

1. Create a client: **Clients → Create**, type *OpenID Connect*, client ID
   `orrery`, *Client authentication* on (confidential), *Standard flow* on.
2. Set the redirect URI. Copy the secret from the *Credentials* tab.
3. Keycloak does not emit a `groups` claim by default. Add a mapper:
   **Client scopes → dedicated scope → Add mapper → Group Membership**, token
   claim name `groups`, *Full group path* **off** (otherwise every group
   arrives as `/team-a` and your prefixed group is `oidc:/team-a`).

```yaml
oidc:
  enabled: true
  issuer: https://keycloak.example.com/realms/main
  clientID: orrery
  # clientSecret via ORRERY_OIDC_CLIENT_SECRET
  scopes: [openid, profile, email]   # groups comes from the mapper, not a scope
  usernameClaim: email
  groupsClaim: groups
  usernamePrefix: 'oidc:'
  groupsPrefix: 'oidc:'
```

### Microsoft Entra ID (Azure AD)

1. **App registrations → New registration**, redirect URI type *Web*.
2. **Certificates & secrets** → new client secret.
3. **Token configuration → Add groups claim** to get group membership into the
   ID token. Entra emits group **object IDs**; either bind RBAC to those IDs,
   or (for Entra-joined setups) enable *sAMAccountName* emission, or federate
   through Dex to translate.
4. Users with many groups get a *groups overage* claim instead of the list —
   the token contains a link rather than groups, and Orrery will see no
   groups. Keep the app's group emission filtered (e.g. *Groups assigned to
   the application*) to stay under the limit.

```yaml
oidc:
  enabled: true
  issuer: https://login.microsoftonline.com/<tenant-id>/v2.0
  clientID: <application-client-id>
  scopes: [openid, profile, email]
  usernameClaim: email          # or preferred_username / upn
  groupsClaim: groups
  usernamePrefix: 'oidc:'
  groupsPrefix: 'oidc:'
```

Use your tenant ID in the issuer, not `common`/`organizations` — the
multi-tenant endpoints advertise a templated issuer that fails verification
(that is what `insecureSkipIssuerVerify` is for, but pinning the tenant is the
better fix).

### Okta

1. **Applications → Create App Integration → OIDC → Web Application**.
2. Groups: **Sign On → OpenID Connect ID Token → Groups claim filter**, claim
   name `groups`, filter *Matches regex* `.*` (or a narrower filter).
3. If you use a custom authorization server, the issuer is
   `https://<org>.okta.com/oauth2/<server-id>`; the org authorization server is
   `https://<org>.okta.com`.

```yaml
oidc:
  enabled: true
  issuer: https://acme.okta.com
  clientID: <client-id>
  scopes: [openid, profile, email, groups]
  usernameClaim: email
  groupsClaim: groups
  usernamePrefix: 'oidc:'
  groupsPrefix: 'oidc:'
```

### Google

Google's OIDC has no groups claim and rejects a `groups` scope request at
consent time on some flows; bind RBAC per user, or federate through Dex with
its Google connector (which can fetch Workspace group membership via the
Directory API).

```yaml
oidc:
  enabled: true
  issuer: https://accounts.google.com
  clientID: <client-id>.apps.googleusercontent.com
  scopes: [openid, profile, email]
  usernameClaim: email
  groupsClaim: ''               # Google emits none
  usernamePrefix: 'oidc:'
  requiredClaims:
    hd: example.com             # pin the Workspace domain
```

## Matching the kube-apiserver (impersonation and passthrough)

For `impersonation` mode nothing at the API server changes — the dashboard's
service account needs the `impersonate` verb (the Helm chart's ClusterRole
grants it) and RBAC bindings must name the mapped identities.

For `passthrough` mode the user's own ID token is sent as the bearer token, so
each cluster's API server must trust the issuer itself:

```
--oidc-issuer-url=https://keycloak.example.com/realms/main
--oidc-client-id=orrery
--oidc-username-claim=email
--oidc-username-prefix=oidc:
--oidc-groups-claim=groups
--oidc-groups-prefix=oidc:
```

Keep these four mapping flags identical to Orrery's `usernameClaim` /
`usernamePrefix` / `groupsClaim` / `groupsPrefix`, and note that passthrough
requires `offlineAccess` in practice: the ID token must stay fresh for as long
as the user keeps the dashboard open.

Getting this wrong is quiet rather than loud: health is probed with the
dashboard's own credential, so a cluster whose audience or prefixes disagree
still shows **healthy** in the fleet view while every user sees permission
denied on it. [`deploy/remote-cluster/preflight.sh`](../deploy/remote-cluster/preflight.sh)
takes an ID token, confirms the API server accepts it, and prints the username
and groups that server derives — run it against each cluster before
registering it. The reduced grants a passthrough cluster needs are in
[`rbac-passthrough.yaml`](../deploy/remote-cluster/rbac-passthrough.yaml)
alongside it.

## Troubleshooting

**`oidc discovery for <issuer>: ...` at startup.** The issuer URL is wrong,
unreachable from the pod, or serves a certificate the pod does not trust. The
issuer in the discovery document must equal the configured issuer — if the
provider genuinely advertises a different one, that is what
`insecureSkipIssuerVerify` is for.

**Provider error: redirect URI mismatch.** The registered URI must match
`redirectURL` exactly, including scheme and trailing parts. Remember the
default is derived from `publicURL`.

**`login state missing or expired`.** The state cookie did not survive the
round trip: more than 10 minutes passed, cookies are blocked, or —
the common case — `session.secure: true` while serving plain HTTP, so the
browser refuses the cookie. For local HTTP development set
`session.secure: false`.

**Login succeeds, dashboard is empty / everything is permission-denied.**
Claim mapping does not match your RBAC bindings. Check the resolved identity:
the audit log (or `kubectl` API server logs) shows the exact
`Impersonate-User` string; compare against your bindings, including prefixes.
Nine times out of ten it is a missing or doubled `oidc:` prefix.

**No groups.** The provider is not emitting the claim (Keycloak needs a
mapper, Okta a claim filter, Entra a token-configuration change, Google never
does), the scope was not granted, or an Entra groups-overage kicked in. Decode
the ID token from the provider's own token tester to see what is actually in
it.

**Signed out whenever the pod restarts, or randomly across requests.** The
session encryption key is being generated at boot (the log warns about this) —
set `ORRERY_SESSION_KEY`, and use `session.store: redis` for more than one
replica. See [DEPLOYMENT.md](DEPLOYMENT.md).

**Sessions end after ~5 minutes.** The provider issues short ID tokens and no
refresh token was granted. Keep `offlineAccess: true` and check the provider
consented to `offline_access` (some require enabling refresh tokens per
client).

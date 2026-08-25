#!/usr/bin/env bash
#
# Verify one remote cluster is ready to be registered with an Orrery hub.
#
# Run this after applying rbac-passthrough.yaml or rbac-impersonation.yaml and
# before adding the cluster to the hub's config. It checks the two things that
# actually go wrong: the hub's service account not having the grants its auth
# mode needs, and — for passthrough — the API server not accepting the ID
# tokens your identity provider issues.
#
# That second check is the one worth running. A cluster whose OIDC audience or
# claim prefixes disagree with the hub still probes healthy in the fleet view,
# because health is measured with the hub's own credential; the mismatch only
# shows up as every user seeing permission denied. This finds it first.
#
#   ./preflight.sh --context prod-eu --mode passthrough \
#       --token-file /tmp/id_token --username-prefix 'oidc:'
#
# Requires kubectl with cluster-admin on the target (the service-account checks
# use impersonation to ask what that account may do).
set -uo pipefail

CONTEXT=""
MODE="passthrough"
NAMESPACE="orrery"
SA="orrery-remote"
TOKEN=""
TOKEN_FILE=""
USERNAME_PREFIX="oidc:"
GROUPS_PREFIX="oidc:"

usage() {
  sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'
  cat <<'EOF'

Options:
  --context CTX          kubectl context for the remote cluster (required)
  --mode MODE            passthrough | impersonation   (default: passthrough)
  --namespace NS         service account namespace     (default: orrery)
  --serviceaccount SA    service account name          (default: orrery-remote)
  --token-file FILE      file holding a user's OIDC ID token (passthrough)
  --id-token TOKEN       the ID token inline; prefer --token-file
  --username-prefix P    expected username prefix      (default: oidc:)
  --groups-prefix P      expected groups prefix        (default: oidc:)
  -h, --help             this text
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --context) CONTEXT="$2"; shift 2 ;;
    --mode) MODE="$2"; shift 2 ;;
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --serviceaccount) SA="$2"; shift 2 ;;
    --token-file) TOKEN_FILE="$2"; shift 2 ;;
    --id-token) TOKEN="$2"; shift 2 ;;
    --username-prefix) USERNAME_PREFIX="$2"; shift 2 ;;
    --groups-prefix) GROUPS_PREFIX="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [ -z "$CONTEXT" ]; then
  echo "--context is required" >&2
  usage >&2
  exit 2
fi
case "$MODE" in
  passthrough|impersonation) ;;
  *) echo "--mode must be passthrough or impersonation" >&2; exit 2 ;;
esac
if [ -n "$TOKEN_FILE" ]; then
  if [ ! -r "$TOKEN_FILE" ]; then
    echo "cannot read --token-file $TOKEN_FILE" >&2
    exit 2
  fi
  TOKEN="$(tr -d '\r\n' < "$TOKEN_FILE")"
fi

if [ -t 1 ]; then
  OK=$'\033[32m  ok\033[0m'; BAD=$'\033[31mFAIL\033[0m'; WARN=$'\033[33mwarn\033[0m'; DIM=$'\033[2m'; OFF=$'\033[0m'
else
  OK="  ok"; BAD="FAIL"; WARN="warn"; DIM=""; OFF=""
fi

FAILURES=0
pass() { printf '[%s] %s\n' "$OK" "$1"; }
fail() { printf '[%s] %s\n' "$BAD" "$1"; FAILURES=$((FAILURES + 1)); }
warn() { printf '[%s] %s\n' "$WARN" "$1"; }
note() { printf '       %s%s%s\n' "$DIM" "$1" "$OFF"; }

k() { kubectl --context "$CONTEXT" "$@"; }

SUBJECT="system:serviceaccount:${NAMESPACE}:${SA}"

echo "Orrery remote-cluster preflight"
echo "  context: $CONTEXT"
echo "  mode:    $MODE"
echo "  subject: $SUBJECT"
echo

# ---------------------------------------------------------------- reachability
# /version and /readyz are exactly what the hub's health probe reads to decide
# whether a cluster shows healthy, degraded or unreachable in the fleet view.
if raw_version=$(k get --raw /version 2>/dev/null); then
  server=$(printf '%s' "$raw_version" | tr -d '\n ' | sed -n 's/.*"gitVersion":"\([^"]*\)".*/\1/p')
  pass "API server reachable${server:+ ($server)}"
else
  fail "cannot reach the API server for context $CONTEXT"
  echo
  echo "$FAILURES check(s) failed."
  exit 1
fi

if readyz=$(k get --raw /readyz 2>/dev/null); then
  if [ "$readyz" = "ok" ]; then
    pass "/readyz is ok (cluster will show healthy, not degraded)"
  else
    warn "/readyz returned '$readyz' — the fleet view will show this cluster degraded"
  fi
else
  warn "cannot read /readyz — the fleet view will show this cluster degraded"
fi

# ------------------------------------------------------------- service account
if k get serviceaccount "$SA" -n "$NAMESPACE" >/dev/null 2>&1; then
  pass "service account $SUBJECT exists"
else
  fail "service account $SUBJECT not found — apply rbac-${MODE}.yaml first"
fi

# A token Secret is what ends up in the hub's kubeconfig; an empty one means
# the controller has not populated it yet.
if secret_data=$(k get secret "${SA}-token" -n "$NAMESPACE" -o jsonpath='{.data.token}' 2>/dev/null); then
  if [ -n "$secret_data" ]; then
    pass "token secret ${SA}-token is populated"
  else
    fail "token secret ${SA}-token exists but is empty"
  fi
else
  warn "no secret ${SA}-token — fine if you mint credentials another way"
fi

# ------------------------------------------------- what the hub's account can do
can() { # can <verb> <resource> <description>
  if k auth can-i "$1" "$2" --all-namespaces --as="$SUBJECT" >/dev/null 2>&1; then
    pass "$3"
  else
    fail "$3"
  fi
}
cannot() { # cannot <verb> <resource> <description-when-absent>
  if k auth can-i "$1" "$2" --all-namespaces --as="$SUBJECT" >/dev/null 2>&1; then
    return 1
  fi
  return 0
}

can list pods "can list pods cluster-wide (fills the shared cache)"
can watch pods "can watch pods cluster-wide (keeps lists live)"
can list customresourcedefinitions "can list CRDs (builds the navigation)"

if [ "$MODE" = impersonation ]; then
  can create subjectaccessreviews "can create SubjectAccessReviews (per-user authorization)"
  if k auth can-i impersonate users --as="$SUBJECT" >/dev/null 2>&1; then
    pass "can impersonate users (acts as the signed-in user)"
  else
    fail "cannot impersonate users — writes, exec and logs will fail"
  fi
  if k auth can-i impersonate groups --as="$SUBJECT" >/dev/null 2>&1; then
    pass "can impersonate groups (carries group-derived RBAC)"
  else
    fail "cannot impersonate groups — group-based RBAC will not apply"
  fi
else
  # Passthrough deliberately withholds both. Finding them here means the
  # cluster is carrying impersonation's blast radius without using it.
  if cannot impersonate users "" ; then
    pass "cannot impersonate users (correct for passthrough)"
  else
    warn "can impersonate users — more privilege than passthrough needs"
    note "apply rbac-passthrough.yaml, or switch this cluster to impersonation"
  fi
  if cannot create subjectaccessreviews ""; then
    pass "cannot create SubjectAccessReviews (correct for passthrough)"
  else
    warn "can create SubjectAccessReviews — unused in passthrough mode"
  fi
fi

# ------------------------------------------------------ does the ID token work
if [ "$MODE" = passthrough ] && [ -z "$TOKEN" ]; then
  echo
  warn "no --token-file/--id-token given; skipping the OIDC checks"
  note "these are the checks that catch an audience or claim-prefix mismatch"
  note "grab a token from the hub: sign in, then read the id_token from your session"
elif [ -n "$TOKEN" ]; then
  echo
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  kc="$tmp/kubeconfig"

  # Minify to this context, then replace the credential outright so a client
  # certificate in the original entry cannot answer for the token under test.
  if ! k config view --raw --minify --flatten > "$kc" 2>/dev/null; then
    fail "could not extract kubeconfig for context $CONTEXT"
  else
    chmod 600 "$kc"
    KUBECONFIG="$kc" kubectl config set-credentials orrery-preflight --token="$TOKEN" >/dev/null 2>&1
    KUBECONFIG="$kc" kubectl config set-context --current --user=orrery-preflight >/dev/null 2>&1

    if whoami_json=$(KUBECONFIG="$kc" kubectl auth whoami -o json 2>"$tmp/err"); then
      # kubectl pretty-prints, so every pattern has to tolerate whitespace
      # around the punctuation — while leaving spaces *inside* a name alone,
      # because plenty of providers emit groups like "Domain Admins".
      flat=$(printf '%s' "$whoami_json" | tr '\n' ' ')
      user=$(printf '%s' "$flat" |
        sed -n 's/.*"username"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
      groups=$(printf '%s' "$flat" |
        sed -n 's/.*"groups"[[:space:]]*:[[:space:]]*\[\([^]]*\)\].*/\1/p' |
        sed 's/"[[:space:]]*,[[:space:]]*"/,/g; s/^[[:space:]]*"//; s/"[[:space:]]*$//')
      pass "the API server accepted the ID token"
      note "username: ${user:-<none>}"
      note "groups:   ${groups:-<none>}"

      # The identity the API server derives is what RBAC evaluates. If its
      # prefix differs from the hub's, bindings written for one will not match
      # the other, and the UI will name a user the cluster has never heard of.
      case "$user" in
        "$USERNAME_PREFIX"*)
          pass "username carries the expected prefix '$USERNAME_PREFIX'" ;;
        "")
          fail "the API server returned no username" ;;
        *)
          fail "username '$user' does not start with '$USERNAME_PREFIX'"
          note "align --oidc-username-prefix here with oidc.usernamePrefix on the hub" ;;
      esac

      if [ -n "$groups" ]; then
        case ",$groups," in
          *",${GROUPS_PREFIX}"*)
            pass "groups carry the expected prefix '$GROUPS_PREFIX'" ;;
          *)
            warn "no group starts with '$GROUPS_PREFIX'"
            note "check --oidc-groups-claim/--oidc-groups-prefix against the hub" ;;
        esac
      else
        warn "the token produced no groups"
        note "group-based RBAC will not apply; check --oidc-groups-claim"
      fi

      # Passthrough authorization is a SelfSubjectAccessReview made with this
      # token, so confirm the identity may create one at all.
      if KUBECONFIG="$kc" kubectl auth can-i get pods --all-namespaces >/dev/null 2>&1; then
        pass "this user may read pods somewhere in the cluster"
      else
        warn "this user may not read pods anywhere"
        note "expected if you have not bound them yet; bind RBAC to '${user}'"
      fi
    else
      err=$(tr -d '\n' < "$tmp/err" | sed 's/  */ /g')
      fail "the API server rejected the ID token"
      note "${err:-no detail returned}"
      case "$err" in
        *Unauthorized*|*"provide credentials"*)
          note "usual causes: --oidc-client-id does not match the token's aud claim,"
          note "--oidc-issuer-url differs from the hub's issuer, or clock skew" ;;
        *"unknown command"*|*"unknown flag"*)
          note "kubectl auth whoami needs kubectl 1.28+; upgrade to run this check" ;;
      esac
    fi
  fi
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "Ready to register. Add it to the hub's clusters: list with authMode: $MODE."
  exit 0
fi
echo "$FAILURES check(s) failed; fix these before registering the cluster."
exit 1

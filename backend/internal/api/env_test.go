package api

import (
	"net/http"
	"testing"
)

// envSeed plants a pod exercising every env source, plus the ConfigMap and
// Secret it references, directly into the fake's stores.
func envSeed(t *testing.T, rig *hndRig) {
	t.Helper()
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()

	cms := rig.fake.resources[hndKey("", "v1", "configmaps")]
	cms.items = append(cms.items, hndObj("", "v1", "ConfigMap", "demo", "app-config", map[string]any{
		"data": map[string]any{"LOG_LEVEL": "debug", "REGION": "eu-west-1"},
	}))
	secrets := rig.fake.resources[hndKey("", "v1", "secrets")]
	secrets.items = append(secrets.items, hndObj("", "v1", "Secret", "demo", "app-creds", map[string]any{
		// Secret data is base64 on the wire; the typed client decodes it.
		"data": map[string]any{"DB_PASSWORD": "aHVudGVyMg==", "API_KEY": "azEyMw=="},
	}))

	pods := rig.fake.resources[hndKey("", "v1", "pods")]
	pods.items = append(pods.items, hndObj("", "v1", "Pod", "demo", "envy", map[string]any{
		"labels": map[string]any{"team": "core"},
		"spec": map[string]any{
			"nodeName":           "node-1",
			"serviceAccountName": "runner",
			"initContainers": []any{map[string]any{
				"name":  "init",
				"image": "init:1",
				"env": []any{
					map[string]any{"name": "MODE", "value": "setup"},
				},
			}},
			"containers": []any{map[string]any{
				"name":  "app",
				"image": "app:1",
				"resources": map[string]any{
					"limits":   map[string]any{"cpu": "500m", "memory": "256Mi"},
					"requests": map[string]any{"cpu": "250m"},
				},
				"envFrom": []any{
					map[string]any{"configMapRef": map[string]any{"name": "app-config"}, "prefix": "CFG_"},
					// Optional and absent: contributes nothing, not an error.
					map[string]any{"configMapRef": map[string]any{"name": "gone", "optional": true}},
				},
				"env": []any{
					map[string]any{"name": "PLAIN", "value": "yes"},
					map[string]any{"name": "PASS", "valueFrom": map[string]any{
						"secretKeyRef": map[string]any{"name": "app-creds", "key": "DB_PASSWORD"},
					}},
					map[string]any{"name": "MISSING_KEY", "valueFrom": map[string]any{
						"configMapKeyRef": map[string]any{"name": "app-config", "key": "ABSENT"},
					}},
					map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{
						"fieldRef": map[string]any{"fieldPath": "metadata.name"},
					}},
					map[string]any{"name": "TEAM", "valueFrom": map[string]any{
						"fieldRef": map[string]any{"fieldPath": "metadata.labels['team']"},
					}},
					map[string]any{"name": "CPU_MILLIS", "valueFrom": map[string]any{
						"resourceFieldRef": map[string]any{"resource": "limits.cpu", "divisor": "1m"},
					}},
					map[string]any{"name": "MEM_BYTES", "valueFrom": map[string]any{
						"resourceFieldRef": map[string]any{"resource": "limits.memory"},
					}},
					// env overrides an envFrom-provided name — kubelet precedence.
					map[string]any{"name": "CFG_REGION", "value": "overridden"},
				},
			}},
		},
		"status": map[string]any{"phase": "Running", "podIP": "10.1.2.3"},
	}))
}

type envResponse struct {
	Containers []struct {
		Name string `json:"name"`
		Init bool   `json:"init"`
		Env  []struct {
			Name      string `json:"name"`
			Value     string `json:"value"`
			Source    string `json:"source"`
			From      string `json:"from"`
			Sensitive bool   `json:"sensitive"`
			Error     string `json:"error"`
		} `json:"env"`
	} `json:"containers"`
}

func TestPodEnvResolvesAllSourcesHTTP(t *testing.T) {
	rig := hndNewRig(t)
	envSeed(t, rig)

	rec := rig.get(t, "/api/v1/clusters/fake/pods/demo/envy/env")
	hndWantStatus(t, rec, http.StatusOK)
	var body envResponse
	hndDecode(t, rec, &body)

	if len(body.Containers) != 2 {
		t.Fatalf("containers = %d, want init + app", len(body.Containers))
	}
	if !body.Containers[0].Init || body.Containers[0].Name != "init" {
		t.Errorf("first container should be the init container, got %+v", body.Containers[0])
	}

	app := body.Containers[1]
	got := map[string]struct {
		value, source, from, errMsg string
		sensitive                   bool
	}{}
	for _, e := range app.Env {
		got[e.Name] = struct {
			value, source, from, errMsg string
			sensitive                   bool
		}{e.Value, e.Source, e.From, e.Error, e.Sensitive}
	}

	check := func(name, value, source string, sensitive bool) {
		t.Helper()
		e, ok := got[name]
		if !ok {
			t.Errorf("missing env %s", name)
			return
		}
		if e.value != value || e.source != source || e.sensitive != sensitive {
			t.Errorf("%s = %+v, want value %q source %q sensitive %v", name, e, value, source, sensitive)
		}
	}

	check("PLAIN", "yes", "literal", false)
	check("CFG_LOG_LEVEL", "debug", "configMap", false)
	check("PASS", "hunter2", "secret", true)
	check("POD_NAME", "envy", "field", false)
	check("TEAM", "core", "field", false)
	check("CPU_MILLIS", "500", "resource", false)
	// 256Mi / divisor 1 = the full byte count.
	check("MEM_BYTES", "268435456", "resource", false)
	// The env entry wins over the envFrom-provided value of the same name.
	check("CFG_REGION", "overridden", "literal", false)
	if got["CFG_REGION"].value == "eu-west-1" {
		t.Error("envFrom value survived an env override")
	}

	if e := got["MISSING_KEY"]; e.errMsg == "" || e.value != "" {
		t.Errorf("MISSING_KEY should carry an error and no value, got %+v", e)
	}

	// The override keeps the row where envFrom first defined it, but the row
	// must reflect the env entry. Source of the override entry: literal.
	for _, e := range app.Env {
		if e.Name == "CFG_REGION" && e.Source != "literal" {
			t.Errorf("CFG_REGION source = %q, want the overriding literal", e.Source)
		}
	}
}

func TestPodEnvForbiddenSecretHTTP(t *testing.T) {
	rig := hndNewRig(t)
	envSeed(t, rig)

	rig.fake.mu.Lock()
	rig.fake.denyResource = "secrets"
	rig.fake.mu.Unlock()

	rec := rig.get(t, "/api/v1/clusters/fake/pods/demo/envy/env")
	hndWantStatus(t, rec, http.StatusOK)
	var body envResponse
	hndDecode(t, rec, &body)

	app := body.Containers[1]
	found := false
	for _, e := range app.Env {
		if e.Name != "PASS" {
			continue
		}
		found = true
		// Being allowed to read the pod must not leak the secret's value.
		if e.Value != "" || e.Error == "" {
			t.Errorf("PASS = %+v, want empty value with a forbidden error", e)
		}
	}
	if !found {
		t.Error("PASS row missing from a forbidden resolution")
	}
}

func TestPodEnvForbiddenPodHTTP(t *testing.T) {
	rig := hndNewRig(t)
	envSeed(t, rig)

	rig.fake.mu.Lock()
	rig.fake.denyResource = "pods"
	rig.fake.mu.Unlock()

	rec := rig.get(t, "/api/v1/clusters/fake/pods/demo/envy/env")
	hndWantStatus(t, rec, http.StatusForbidden)
}

func TestPodEnvUnknownPodHTTP(t *testing.T) {
	rig := hndNewRig(t)
	rec := rig.get(t, "/api/v1/clusters/fake/pods/demo/absent/env")
	hndWantStatus(t, rec, http.StatusNotFound)
}

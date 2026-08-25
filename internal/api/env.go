package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// envVar is one resolved environment variable as the pod detail page shows it.
// Value is the resolved value when it could be resolved; Err explains why it
// could not (forbidden reference, missing key). Sensitive marks values that
// came out of a Secret so the frontend can mask them until asked.
type envVar struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Source    string `json:"source"` // literal | configMap | secret | field | resource
	From      string `json:"from,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
	Err       string `json:"error,omitempty"`
}

type containerEnv struct {
	Name string   `json:"name"`
	Init bool     `json:"init,omitempty"`
	Env  []envVar `json:"env"`
}

// podEnv resolves each container's environment the way the kubelet would:
// envFrom sources first, in order, then env entries, later names overriding
// earlier ones. References are read with the caller's own clients after an
// access review, so a Secret the user may not get resolves to an error entry
// rather than a value — the dashboard's own broad read access is never used
// here.
func (a *API) podEnv(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")

	podRes, err := res.cluster.Discovery.Resolve(ctx, "", "v1", "pods")
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	res.resource = podRes
	if err := a.authorize(ctx, res, "get", namespace, name, ""); err != nil {
		a.writeErr(w, r, err)
		return
	}

	pod, err := res.clients.Kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	rs := &refResolver{api: a, res: res, ctx: ctx, namespace: namespace}
	out := make([]containerEnv, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, c := range pod.Spec.InitContainers {
		out = append(out, containerEnv{Name: c.Name, Init: true, Env: rs.resolveContainer(pod, c)})
	}
	for _, c := range pod.Spec.Containers {
		out = append(out, containerEnv{Name: c.Name, Env: rs.resolveContainer(pod, c)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
}

// refResolver memoises ConfigMap/Secret lookups (and their authorization
// verdicts) so a pod whose ten variables read one Secret costs one review and
// one get, not ten.
type refResolver struct {
	api       *API
	res       *resolved
	ctx       context.Context
	namespace string

	configMaps map[string]refLookup[*corev1.ConfigMap]
	secrets    map[string]refLookup[*corev1.Secret]
}

type refLookup[T any] struct {
	obj T
	err error
}

func (rs *refResolver) configMap(name string) (*corev1.ConfigMap, error) {
	if rs.configMaps == nil {
		rs.configMaps = map[string]refLookup[*corev1.ConfigMap]{}
	}
	if l, ok := rs.configMaps[name]; ok {
		return l.obj, l.err
	}
	cm, err := refGet(rs, "configmaps", name, func() (*corev1.ConfigMap, error) {
		return rs.res.clients.Kube.CoreV1().ConfigMaps(rs.namespace).Get(rs.ctx, name, metav1.GetOptions{})
	})
	rs.configMaps[name] = refLookup[*corev1.ConfigMap]{obj: cm, err: err}
	return cm, err
}

func (rs *refResolver) secret(name string) (*corev1.Secret, error) {
	if rs.secrets == nil {
		rs.secrets = map[string]refLookup[*corev1.Secret]{}
	}
	if l, ok := rs.secrets[name]; ok {
		return l.obj, l.err
	}
	s, err := refGet(rs, "secrets", name, func() (*corev1.Secret, error) {
		return rs.res.clients.Kube.CoreV1().Secrets(rs.namespace).Get(rs.ctx, name, metav1.GetOptions{})
	})
	rs.secrets[name] = refLookup[*corev1.Secret]{obj: s, err: err}
	return s, err
}

// refGet authorizes and fetches one referenced object. The access review runs
// against the referenced resource itself — being allowed to read a pod says
// nothing about the Secrets its spec points at.
func refGet[T any](rs *refResolver, plural, name string, get func() (T, error)) (T, error) {
	var zero T
	ar, err := rs.res.cluster.Discovery.Resolve(rs.ctx, "", "v1", plural)
	if err != nil {
		return zero, err
	}
	saved := rs.res.resource
	rs.res.resource = ar
	err = rs.api.authorize(rs.ctx, rs.res, "get", rs.namespace, name, "")
	rs.res.resource = saved
	if err != nil {
		return zero, err
	}
	return get()
}

// refErr compresses a lookup failure into the short form an env row shows.
func refErr(kind, name string, err error) string {
	var f *forbiddenError
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Sprintf("%s %q not found", kind, name)
	case errors.As(err, &f) || apierrors.IsForbidden(err):
		return fmt.Sprintf("%s %q is forbidden", kind, name)
	default:
		return fmt.Sprintf("%s %q: %s", kind, name, err)
	}
}

func (rs *refResolver) resolveContainer(pod *corev1.Pod, c corev1.Container) []envVar {
	// Order and precedence match the kubelet: envFrom first, then env, with a
	// later definition of the same name replacing the earlier one in place.
	byName := map[string]int{}
	var out []envVar
	put := func(v envVar) {
		if i, ok := byName[v.Name]; ok {
			out[i] = v
			return
		}
		byName[v.Name] = len(out)
		out = append(out, v)
	}

	for _, src := range c.EnvFrom {
		switch {
		case src.ConfigMapRef != nil:
			name := src.ConfigMapRef.Name
			cm, err := rs.configMap(name)
			if err != nil {
				if src.ConfigMapRef.Optional != nil && *src.ConfigMapRef.Optional && apierrors.IsNotFound(err) {
					continue // an optional source that is absent contributes nothing
				}
				put(envVar{Name: src.Prefix + "…", Source: "configMap", From: name, Err: refErr("configmap", name, err)})
				continue
			}
			for _, k := range sortedKeys(cm.Data) {
				put(envVar{Name: src.Prefix + k, Value: cm.Data[k], Source: "configMap", From: name + "/" + k})
			}
		case src.SecretRef != nil:
			name := src.SecretRef.Name
			sec, err := rs.secret(name)
			if err != nil {
				if src.SecretRef.Optional != nil && *src.SecretRef.Optional && apierrors.IsNotFound(err) {
					continue
				}
				put(envVar{Name: src.Prefix + "…", Source: "secret", From: name, Err: refErr("secret", name, err)})
				continue
			}
			for _, k := range sortedKeys(sec.Data) {
				put(envVar{Name: src.Prefix + k, Value: string(sec.Data[k]), Source: "secret", From: name + "/" + k, Sensitive: true})
			}
		}
	}

	for _, e := range c.Env {
		put(rs.resolveOne(pod, c, e))
	}
	return out
}

func (rs *refResolver) resolveOne(pod *corev1.Pod, c corev1.Container, e corev1.EnvVar) envVar {
	if e.ValueFrom == nil {
		return envVar{Name: e.Name, Value: e.Value, Source: "literal"}
	}
	vf := e.ValueFrom
	switch {
	case vf.ConfigMapKeyRef != nil:
		ref := vf.ConfigMapKeyRef
		v := envVar{Name: e.Name, Source: "configMap", From: ref.Name + "/" + ref.Key}
		cm, err := rs.configMap(ref.Name)
		if err != nil {
			v.Err = refErr("configmap", ref.Name, err)
			return v
		}
		val, ok := cm.Data[ref.Key]
		if !ok {
			v.Err = fmt.Sprintf("key %q not in configmap %q", ref.Key, ref.Name)
			return v
		}
		v.Value = val
		return v

	case vf.SecretKeyRef != nil:
		ref := vf.SecretKeyRef
		v := envVar{Name: e.Name, Source: "secret", From: ref.Name + "/" + ref.Key, Sensitive: true}
		sec, err := rs.secret(ref.Name)
		if err != nil {
			v.Err = refErr("secret", ref.Name, err)
			return v
		}
		val, ok := sec.Data[ref.Key]
		if !ok {
			v.Err = fmt.Sprintf("key %q not in secret %q", ref.Key, ref.Name)
			return v
		}
		v.Value = string(val)
		return v

	case vf.FieldRef != nil:
		path := vf.FieldRef.FieldPath
		v := envVar{Name: e.Name, Source: "field", From: path}
		val, err := podFieldValue(pod, path)
		if err != nil {
			v.Err = err.Error()
			return v
		}
		v.Value = val
		return v

	case vf.ResourceFieldRef != nil:
		ref := vf.ResourceFieldRef
		v := envVar{Name: e.Name, Source: "resource", From: ref.Resource}
		val, err := containerResourceValue(c, ref)
		if err != nil {
			v.Err = err.Error()
			return v
		}
		v.Value = val
		return v
	}
	return envVar{Name: e.Name, Source: "literal", Err: "unrecognised valueFrom"}
}

// podFieldValue mirrors the downward-API field paths the kubelet accepts.
func podFieldValue(pod *corev1.Pod, path string) (string, error) {
	if key, ok := subscript(path, "metadata.labels"); ok {
		return pod.Labels[key], nil
	}
	if key, ok := subscript(path, "metadata.annotations"); ok {
		return pod.Annotations[key], nil
	}
	switch path {
	case "metadata.name":
		return pod.Name, nil
	case "metadata.namespace":
		return pod.Namespace, nil
	case "metadata.uid":
		return string(pod.UID), nil
	case "spec.nodeName":
		return pod.Spec.NodeName, nil
	case "spec.serviceAccountName":
		return pod.Spec.ServiceAccountName, nil
	case "status.hostIP":
		return pod.Status.HostIP, nil
	case "status.podIP":
		return pod.Status.PodIP, nil
	case "status.podIPs":
		ips := make([]string, 0, len(pod.Status.PodIPs))
		for _, ip := range pod.Status.PodIPs {
			ips = append(ips, ip.IP)
		}
		return strings.Join(ips, ","), nil
	}
	return "", fmt.Errorf("unsupported fieldRef %q", path)
}

// subscript extracts k from `prefix['k']`.
func subscript(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix+"['") || !strings.HasSuffix(path, "']") {
		return "", false
	}
	return path[len(prefix)+2 : len(path)-2], true
}

// containerResourceValue resolves limits.cpu / requests.memory style refs.
// When the container declares no limit the kubelet substitutes the node's
// allocatable; from here the node is not in hand, so that case reports the
// omission instead of guessing.
func containerResourceValue(c corev1.Container, ref *corev1.ResourceFieldSelector) (string, error) {
	kind, resName, ok := strings.Cut(ref.Resource, ".")
	if !ok {
		return "", fmt.Errorf("unsupported resourceFieldRef %q", ref.Resource)
	}
	var list corev1.ResourceList
	switch kind {
	case "limits":
		list = c.Resources.Limits
	case "requests":
		list = c.Resources.Requests
	default:
		return "", fmt.Errorf("unsupported resourceFieldRef %q", ref.Resource)
	}
	q, ok := list[corev1.ResourceName(resName)]
	if !ok {
		return "", fmt.Errorf("container declares no %s", ref.Resource)
	}

	divisor := ref.Divisor
	if divisor.IsZero() {
		divisor = resource.MustParse("1")
	}
	// The kubelet divides and rounds up. Milli-precision is only meaningful
	// for CPU quantities with a milli divisor; everything else is whole units.
	if divisor.Cmp(resource.MustParse("1m")) == 0 {
		return fmt.Sprintf("%d", q.MilliValue()), nil
	}
	d := divisor.Value()
	if d <= 0 {
		return "", fmt.Errorf("bad divisor %q", divisor.String())
	}
	return fmt.Sprintf("%d", (q.Value()+d-1)/d), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

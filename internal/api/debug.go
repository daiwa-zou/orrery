package api

import (
	"crypto/rand"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// debugRequest asks for an ephemeral debug container inside a running pod.
//
// The image is not part of it: the operator configures that (see
// config.DebugConfig). A console that accepted an image name from the browser
// would be a way to run arbitrary code inside another workload's namespaces.
type debugRequest struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// TargetContainer shares that container's process namespace, so the
	// debugger can see its processes and read its /proc. Empty attaches to the
	// pod without targeting a specific container.
	TargetContainer string `json:"targetContainer"`
}

type debugResponse struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace"`
	// Container is the generated name to exec into once it starts.
	Container string `json:"container"`
	Image     string `json:"image"`
}

// debugSuffix generates the disambiguating tail of a debug container's name.
// Ephemeral containers cannot be removed once added, so each attempt needs its
// own name or the second one collides with the first.
func debugSuffix() (string, error) {
	const alphabet = "bcdfghjklmnpqrstvwxz2456789"
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf), nil
}

// debugPod attaches an ephemeral container to a running pod.
//
// This is the answer to the case exec cannot reach: a container that is
// crash-looping has no running process to attach to, and a distroless image
// has no shell to attach with. An ephemeral container joins the pod with its
// own image and, when a target is named, that container's process namespace.
//
// Two properties are worth stating plainly. It is gated on `patch` of the
// pods/ephemeralcontainers subresource — the same permission kubectl debug
// needs, evaluated by the cluster rather than by the dashboard. And it is one
// way: Kubernetes has no API for removing an ephemeral container, so the
// container lives until the pod is replaced. The UI says so before asking.
func (a *API) debugPod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	req, err := decodeBody[debugRequest](r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if req.Namespace == "" || req.Pod == "" {
		a.writeErr(w, r, badRequest("namespace and pod are required"))
		return
	}

	res, err := a.resolveTarget(r, targetRef{
		Version: "v1", Resource: "pods", Namespace: req.Namespace, Name: req.Pod,
	})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	if err := a.authorize(ctx, res, "patch", req.Namespace, req.Pod, "ephemeralcontainers"); err != nil {
		a.writeErr(w, r, err)
		return
	}

	image := a.cfg.Debug.Image
	if image == "" {
		a.writeErr(w, r, badRequest("no debug image is configured; set debug.image"))
		return
	}

	pods := res.clients.Kube.CoreV1().Pods(req.Namespace)
	pod, err := pods.Get(ctx, req.Pod, metav1.GetOptions{})
	if err != nil {
		a.writeErr(w, r, err)
		return
	}

	// A pod that has finished will never start another container. The kubelet
	// acts on ephemeral containers only while the pod is running, and the API
	// server accepts the update regardless — so nothing fails, the container is
	// simply added to a pod that is done, and whoever asked is left watching a
	// spinner for a start that is not coming. Refusing here is the difference
	// between an answer and a wait with no end.
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		a.writeErr(w, r, badRequest(
			"pod %q has finished (%s), so the kubelet will not start a debug container in it — debug a running pod instead",
			req.Pod, pod.Status.Phase))
		return
	}

	// Targeting a container that does not exist fails inside the API server
	// with a message about the pod spec; catching it here says which name was
	// wrong and what the choices were.
	if req.TargetContainer != "" && !hasContainer(pod, req.TargetContainer) {
		a.writeErr(w, r, badRequest(
			"pod %q has no container %q (containers: %s)",
			req.Pod, req.TargetContainer, containerNames(pod)))
		return
	}

	suffix, err := debugSuffix()
	if err != nil {
		a.writeErr(w, r, fmt.Errorf("generating a container name: %w", err))
		return
	}
	name := "debugger-" + suffix

	pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, corev1.EphemeralContainer{
		EphemeralContainerCommon: corev1.EphemeralContainerCommon{
			Name:                     name,
			Image:                    image,
			ImagePullPolicy:          corev1.PullIfNotPresent,
			Stdin:                    true,
			TTY:                      true,
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
		TargetContainerName: req.TargetContainer,
	})

	if _, err := pods.UpdateEphemeralContainers(ctx, req.Pod, pod, metav1.UpdateOptions{}); err != nil {
		a.writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, debugResponse{
		Pod:       req.Pod,
		Namespace: req.Namespace,
		Container: name,
		Image:     image,
	})
}

func hasContainer(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func containerNames(pod *corev1.Pod) string {
	out := ""
	for i, c := range pod.Spec.Containers {
		if i > 0 {
			out += ", "
		}
		out += c.Name
	}
	if out == "" {
		return "none"
	}
	return out
}

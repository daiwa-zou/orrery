package api

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"slices"
	"strings"

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

	// Sharing a process namespace needs a process to share. The kubelet cannot
	// resolve a target that is not running, and it says so with a
	// CreateContainerConfigError that never resolves — the ephemeral container
	// sits in Waiting for the life of the pod, and it cannot be edited or
	// removed to try again. That is the shape of the hang this refuses: a pod
	// whose only container is stuck pulling its image, debugged at that
	// container, waits forever for a start that the config error has already
	// ruled out.
	if req.TargetContainer != "" {
		if state, running := containerState(pod, req.TargetContainer); !running {
			a.writeErr(w, r, badRequest(
				"container %q in pod %q is not running (%s), so a debug container cannot share its process namespace — debug it once it is running, or without targeting a container",
				req.TargetContainer, req.Pod, state))
			return
		}
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

// targetableContainers is every container a debug container may be pointed at.
//
// One list, because hasContainer and containerNames are the check and the
// message for the same question and they had drifted apart: the check accepted
// init containers and the message did not list them. Mistyping an init
// container's name was then answered with "pod "web" has no container
// "migrat" (containers: app, sidecar)" — a sentence whose parenthesis, read as
// the choices on offer, says init containers cannot be targeted at all. They
// can; the name was simply misspelt. A message that enumerates a different set
// from the one the check tests is worse than no message, because it is read as
// authoritative.
func targetableContainers(pod *corev1.Pod) []string {
	out := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.Containers {
		out = append(out, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		out = append(out, c.Name)
	}
	return out
}

func hasContainer(pod *corev1.Pod, name string) bool {
	return slices.Contains(targetableContainers(pod), name)
}

// containerState reports whether one of a pod's containers is running, and the
// word for what it is doing instead. The state is for the message: "not
// running" is the fact, and "ImagePullBackOff" is the reason someone can act
// on.
func containerState(pod *corev1.Pod, name string) (string, bool) {
	all := append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...),
		pod.Status.InitContainerStatuses...)
	for _, s := range all {
		if s.Name != name {
			continue
		}
		switch {
		case s.State.Running != nil:
			return "running", true
		case s.State.Waiting != nil && s.State.Waiting.Reason != "":
			return s.State.Waiting.Reason, false
		case s.State.Terminated != nil && s.State.Terminated.Reason != "":
			return s.State.Terminated.Reason, false
		}
		return "not started", false
	}
	// In the spec but not yet in the status: the kubelet has not reported on
	// it at all, which is not running either.
	return "not started", false
}

// containerNames renders the set hasContainer accepts, for the refusal that
// names it.
func containerNames(pod *corev1.Pod) string {
	names := targetableContainers(pod)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

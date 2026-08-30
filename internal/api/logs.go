package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/daiwa-zou/orrery/internal/cluster"
)

// podLogOptions builds the log request from query parameters, clamping the
// unbounded ones. An operator asking for "all logs since the beginning of
// time" across a busy pod is a good way to melt a dashboard.
func podLogOptions(r *http.Request, follow bool) *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{
		Container:  r.URL.Query().Get("container"),
		Follow:     follow,
		Timestamps: queryBool(r, "timestamps", false),
		Previous:   queryBool(r, "previous", false),
	}
	// The minimum clamp is 1, so a tail is always set — there is deliberately
	// no "all lines" mode; unbounded scrollback belongs to the log store, not
	// a browser tab.
	tail := int64(queryInt(r, "tailLines", 500, 1, 100000))
	opts.TailLines = &tail
	if s := queryInt(r, "sinceSeconds", 0, 0, 30*24*3600); s > 0 {
		since := int64(s)
		opts.SinceSeconds = &since
	}
	if b := queryInt(r, "limitBytes", 0, 0, 100<<20); b > 0 {
		limit := int64(b)
		opts.LimitBytes = &limit
	}
	return opts
}

// maxAggregatedPods bounds how many pods one request may read at once. A
// merged view of a hundred pods is a hundred streams held open against the API
// server by one caller, whether it follows them or takes a snapshot.
const maxAggregatedPods = 20

// logResource is the pods resource; logs are a subresource of it.
func (a *API) logResource(ctx context.Context, c *cluster.Cluster) (cluster.APIResource, error) {
	return c.Discovery.Resolve(ctx, "", "v1", "pods")
}

// authorizeLogs checks get on pods/log, the same permission kubectl needs.
func (a *API) authorizeLogs(ctx context.Context, res *resolved, namespace, pod string) error {
	ar, err := a.logResource(ctx, res.cluster)
	if err != nil {
		return err
	}
	res.resource = ar
	return a.authorize(ctx, res, "get", namespace, pod, "log")
}

// getPodLogs returns a snapshot of a container's logs as plain text.
func (a *API) getPodLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace, pod := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if err := a.authorizeLogs(ctx, res, namespace, pod); err != nil {
		a.writeErr(w, r, err)
		return
	}

	stream, err := res.clients.Kube.CoreV1().
		Pods(namespace).GetLogs(pod, podLogOptions(r, false)).
		Stream(ctx)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	defer stream.Close()

	if r.URL.Query().Get("download") == "true" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+pod+".log\"")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, stream); err != nil && !isClientGone(err) {
		a.log.Debug("log copy ended", "err", err)
	}
}

// podsLogSnapshot returns the recent logs of several pods in one JSON reply.
//
// The WebSocket at /ws/logs already merges many pods into one feed, and for a
// console watching a rollout that is the right shape. It is the wrong shape
// for a client that asks a question and wants an answer: a socket handshake,
// an origin check the caller cannot satisfy, a frame protocol and an idle
// connection, all to read the last hundred lines of three replicas. Request
// and response is what that client speaks, and "what are these pods saying?"
// is one of the first questions asked about a failing workload.
//
// Every pod is authorized on its own, exactly as the stream does — reading
// several at once is a convenience over the same per-object checks, never a
// way around them. One unreadable pod is reported in its own entry rather than
// failing the batch, so a terminating replica does not hide its healthy
// siblings; a pod the caller may not read at all is refused outright, because
// silently dropping it would present a partial answer as a complete one.
func (a *API) podsLogSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	pods := r.URL.Query()["pod"]
	if namespace == "" || len(pods) == 0 {
		a.writeErr(w, r, badRequest("namespace and at least one pod are required"))
		return
	}
	if len(pods) > maxAggregatedPods {
		a.writeErr(w, r, badRequest(
			"cannot read more than %d pods at once (asked for %d)", maxAggregatedPods, len(pods)))
		return
	}
	for _, pod := range pods {
		if err := a.authorizeLogs(ctx, res, namespace, pod); err != nil {
			a.writeErr(w, r, err)
			return
		}
	}

	opts := podLogOptions(r, false)
	out := logSnapshotResponse{
		Namespace: namespace,
		Container: opts.Container,
		Pods:      make([]podLogSnapshot, len(pods)),
	}

	// Read concurrently: the batch is capped well below anything that would
	// stress the API server, and a serial read makes the caller wait for the
	// sum of what it could have waited for the maximum of.
	var wg sync.WaitGroup
	for i, pod := range pods {
		wg.Add(1)
		go func(i int, pod string) {
			defer wg.Done()
			out.Pods[i] = a.readPodLog(ctx, res, namespace, pod, opts)
		}(i, pod)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, out)
}

// logSnapshotResponse is one batch of pod logs.
type logSnapshotResponse struct {
	Namespace string           `json:"namespace"`
	Container string           `json:"container,omitempty"`
	Pods      []podLogSnapshot `json:"pods"`
}

type podLogSnapshot struct {
	Pod   string   `json:"pod"`
	Lines []string `json:"lines"`
	// Error explains a pod that could not be read — still terminating, no such
	// container, no previous instance. The other pods are unaffected.
	Error string `json:"error,omitempty"`
	// Truncated marks a pod whose output hit the line ceiling, so a cut log is
	// not read as the whole story.
	Truncated bool `json:"truncated,omitempty"`
}

// maxSnapshotLines caps one pod's share of a snapshot. TailLines already
// bounds what the API server sends, but a single line can be arbitrarily long
// and a JSON reply is held whole in memory at both ends.
const maxSnapshotLines = 10000

func (a *API) readPodLog(
	ctx context.Context,
	res *resolved,
	namespace, pod string,
	opts *corev1.PodLogOptions,
) podLogSnapshot {
	out := podLogSnapshot{Pod: pod, Lines: []string{}}
	stream, err := res.clients.Kube.CoreV1().
		Pods(namespace).GetLogs(pod, opts).
		Stream(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		if len(out.Lines) >= maxSnapshotLines {
			out.Truncated = true
			break
		}
		out.Lines = append(out.Lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil && !isClientGone(err) {
		out.Error = err.Error()
	}
	return out
}

// streamPodLogs follows a container's logs over a WebSocket.
// taggedLine is one log line and the pod it came from. Aggregating several
// pods into one stream means the pod name has to travel with the text.
type taggedLine struct {
	pod  string
	text string
}

// streamPodLogs streams one pod's logs, or several merged into one feed.
//
// Repeating the pod parameter aggregates: "what are all twelve replicas of
// this deployment saying right now?" is the question during an incident, and
// answering it by opening twelve tabs is not answering it. Each pod is
// authorized on its own — aggregation is a convenience over the same
// per-object checks, never a way around them — and lines carry the pod they
// came from so the merged view stays readable.
//
// The single-pod wire format is unchanged, so an aggregated stream is the only
// one that pays for the extra field.
func (a *API) streamPodLogs(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	pods := r.URL.Query()["pod"]
	if namespace == "" || len(pods) == 0 {
		a.writeErr(w, r, badRequest("namespace and at least one pod are required"))
		return
	}
	if len(pods) > maxAggregatedPods {
		a.writeErr(w, r, badRequest(
			"cannot follow more than %d pods at once (asked for %d)", maxAggregatedPods, len(pods)))
		return
	}
	aggregated := len(pods) > 1

	// Authorize every pod before opening the socket, so a caller who may read
	// some of a workload's pods but not others is refused outright rather than
	// shown a partial feed they would read as complete.
	for _, pod := range pods {
		if err := a.authorizeLogs(r.Context(), res, namespace, pod); err != nil {
			a.writeErr(w, r, err)
			return
		}
	}

	conn, err := a.upgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws := newWSConn(conn)
	defer ws.close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go ws.ping(ctx)
	go func() { ws.drain(); cancel() }()

	// Batch lines briefly before sending. A pod logging ten thousand lines a
	// second would otherwise become ten thousand WebSocket frames a second,
	// and the browser, not the cluster, becomes the bottleneck.
	const (
		flushInterval = 100 * time.Millisecond
		maxBatchLines = 500
	)

	lines := make(chan taggedLine, 4096)
	var readers sync.WaitGroup

	for _, pod := range pods {
		stream, err := res.clients.Kube.CoreV1().
			Pods(namespace).GetLogs(pod, podLogOptions(r, true)).
			Stream(ctx)
		if err != nil {
			// One unreadable pod should not sink a merged feed — a replica can
			// be terminating while its siblings are healthy. Alone, it is fatal.
			if !aggregated {
				ws.wsError(err.Error())
				return
			}
			_ = ws.WriteJSON(map[string]any{
				"type": "STREAM_ERROR", "pod": pod, "reason": err.Error(),
			})
			continue
		}

		readers.Add(1)
		go func(pod string, stream io.ReadCloser) {
			defer readers.Done()
			defer stream.Close()
			scanner := bufio.NewScanner(stream)
			scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for scanner.Scan() {
				select {
				case lines <- taggedLine{pod: pod, text: scanner.Text()}:
				case <-ctx.Done():
					return
				}
			}
		}(pod, stream)
	}

	// Closes the channel once every pod's reader has finished, which is what
	// turns "all streams ended" into a single EOF for the client.
	go func() {
		readers.Wait()
		close(lines)
	}()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	// A follow can run for days; like watches, it must not outlive the
	// permission that opened it. The API server only checked at request time.
	reauth := time.NewTicker(reauthorizeInterval)
	defer reauth.Stop()

	// Batched per pod: a frame carries lines from one pod, so the client never
	// has to guess which of a merged batch came from where.
	batch := make([]string, 0, maxBatchLines)
	batchPod := ""
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		msg := map[string]any{"type": "LOG", "lines": batch}
		if aggregated {
			msg["pod"] = batchPod
		}
		err := ws.WriteJSON(msg)
		batch = batch[:0]
		return err == nil
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case <-ws.Done():
			return
		case <-reauth.C:
			if err := a.refreshStreamIdentity(ctx, r, res); err != nil {
				flush()
				ws.wsError("session expired; sign in again")
				return
			}
			for _, pod := range pods {
				if err := a.authorizeLogs(ctx, res, namespace, pod); err != nil {
					flush()
					ws.wsError(streamClosedBecause(err, "access to this pod's logs was revoked"))
					return
				}
			}
		case <-ticker.C:
			if !flush() {
				return
			}
		case line, ok := <-lines:
			if !ok {
				flush()
				_ = ws.WriteJSON(map[string]any{"type": "EOF"})
				ws.closeWith(1000, "log stream ended")
				return
			}
			// A batch belongs to one pod; a line from another closes it first.
			if line.pod != batchPod {
				if !flush() {
					return
				}
				batchPod = line.pod
			}
			batch = append(batch, line.text)
			if len(batch) >= maxBatchLines && !flush() {
				return
			}
		}
	}
}

// isClientGone recognises the errors that mean the browser hung up, which are
// normal and not worth logging as failures.
func isClientGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "client disconnected")
}

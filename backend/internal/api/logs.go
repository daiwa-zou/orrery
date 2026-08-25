package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/daiwa-zou/orrery/backend/internal/cluster"
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
	if n := queryInt(r, "tailLines", 500, 1, 100000); n > 0 {
		tail := int64(n)
		opts.TailLines = &tail
	}
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

// streamPodLogs follows a container's logs over a WebSocket.
func (a *API) streamPodLogs(w http.ResponseWriter, r *http.Request) {
	res, err := a.clusterOnly(r)
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	pod := r.URL.Query().Get("pod")
	if namespace == "" || pod == "" {
		a.writeErr(w, r, badRequest("namespace and pod are required"))
		return
	}
	if err := a.authorizeLogs(r.Context(), res, namespace, pod); err != nil {
		a.writeErr(w, r, err)
		return
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

	stream, err := res.clients.Kube.CoreV1().
		Pods(namespace).GetLogs(pod, podLogOptions(r, true)).
		Stream(ctx)
	if err != nil {
		ws.wsError(err.Error())
		return
	}
	defer stream.Close()

	// Batch lines briefly before sending. A pod logging ten thousand lines a
	// second would otherwise become ten thousand WebSocket frames a second,
	// and the browser, not the cluster, becomes the bottleneck.
	const (
		flushInterval = 100 * time.Millisecond
		maxBatchLines = 500
	)

	lines := make(chan string, 4096)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	// A follow can run for days; like watches, it must not outlive the
	// permission that opened it. The API server only checked at request time.
	reauth := time.NewTicker(reauthorizeInterval)
	defer reauth.Stop()

	batch := make([]string, 0, maxBatchLines)
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		err := ws.WriteJSON(map[string]any{"type": "LOG", "lines": batch})
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
			if err := a.authorizeLogs(ctx, res, namespace, pod); err != nil {
				flush()
				ws.wsError("access to this pod's logs was revoked")
				return
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
			batch = append(batch, line)
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

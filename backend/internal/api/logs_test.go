package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestPodLogOptionsDefaults(t *testing.T) {
	opts := podLogOptions(req(""), false)
	if opts.Container != "" || opts.Follow || opts.Timestamps || opts.Previous {
		t.Errorf("defaults = %+v", opts)
	}
	// There is deliberately no "all lines" mode: the tail is always set.
	if opts.TailLines == nil || *opts.TailLines != 500 {
		t.Errorf("default tail = %v, want 500", opts.TailLines)
	}
	if opts.SinceSeconds != nil || opts.LimitBytes != nil {
		t.Errorf("unrequested bounds were set: %+v", opts)
	}
}

func TestPodLogOptionsReadsQuery(t *testing.T) {
	opts := podLogOptions(req("container=app&timestamps=true&previous=1&tailLines=42&sinceSeconds=3600&limitBytes=1024"), true)
	if opts.Container != "app" || !opts.Follow || !opts.Timestamps || !opts.Previous {
		t.Errorf("flags = %+v", opts)
	}
	if *opts.TailLines != 42 {
		t.Errorf("tail = %d", *opts.TailLines)
	}
	if opts.SinceSeconds == nil || *opts.SinceSeconds != 3600 {
		t.Errorf("sinceSeconds = %v", opts.SinceSeconds)
	}
	if opts.LimitBytes == nil || *opts.LimitBytes != 1024 {
		t.Errorf("limitBytes = %v", opts.LimitBytes)
	}
}

func TestPodLogOptionsClamps(t *testing.T) {
	// tailLines=0 would mean "everything"; the clamp forbids that.
	opts := podLogOptions(req("tailLines=0"), false)
	if *opts.TailLines != 1 {
		t.Errorf("tail floor = %d, want 1", *opts.TailLines)
	}
	opts = podLogOptions(req("tailLines=99999999"), false)
	if *opts.TailLines != 100000 {
		t.Errorf("tail ceiling = %d, want 100000", *opts.TailLines)
	}
	// sinceSeconds is capped at 30 days.
	opts = podLogOptions(req("sinceSeconds=999999999"), false)
	if *opts.SinceSeconds != 30*24*3600 {
		t.Errorf("since ceiling = %d", *opts.SinceSeconds)
	}
}

func TestIsClientGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"wrapped cancel", fmt.Errorf("copy: %w", context.Canceled), true},
		{"closed pipe", io.ErrClosedPipe, true},
		{"broken pipe text", errors.New("write tcp 1.2.3.4: broken pipe"), true},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"client disconnected", errors.New("http2: client disconnected"), true},
		// A real failure must never be swallowed as "the browser left".
		{"genuine error", errors.New("stream error: unexpected EOF"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isClientGone(tc.err); got != tc.want {
				t.Errorf("isClientGone(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

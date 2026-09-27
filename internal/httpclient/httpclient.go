// Package httpclient holds the one HTTP client every fetch goes through: the
// http backend, the sidecar digests and the apt repository.
//
// http.DefaultClient has no timeouts and follows every redirect. A server that
// accepts the connection and then stops sending would hang an init container
// with no error, and an https URL that redirects to plain http would deliver
// bytes nobody authenticated. This client limits both.
//
// There is no total timeout: an archive dump is gigabytes, and a limit that
// suits it would not detect a stall on a small file for a long time. Instead,
// each Read of a response body must return within IdleRead.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Timeouts are the limits the client applies. A zero value disables that
// limit.
type Timeouts struct {
	// ResponseHeader is the time to wait for the response headers after the
	// request is sent.
	ResponseHeader time.Duration
	// TLSHandshake is the time allowed for the TLS handshake.
	TLSHandshake time.Duration
	// IdleRead is the time one Read of a response body may wait for data. It
	// starts again at each Read, so a slow but moving transfer never hits it.
	IdleRead time.Duration
}

// DefaultTimeouts are the limits of Client.
var DefaultTimeouts = Timeouts{
	ResponseHeader: 30 * time.Second,
	TLSHandshake:   10 * time.Second,
	IdleRead:       60 * time.Second,
}

// maxRedirects is the number of redirects followed before the request fails.
const maxRedirects = 10

// Client is the client the http backend and the apt backend use.
var Client = New(DefaultTimeouts)

// New returns a client with the given limits and the redirect policy of
// checkRedirect. Tests use it to set short limits.
func New(t Timeouts) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = t.ResponseHeader
	base.TLSHandshakeTimeout = t.TLSHandshake
	return &http.Client{
		Transport:     &idleTransport{base: base, idle: t.IdleRead},
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect refuses a redirect that leaves https after any earlier hop
// used it, and stops after maxRedirects hops. A redirect to plain http would
// hand the download to whoever controls the network path.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Scheme == "https" {
		return nil
	}
	for _, prev := range via {
		if prev.URL.Scheme == "https" {
			return fmt.Errorf("refusing redirect from %s to %s: it leaves https",
				prev.URL.Redacted(), req.URL.Redacted())
		}
	}
	return nil
}

// errIdle is the cause a body read is cancelled with when no data arrives
// within the idle limit.
var errIdle = errors.New("idle read timeout")

// idleTransport gives every response body an idle-read limit. The request
// runs on a context of its own, and the body cancels that context when one
// Read waits longer than idle. The cancellation also stops a Read that is
// blocked in the network.
type idleTransport struct {
	base http.RoundTripper
	idle time.Duration
}

func (t *idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.idle <= 0 {
		return t.base.RoundTrip(req)
	}
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	resp.Body = &idleBody{body: resp.Body, ctx: ctx, cancel: cancel, idle: t.idle}
	return resp, nil
}

type idleBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	idle   time.Duration
	timer  *time.Timer // created by the first Read
}

// Read arms the timer for the duration of the call only, so the time the
// caller spends writing the data to disk does not count as idle.
func (b *idleBody) Read(p []byte) (int, error) {
	if b.timer == nil {
		b.timer = time.AfterFunc(b.idle, func() { b.cancel(errIdle) })
	} else {
		b.timer.Reset(b.idle)
	}
	n, err := b.body.Read(p)
	b.timer.Stop()
	if err != nil && errors.Is(context.Cause(b.ctx), errIdle) {
		return n, fmt.Errorf("no data received for %s: %w", b.idle, errIdle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	if b.timer != nil {
		b.timer.Stop()
	}
	err := b.body.Close()
	b.cancel(nil)
	return err
}

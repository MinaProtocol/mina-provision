package httpclient

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// trust makes c accept the certificate of a TLS test server.
func trust(c *http.Client, srv *httptest.Server) {
	base := c.Transport.(*idleTransport).base.(*http.Transport)
	base.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
}

// An https URL that redirects to plain http must fail, not return the body of
// the plain-http server.
func TestRedirectFromHTTPSToHTTPIsRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("unauthenticated bytes"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/file", http.StatusFound)
	}))
	defer secure.Close()

	c := New(DefaultTimeouts)
	trust(c, secure)
	resp, err := c.Get(secure.URL + "/file")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the redirect to plain http to be refused")
	}
	if !strings.Contains(err.Error(), "leaves https") {
		t.Errorf("error does not say why: %v", err)
	}
}

func TestRedirectsAreCapped(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()

	resp, err := New(DefaultTimeouts).Get(srv.URL + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an endless redirect chain to fail")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A server that sends the headers and then stops sending must fail within the
// idle limit, not hang.
func TestStalledBodyFailsPromptly(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("some"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := New(Timeouts{ResponseHeader: time.Second, IdleRead: 200 * time.Millisecond})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	if !errors.Is(err, errIdle) {
		t.Fatalf("got %v, want an idle read timeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the stall was detected after %s", elapsed)
	}
}

// The idle limit is per Read, not a total: a transfer that keeps sending runs
// longer than the limit and succeeds.
func TestSlowButMovingBodySucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()

	c := New(Timeouts{ResponseHeader: time.Second, IdleRead: 150 * time.Millisecond})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("a moving transfer failed: %v", err)
	}
	if string(body) != strings.Repeat("chunk", 6) {
		t.Errorf("got %q", body)
	}
}

// A server that never sends the response headers must fail within the
// header limit.
func TestMissingResponseHeadersFail(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := New(Timeouts{ResponseHeader: 200 * time.Millisecond})
	start := time.Now()
	resp, err := c.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected a response header timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the timeout took %s", elapsed)
	}
}

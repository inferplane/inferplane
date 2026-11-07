package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientHeaderTimeoutAndStreaming(t *testing.T) {
	client := NewHTTPClient(nil)
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.ResponseHeaderTimeout != 120*time.Second || client.Timeout != 0 {
		t.Fatalf("unbounded headers or bounded stream: %#v", client)
	}
	if tr == http.DefaultTransport {
		t.Fatal("modified shared transport")
	}
	tr.ResponseHeaderTimeout = 30 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/headers" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("stream"))
	}))
	defer srv.Close()
	if resp, err := client.Get(srv.URL + "/headers"); err == nil {
		resp.Body.Close()
		t.Fatal("headers were not bounded")
	}
	resp, err := client.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(3*tr.ResponseHeaderTimeout, func() { close(release) })
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "stream" {
		t.Fatalf("header deadline terminated stream: %q, %v", body, err)
	}
}

func TestHTTPClientPreservesInjectedSettings(t *testing.T) {
	tr := &http.Transport{ResponseHeaderTimeout: time.Second}
	original := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	client := NewHTTPClient(original)
	if client == original || client.Transport != tr || client.Timeout != original.Timeout || original.CheckRedirect != nil {
		t.Fatal("injected client settings changed")
	}
	if client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("credential-bearing redirects allowed")
	}
}

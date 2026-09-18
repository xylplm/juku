package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type browserFixtureClient struct {
	do     func(*fhttp.Request) (*fhttp.Response, error)
	closed atomic.Int32
}

func (client *browserFixtureClient) Do(request *fhttp.Request) (*fhttp.Response, error) {
	return client.do(request)
}
func (client *browserFixtureClient) CloseIdleConnections() { client.closed.Add(1) }

func TestHuangguoBrowserHTTP2CookiesAndNativeIsolation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		if r.ProtoMajor != 2 || !strings.Contains(r.UserAgent(), "Chrome/150.0.0.0") || r.Header.Get("Sec-Ch-Ua-Platform") != `"macOS"` {
			t.Errorf("browser fingerprint headers or HTTP/2 missing: %s %s", r.Proto, r.UserAgent())
		}
		_, err := r.Cookie("fixture_session")
		if (count == 2) != (err == nil) {
			t.Errorf("cookie session reuse/reset failed at request %d", count)
		}
		http.SetCookie(w, &http.Cookie{Name: "fixture_session", Value: "local-test-only", Path: "/", Secure: true})
		io.WriteString(w, "fixture browser page")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	cfg := defaultConfig()
	cfg.dataDir, cfg.OutputDir, cfg.ProxyURL, cfg.Retries = t.TempDir(), t.TempDir(), "direct", 1
	cfg.HuangguoVideoURL, cfg.InsecureTLS = server.URL, true
	downloader := NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	downloader.limiter = newRequestLimiter(2, time.Nanosecond)
	transport := downloader.client.Transport.(*imageTransport).base.(*huangguoBrowserTransport)
	var nativeCalls atomic.Int32
	transport.base = rankingTransport(func(r *http.Request) (*http.Response, error) {
		nativeCalls.Add(1)
		return rankingHTTPResponse(r, 200, "native fixture"), nil
	})
	for index := 0; index < 3; index++ {
		if index == 2 {
			downloader.client.CloseIdleConnections()
		}
		body, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", "https://huangguo.video/")
		if err != nil || body != "fixture browser page" {
			t.Fatal(body, err)
		}
	}
	if nativeCalls.Load() != 0 {
		t.Fatal("browser request first used the native transport")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://hongguoduanju.com/", nil)
	response, err := downloader.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if nativeCalls.Load() != 1 {
		t.Fatal("unrelated source did not keep native transport")
	}
}

func TestHuangguoBrowserRespectsCertificatesAndCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "fixture") }))
	defer server.Close()
	cfg := defaultConfig()
	cfg.dataDir, cfg.OutputDir, cfg.ProxyURL, cfg.Retries = t.TempDir(), t.TempDir(), "direct", 1
	cfg.HuangguoVideoURL = server.URL
	downloader := NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	if _, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", ""); err == nil {
		t.Fatal("untrusted certificate was accepted by default")
	}
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer blocking.Close()
	cfg.HuangguoVideoURL = blocking.URL
	downloader = NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := downloader.fetchProviderText(ctx, "https://huangguo.video/videos", ""); err == nil {
		t.Fatal("canceled browser body read succeeded")
	}
	downloader.limiter.mu.Lock()
	active := downloader.limiter.active
	downloader.limiter.mu.Unlock()
	if active != 0 {
		t.Fatal("browser timeout leaked a limiter permit")
	}
}

func TestHuangguoBrowserProxyResetAndBackoff(t *testing.T) {
	cfg := defaultConfig()
	cfg.dataDir, cfg.OutputDir, cfg.ProxyURL, cfg.Retries = t.TempDir(), t.TempDir(), "http://proxy-one.invalid:8080", 3
	downloader := NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	downloader.limiter = newRequestLimiter(2, time.Nanosecond)
	transport := downloader.client.Transport.(*imageTransport).base.(*huangguoBrowserTransport)
	var routes []string
	var clients []*browserFixtureClient
	calls := 0
	transport.newClient = func(proxy string) (browserHTTPClient, error) {
		routes = append(routes, proxy)
		client := &browserFixtureClient{do: func(r *fhttp.Request) (*fhttp.Response, error) {
			calls++
			status, body := 403, "Cloudflare: Sorry, you have been blocked"
			if len(routes) > 1 {
				status, body = 200, "fixture success"
			}
			return &fhttp.Response{StatusCode: status, Header: fhttp.Header{"Cf-Ray": {"fixture-ray"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}}
		clients = append(clients, client)
		return client, nil
	}
	for index := 0; index < 2; index++ {
		_, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", "")
		if err == nil || !strings.Contains(err.Error(), "Cloudflare 拒绝了当前请求") {
			t.Fatal("missing accurate block cause", err)
		}
	}
	if calls != 1 || len(routes) != 1 {
		t.Fatal("backoff did not prevent repeated requests", calls, len(routes))
	}
	downloader.proxyRouter.configure("socks5://proxy-two.invalid:1080")
	downloader.client.CloseIdleConnections()
	if clients[0].closed.Load() == 0 {
		t.Fatal("old proxy session not closed")
	}
	_, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", "")
	if err == nil || calls != 1 {
		t.Fatal("proxy switch bypassed the existing backoff")
	}
	downloader.limiter.mu.Lock()
	delete(downloader.limiter.backoffs, "huangguo.video")
	downloader.limiter.mu.Unlock()
	body, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", "")
	if err != nil || body != "fixture success" || len(routes) != 2 || routes[1] != "socks5://proxy-two.invalid:1080" {
		t.Fatal("updated proxy was not used", routes, err)
	}
}

func TestHuangguoBrowserRedirectUsesNativePolicy(t *testing.T) {
	cfg := defaultConfig()
	cfg.dataDir, cfg.OutputDir, cfg.ProxyURL = t.TempDir(), t.TempDir(), "direct"
	downloader := NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	transport := downloader.client.Transport.(*imageTransport).base.(*huangguoBrowserTransport)
	transport.newClient = func(string) (browserHTTPClient, error) {
		return &browserFixtureClient{do: func(r *fhttp.Request) (*fhttp.Response, error) {
			return &fhttp.Response{StatusCode: 302, Header: fhttp.Header{"Location": {"https://redirect.invalid/fixture"}, "Set-Cookie": {"fixture_session=private; Path=/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}}, nil
	}
	transport.base = rankingTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "redirect.invalid" || r.Header.Get("Cookie") != "" || r.Header.Get("Sec-Ch-Ua") != "" {
			t.Error("redirect leaked fingerprint session headers")
		}
		return rankingHTTPResponse(r, 200, "redirected fixture"), nil
	})
	body, err := downloader.fetchProviderText(context.Background(), "https://huangguo.video/videos", "")
	if err != nil || body != "redirected fixture" {
		t.Fatal(body, err)
	}
}

func TestHuangguoBrowserLivePages(t *testing.T) {
	if os.Getenv("JUKU_HUANGGUO_BROWSER_PROBE") != "1" {
		t.Skip("text-only live probe is opt-in")
	}
	cfg := defaultConfig()
	cfg.dataDir, cfg.OutputDir, cfg.ProxyURL, cfg.Retries = t.TempDir(), t.TempDir(), firstNonEmpty(os.Getenv("JUKU_HUANGGUO_BROWSER_PROXY"), "direct"), 1
	cfg.RequestIntervalMS = 2000
	downloader := NewDownloader(cfg)
	defer downloader.client.CloseIdleConnections()
	var events []diagnosticEvent
	transport := downloader.client.Transport.(*imageTransport).base.(*huangguoBrowserTransport)
	transport.record = func(event diagnosticEvent) { events = append(events, event) }
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	urls := []string{"https://huangguo.video/videos", "https://huangguo.video/videos?category=1"}
	counts := []int{}
	detailURL := ""
	for _, address := range urls {
		body, err := downloader.fetchProviderText(ctx, address, "https://huangguo.video/")
		if err != nil {
			t.Fatal(err)
		}
		dramas := parseHuangguoVideoCards(body, address)
		if len(dramas) == 0 {
			t.Fatal("200 page contained no business records")
		}
		counts = append(counts, len(dramas))
		if detailURL == "" {
			for _, drama := range dramas {
				_, id, ok := splitProviderDramaID(drama.ID)
				if ok && strings.HasPrefix(id, "series/") {
					detailURL = "https://huangguo.video/" + id
					break
				}
			}
		}
	}
	if detailURL == "" {
		t.Fatal("missing linked detail")
	}
	body, err := downloader.fetchProviderText(ctx, detailURL, "https://huangguo.video/")
	if err != nil {
		t.Fatal(err)
	}
	episodes := parseHuangguoVideoEpisodes(body, detailURL)
	if len(episodes) == 0 {
		t.Fatal("detail contained no episode links")
	}
	counts = append(counts, len(episodes))
	evidence := map[string]any{"time": time.Now().UTC(), "client": "tls-client v1.16.0 / Chrome 150", "counts": counts, "requests": events, "route": map[bool]string{true: "direct", false: "configured-proxy"}[cfg.ProxyURL == "direct"]}
	if output := os.Getenv("JUKU_HUANGGUO_BROWSER_EVIDENCE"); output != "" {
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(filepath.Clean(output), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range events {
		if event.HTTPStatus != 200 || event.Protocol != "HTTP/2.0" {
			t.Fatal("unexpected live response", event.HTTPStatus, event.Protocol)
		}
	}
	t.Logf("text-only list/category/detail counts: %v; responses=%d", counts, len(events))
}

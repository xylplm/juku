package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestHuangdouCoverUsesActiveAPIEntry(t *testing.T) {
	for _, test := range []struct {
		name, configured, preferred, remote, referer string
	}{
		{"default", "", "", "https://covers.example.org/text?signature=%2F%2B%3D", "https://tideember.cc/home"},
		{"fallback", "", "https://xqjurgek.top", "https://covers.example.org/text?signature=%2F%2B%3D", "https://xqjurgek.top/home"},
		{"configured", "https://mirror.example.org/base/", "", "https://covers.example.org/text?signature=%2F%2B%3D", "https://mirror.example.org/base/home"},
		{"same-site", "", "https://xqjurgek.top", "https://tideember.cc/text?signature=%2F%2B%3D", "https://tideember.cc/home"},
		{"mirror-image", "", "", "https://xqjurgek.top/text?signature=%2F%2B%3D", "https://xqjurgek.top/home"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Header.Get("Referer") != test.referer || request.URL.String() != test.remote {
					t.Error("cover did not follow the API entry or changed its signature", request.Header.Get("Referer"), request.URL.String())
				}
				return rankingHTTPResponse(request, http.StatusServiceUnavailable, "text-only fixture"), nil
			})
			d.cfg.HuangdouURL = test.configured
			d.providerHosts[sourceHuangdou] = test.preferred
			app := &UIApp{downloader: d, cfg: d.cfg}
			ctx := context.WithValue(context.Background(), coverSourceKey{}, sourceHuangdou)
			_, err := app.loadCoverImage(ctx, test.remote, nil)
			if calls != 1 || err == nil || !strings.Contains(err.Error(), "上游 HTTP 503") {
				t.Fatal("expected only the isolated textual transport", calls, err)
			}
		})
	}
}

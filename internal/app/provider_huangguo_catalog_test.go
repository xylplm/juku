package app

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHuangguoVideoStopsOnCloudflareBlock(t *testing.T) {
	var calls atomic.Int32
	d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return rankingHTTPResponse(request, 403, "Cloudflare: Sorry, you have been blocked"), nil
	})
	items, err := d.fetchHuangguoVideoDramas(context.Background())
	if len(items) != 0 || err == nil || !strings.Contains(err.Error(), "Cloudflare 拒绝了当前请求") || calls.Load() != 1 {
		t.Fatal("blocked catalog lost its cause or repeated requests", err)
	}
}

func TestHuangguoAICatalogUsesAvailableCategoryAPIs(t *testing.T) {
	var APIs atomic.Int32
	d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/api/videos/category/") {
			APIs.Add(1)
			if !strings.Contains(request.URL.Path, "/category/ai-") {
				t.Error("HTML recommendation page was requested through a nonexistent category API")
			}
			return rankingHTTPResponse(request, 200, `{"data":{"items":[{"id":"123","title":"无图目录样本"}],"pagination":{"pages":1}}}`), nil
		}
		return rankingHTTPResponse(request, 200, "<html>local text fixture</html>"), nil
	})
	d.cfg.MaxPagesPerSort = 1
	items, err := d.fetchHuangguoAIDramas(context.Background())
	if err != nil || len(items) != 1 || APIs.Load() != 4 {
		t.Fatalf("catalog paths: items=%d APIs=%d error=%v", len(items), APIs.Load(), err)
	}
}

func TestHuangguoVideoValidListAllowsEmptyCategories(t *testing.T) {
	for _, validList := range []bool{true, false} {
		var calls atomic.Int32
		d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			body := "<main>当前分类暂无内容</main>"
			if validList && request.URL.RawQuery == "" {
				body = `<article class="video-card"><a href="/series/fixture01" title="纯文字目录样本">目录样本</a></article>`
			}
			return rankingHTTPResponse(request, 200, body), nil
		})
		d.cfg.MaxPagesPerSort = 2
		items, err := d.fetchHuangguoVideoDramas(context.Background())
		if validList && (err != nil || len(items) != 1 || calls.Load() != 6) {
			t.Fatal("empty category rejected a valid catalog", err, len(items), calls.Load())
		}
		if !validList && (err == nil || len(items) != 0 || calls.Load() != 1) {
			t.Fatal("empty main response was reported as a successful catalog", err, len(items), calls.Load())
		}
	}
}

func TestHuangguoVideoCatalogPaginatesConfiguredPages(t *testing.T) {
	var queries []string
	d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		query := request.URL.RawQuery
		queries = append(queries, query)
		values := request.URL.Query()
		page := values.Get("page")
		if page == "" {
			page = "1"
		}
		category := values.Get("category")
		if category == "" {
			category = "0"
		}
		id := "fixture-c" + category + "-p" + page
		body := `<article class="video-card"><a href="/series/` + id + `" title="纯文字目录样本 ` + id + `">目录样本</a></article>`
		return rankingHTTPResponse(request, 200, body), nil
	})
	d.cfg.MaxPagesPerSort = 2
	items, err := d.fetchHuangguoVideoDramas(context.Background())
	if err != nil || len(items) != 10 {
		t.Fatalf("paginated catalog failed: items=%d error=%v", len(items), err)
	}
	want := []string{"", "page=2", "category=1", "category=1&page=2", "category=2", "category=2&page=2", "category=3", "category=3&page=2", "category=4", "category=4&page=2"}
	for index, query := range want {
		if index >= len(queries) || queries[index] != query {
			t.Fatalf("wrong paginated requests: got=%v want=%v", queries, want)
		}
	}
}

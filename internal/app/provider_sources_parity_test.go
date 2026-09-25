package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProviderSourceParsesTextFixturesAndRejectsMismatchedIdentity(t *testing.T) {
	huangjuRow := map[string]any{"id": "77", "slug": "sample-drama", "title": "剧果样本", "totalEpisodes": json.Number("2"), "status": "completed", "categories": []any{"热播"}, "year": "2026", "score": "8.5"}
	drama, err := huangjuDramaFromMap(huangjuRow, huangjuBaseURL, "")
	if err != nil || drama.ID != "huangju:sample-drama-77" || drama.ReleaseStatus != "finished" || drama.EpisodeCount != 2 || drama.ChannelName != "剧果" {
		t.Fatalf("huangju text row parsed incorrectly: %+v %v", drama, err)
	}
	if _, err := huangjuDramaFromMap(huangjuRow, huangjuBaseURL, "sample-drama-78"); err == nil {
		t.Fatal("huangju accepted a mismatched source id")
	}
	if validHuangjuID("bad:value") || !validHuangjuID(strings.Repeat("a", 450)) || validHuangjuID(strings.Repeat("a", 451)) {
		t.Fatal("huangju id validation changed")
	}
	yeguoRow := map[string]any{"video_id": "123", "title": "野果样本", "episode_count": "12", "serialize_status": "2", "play_count_text": "3.4万", "published_at": "2026-09-23T10:20:30+08:00", "is_vip": "1", "tags": []any{"甜宠", "重生"}}
	yeguo, err := yeguoDramaFromMap(yeguoRow, yeguoBaseURL)
	if err != nil || yeguo.ID != "yeguo:123" || yeguo.ReleaseStatus != "finished" || yeguo.OnlineDate != "2026-09-23" || yeguo.VIP == nil || !*yeguo.VIP {
		t.Fatalf("yeguo text row parsed incorrectly: %+v %v", yeguo, err)
	}
	page := "https://www.dsd.com.se/index.php/vod/type/id/9/page/1.html"
	action, values, valid := dsdRouteParameters(page, "/index.php/vod/play/id/456/sid/1/nid/2.html?extra=1")
	if !valid || action != "play" || values["id"] != "456" || values["sid"] != "1" || values["nid"] != "2" || values["extra"] != "1" {
		t.Fatal("dsd route parser lost valid fields", action, values, valid)
	}
	if _, _, valid = dsdRouteParameters(page, "https://other.example/index.php/vod/play/id/456/sid/1/nid/2.html"); valid {
		t.Fatal("dsd accepted a cross-origin route")
	}
}

func TestProviderCatalogCursorAdvancesOnlyAfterUsefulPages(t *testing.T) {
	dramaCalls := 0
	d := providerSourceFixtureDownloader(t, func(request *http.Request, form url.Values) (*http.Response, error) {
		if request.URL.Host != "api.huangju.test" {
			t.Fatalf("unexpected host: %s", request.URL.Host)
		}
		if request.URL.Path == "/auth/guest" {
			return rankingHTTPResponse(request, http.StatusOK, `{"token":"guest-token"}`), nil
		}
		if request.URL.Path != "/dramas" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		dramaCalls++
		page := request.URL.Query().Get("page")
		if request.URL.Query().Get("q") != "" {
			t.Fatal("catalog cursor issued a search request")
		}
		switch page {
		case "1":
			return rankingHTTPResponse(request, http.StatusOK, `{"items":[{"id":"1","slug":"first","title":"第一页"}],"page":1,"pageSize":1,"total":3}`), nil
		case "2":
			return rankingHTTPResponse(request, http.StatusOK, `{"items":[{"id":"2","slug":"second","title":"第二页"}],"page":2,"pageSize":1,"total":3}`), nil
		default:
			return rankingHTTPResponse(request, http.StatusServiceUnavailable, `temporarily unavailable`), nil
		}
	})
	first, err := d.fetchPagedProviderDramas(context.Background(), sourceHuangju)
	if err != nil || len(first) != 1 || d.providerCatalogSnapshot()[sourceHuangju].Page != 1 {
		t.Fatalf("first catalog page did not initialize cursor: %+v %v", d.providerCatalogSnapshot(), err)
	}
	next, err := d.fetchPagedProviderDramas(context.WithValue(context.Background(), libraryMoreKey{}, true), sourceHuangju)
	if err != nil || len(next) != 1 || d.providerCatalogSnapshot()[sourceHuangju].Page != 2 {
		t.Fatalf("second catalog page did not advance cursor: %+v %v", d.providerCatalogSnapshot(), err)
	}
	failed, err := d.fetchPagedProviderDramas(context.WithValue(context.Background(), libraryMoreKey{}, true), sourceHuangju)
	if err == nil || len(failed) != 0 || d.providerCatalogSnapshot()[sourceHuangju].Page != 2 || dramaCalls != 3 {
		t.Fatalf("failed page advanced or hid cursor state: %+v %d %v", d.providerCatalogSnapshot(), dramaCalls, err)
	}
	restarted := NewDownloader(d.cfg)
	restarted.restoreProviderCatalog(d.providerCatalogSnapshot())
	if restarted.providerCatalogSnapshot()[sourceHuangju].Page != 2 {
		t.Fatalf("restored cursor lost page: %+v", restarted.providerCatalogSnapshot())
	}
}

func TestProviderCatalogUpdateLoadsConfiguredBatch(t *testing.T) {
	var pages []string
	d := providerSourceFixtureDownloader(t, func(request *http.Request, form url.Values) (*http.Response, error) {
		if request.URL.Host != "api.huangju.test" {
			t.Fatalf("unexpected host: %s", request.URL.Host)
		}
		if request.URL.Path == "/auth/guest" {
			return rankingHTTPResponse(request, http.StatusOK, `{"token":"guest-token"}`), nil
		}
		if request.URL.Path != "/dramas" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		page := request.URL.Query().Get("page")
		pages = append(pages, page)
		title := "第" + page + "页"
		id := page
		total := 5
		return rankingHTTPResponse(request, http.StatusOK, fmt.Sprintf(`{"items":[{"id":%q,"slug":"page-%s","title":%q}],"page":%s,"pageSize":1,"total":%d}`, id, page, title, page, total)), nil
	})
	d.cfg.MaxPagesPerSort = 3
	items, err := d.fetchPagedProviderDramas(context.WithValue(context.Background(), libraryUpdateKey{}, true), sourceHuangju)
	if err != nil || len(items) != 3 || !reflect.DeepEqual(pages, []string{"1", "2", "3"}) {
		t.Fatalf("update did not batch configured pages: pages=%v items=%d err=%v", pages, len(items), err)
	}
	if cursor := d.providerCatalogSnapshot()[sourceHuangju]; cursor.Page != 3 || cursor.Exhausted {
		t.Fatalf("cursor did not advance to third page: %+v", cursor)
	}
	pages = nil
	items, err = d.fetchPagedProviderDramas(context.WithValue(context.Background(), libraryMoreKey{}, true), sourceHuangju)
	if err != nil || len(items) != 1 || !reflect.DeepEqual(pages, []string{"4"}) {
		t.Fatalf("ordinary more should keep one-page continuation: pages=%v items=%d err=%v", pages, len(items), err)
	}
}

func TestProviderCatalogContinuesAfterRepeatedPage(t *testing.T) {
	var pages []string
	d := providerSourceFixtureDownloader(t, func(request *http.Request, form url.Values) (*http.Response, error) {
		if request.URL.Host != "api.yeguo.test" || request.URL.Path != "/api/theater/exploreList" {
			t.Fatalf("unexpected yeguo request: %s", request.URL.String())
		}
		if request.Method != http.MethodPost {
			t.Fatalf("yeguo catalog must use POST for pagination, got %s", request.Method)
		}
		page := form.Get("page")
		pages = append(pages, page)
		switch page {
		case "1", "2":
			body := `{"status":"1","data":{"list":[{"video_id":"9101","title":"重复页样本","episode_count":"6","serialize_status":"2"}],"page":` + page + `,"limit":1,"total":3,"has_more":"1"}}`
			return rankingHTTPResponse(request, http.StatusOK, body), nil
		case "3":
			return rankingHTTPResponse(request, http.StatusOK, `{"status":"1","data":{"list":[{"video_id":"9103","title":"恢复页样本","episode_count":"6","serialize_status":"2"}],"page":3,"limit":1,"total":3,"has_more":"0"}}`), nil
		default:
			t.Fatalf("unexpected yeguo page: %s", page)
			return nil, nil
		}
	})
	d.yeguoClient().access = &yeguoAccess{base: "https://api.yeguo.test", identifier: "fixture-trace", loadedAt: time.Now()}
	d.cfg.MaxPagesPerSort = 3
	items, err := d.fetchPagedProviderDramas(context.WithValue(context.Background(), libraryUpdateKey{}, true), sourceYeguo)
	if err != nil || len(items) != 2 || !reflect.DeepEqual(pages, []string{"1", "2", "3"}) {
		t.Fatalf("repeated yeguo page stopped pagination: pages=%v items=%d err=%v", pages, len(items), err)
	}
	cursor := d.providerCatalogSnapshot()[sourceYeguo]
	if cursor.Page != 3 || !cursor.Exhausted {
		t.Fatalf("repeated yeguo page did not advance cursor: %+v", cursor)
	}
}

func TestYeguoCatalogAcceptsSourcePageSize(t *testing.T) {
	d := providerSourceFixtureDownloader(t, func(request *http.Request, form url.Values) (*http.Response, error) {
		if request.URL.Host != "api.yeguo.test" || request.URL.Path != "/api/theater/exploreList" {
			t.Fatalf("unexpected yeguo request: %s", request.URL.String())
		}
		if request.Method != http.MethodPost {
			t.Fatalf("yeguo catalog must use POST for pagination, got %s", request.Method)
		}
		if form.Get("page") != "1" || form.Get("limit") != "20" {
			t.Fatalf("unexpected yeguo page request: %v", form)
		}
		rows := make([]any, 0, 30)
		for index := 1; index <= 30; index++ {
			id := strconv.Itoa(9000 + index)
			rows = append(rows, map[string]any{"video_id": id, "title": "野果目录样本 " + id, "episode_count": "6", "serialize_status": "2"})
		}
		body, _ := json.Marshal(map[string]any{"status": "1", "data": map[string]any{"list": rows, "limit": 20, "total": 30, "has_more": "0"}})
		return rankingHTTPResponse(request, http.StatusOK, string(body)), nil
	})
	d.yeguoClient().access = &yeguoAccess{base: "https://api.yeguo.test", identifier: "fixture-trace", loadedAt: time.Now()}
	items, more, err := d.fetchYeguoCatalogPage(context.Background(), 1, "", "")
	if err != nil || more || len(items) != 30 {
		t.Fatalf("yeguo source-sized page rejected: items=%d more=%t err=%v", len(items), more, err)
	}
}

func TestProviderMediaCredentialsLimitOriginAndUseBackupAddresses(t *testing.T) {
	primary := "https://media.example.test/main.mp4"
	backup := "https://media.example.test/backup.mp4"
	model := map[string]any{"video_duration": "6", "video_list": []any{map[string]any{
		"main_url": "invalid", "backup_url": base64.StdEncoding.EncodeToString([]byte(primary)), "backup_urls": []any{backup},
		"video_meta": map[string]any{"codec_type": "h264", "definition": "720p"},
	}}}
	media, err := selectHongguoAppMedia(model)
	if err != nil || media.URL != primary || len(media.Variants) != 2 || media.Variants[1].URL != backup || media.Duration != 6*time.Second {
		t.Fatalf("hongguo backup media fields were lost: %+v %v", media, err)
	}
	credentials := &providerMediaCredentials{origin: "https://media.example.test", cookie: "Signed=fixture", referer: "https://source.example.test/page", userAgent: "Agent"}
	request, _ := http.NewRequest(http.MethodGet, "https://media.example.test/part", nil)
	if err := credentials.apply(request); err != nil || request.Header.Get("Cookie") != "Signed=fixture" || request.Header.Get("Origin") != "https://source.example.test" || request.Header.Get("User-Agent") != "Agent" {
		t.Fatal("credentials not applied on exact origin", err, request.Header)
	}
	other, _ := http.NewRequest(http.MethodGet, "https://media.example.test:444/part", nil)
	other.Header.Set("Cookie", "old=value")
	if err := credentials.apply(other); err != nil || other.Header.Get("Cookie") != "" {
		t.Fatal("credentials leaked to another origin", err, other.Header)
	}
}

func TestProviderDSDMediaSigningPropagatesFetchErrors(t *testing.T) {
	d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "/addons/vplayer/") {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		return rankingHTTPResponse(request, http.StatusBadGateway, "unavailable"), nil
	})
	_, found, err := d.signDSDMedia(context.Background(), "https://media.example.test/video/index.m3u8", "https://www.dsd.com.se/index.php/vod/play/id/1/sid/1/nid/1.html", "https://www.dsd.com.se")
	if err == nil || found {
		t.Fatal("dsd signing failure was swallowed", found, err)
	}
	if path := dsdVplayerMediaPath("https://media.example.test/video/index.m3u8?token=a%2Fb"); path != "/video/index.m3u8?token=a%2Fb" {
		t.Fatal("dsd media path changed", path)
	}
}

func TestOnlineSearchPagesRespectPermissionsAndNextPages(t *testing.T) {
	d := providerSourceFixtureDownloader(t, func(request *http.Request, form url.Values) (*http.Response, error) {
		switch request.URL.Host {
		case "api.huangju.test":
			if request.URL.Path == "/auth/guest" {
				return rankingHTTPResponse(request, http.StatusOK, `{"token":"guest-token"}`), nil
			}
			if request.URL.Path != "/dramas" || request.URL.Query().Get("q") != "样本" || request.URL.Query().Get("page") != "2" {
				t.Fatalf("unexpected huangju search request: %s", request.URL.String())
			}
			return rankingHTTPResponse(request, http.StatusOK, `{"items":[{"id":"202","slug":"huangju-search","title":"剧果样本"}],"page":2,"pageSize":20,"total":21}`), nil
		case "api.yeguo.test":
			if request.Method != http.MethodPost {
				t.Fatalf("yeguo search must use POST for pagination, got %s", request.Method)
			}
			if request.URL.Path != "/api/search/result" || form.Get("keyword") != "样本" || form.Get("page") != "1" {
				t.Fatalf("unexpected yeguo search request: %s %v", request.URL.String(), form)
			}
			return rankingHTTPResponse(request, http.StatusOK, `{"status":"1","data":{"list":[{"video_id":"301","title":"野果样本","episode_count":"10","serialize_status":"2"}],"page":1,"limit":20,"total":21,"has_more":"1"}}`), nil
		default:
			t.Fatalf("unexpected host: %s", request.URL.Host)
		}
		return rankingHTTPResponse(request, http.StatusNotFound, ""), nil
	})
	d.yeguoClient().access = &yeguoAccess{base: "https://api.yeguo.test", identifier: "fixture-trace", loadedAt: time.Now()}
	a := &UIApp{downloader: d, cfg: d.cfg, metadataClosed: true}
	request := httptest.NewRequest(http.MethodGet, "/api/ui/search?q="+url.QueryEscape("样本")+"&source=all&pages="+url.QueryEscape(`{"huangju":2,"yeguo":1}`), nil)
	request = request.WithContext(withSourceScope(request.Context(), accountRecord{Sources: []string{sourceHuangju, sourceYeguo}}))
	writer := httptest.NewRecorder()
	a.handleLibrarySearch(writer, request)
	if writer.Code != http.StatusOK {
		t.Fatalf("online provider search failed: %d %s", writer.Code, writer.Body.String())
	}
	var result struct {
		Data      []Drama        `json:"data"`
		NextPages map[string]int `json:"nextPages"`
		HasMore   bool           `json:"hasMore"`
		Warning   string         `json:"warning"`
	}
	if err := json.NewDecoder(writer.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 2 || result.NextPages[sourceYeguo] != 2 || result.NextPages[sourceHuangju] != 0 || !result.HasMore || result.Warning != "" {
		t.Fatalf("provider search did not preserve per-source continuation: %+v", result)
	}
	for _, drama := range result.Data {
		if drama.Source != sourceHuangju && drama.Source != sourceYeguo {
			t.Fatalf("unauthorized source leaked: %+v", drama)
		}
	}
	request = httptest.NewRequest(http.MethodGet, "/api/ui/search?q="+url.QueryEscape("样本")+"&source=all&pages="+url.QueryEscape(`{"dsd":1}`), nil)
	request = request.WithContext(withSourceScope(request.Context(), accountRecord{Sources: []string{sourceHuangju}}))
	writer = httptest.NewRecorder()
	a.handleLibrarySearch(writer, request)
	if writer.Code != http.StatusForbidden {
		t.Fatalf("unauthorized provider continuation accepted: %d %s", writer.Code, writer.Body.String())
	}
}

func providerSourceFixtureDownloader(t *testing.T, transport func(*http.Request, url.Values) (*http.Response, error)) *Downloader {
	t.Helper()
	d := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		form := request.URL.Query()
		if request.Body != nil {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			bodyForm, _ := url.ParseQuery(string(body))
			for key, values := range bodyForm {
				form[key] = append(form[key], values...)
			}
		}
		return transport(request, form)
	})
	d.cfg.HuangjuAPIURL = "https://api.huangju.test"
	d.cfg.HuangjuURL = "https://huangju.test"
	d.cfg.YeguoURL = "https://yeguo.test"
	return d
}

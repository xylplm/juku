package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLibraryUpdateTargetsIndividualHuangguoEntrypoints(t *testing.T) {
	for _, source := range []string{"cloudfront", sourceHuangguoAI, sourceHuangguoVideo} {
		t.Run(source, func(t *testing.T) {
			started := make(chan struct{}, 8)
			downloader := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
				if request.Context().Err() == nil {
					select {
					case started <- struct{}{}:
					default:
					}
				}
				<-request.Context().Done()
				return nil, request.Context().Err()
			})
			app := &UIApp{downloader: downloader, cfg: downloader.cfg, libraryAttempted: true, librarySources: map[string]librarySourceState{}}
			for _, provider := range []string{"cloudfront", sourceHuangguoAI, sourceHuangguoVideo, sourceHuangdou, sourceHongguo} {
				app.librarySources[provider] = librarySourceState{Status: "cached", Count: 7}
			}
			t.Cleanup(func() {
				app.mu.Lock()
				done, cancel := app.libraryLoading, app.libraryCancel
				app.mu.Unlock()
				if cancel != nil {
					cancel()
				}
				if done != nil {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("source update did not stop")
					}
				}
				app.stopSortMetadata()
			})
			read := func() struct {
				Accepted bool   `json:"updateAccepted"`
				Loading  bool   `json:"loading"`
				Source   string `json:"loadingSource"`
			} {
				t.Helper()
				request := httptest.NewRequest(http.MethodGet, "/api/ui/dramas?update=1&source="+source, nil)
				request = request.WithContext(withSourceScope(context.Background(), accountRecord{Sources: []string{"huangguo"}}))
				writer := httptest.NewRecorder()
				app.handleDramas(writer, request)
				var result struct {
					Accepted bool   `json:"updateAccepted"`
					Loading  bool   `json:"loading"`
					Source   string `json:"loadingSource"`
				}
				if writer.Code != http.StatusOK || json.Unmarshal(writer.Body.Bytes(), &result) != nil {
					t.Fatalf("individual source update rejected: %d %s", writer.Code, writer.Body.String())
				}
				return result
			}
			if result := read(); !result.Accepted || !result.Loading || result.Source != source {
				t.Fatalf("update was not scoped to the requested entrypoint: %+v", result)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("source update never dispatched a textual request")
			}
			if result := read(); result.Accepted || !result.Loading || result.Source != source {
				t.Fatalf("busy update was reported as newly accepted: %+v", result)
			}
			app.mu.Lock()
			defer app.mu.Unlock()
			for provider, state := range app.librarySources {
				want := "cached"
				if provider == source {
					want = "loading"
				}
				if state.Status != want || state.Count != 7 {
					t.Errorf("update changed another entrypoint: %s %+v", provider, state)
				}
			}
		})
	}
}

func TestIndividualSourceUpdatesRequireSourcePermission(t *testing.T) {
	var calls atomic.Int32
	downloader := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return rankingHTTPResponse(request, http.StatusServiceUnavailable, "text fixture"), nil
	})
	app := &UIApp{downloader: downloader, cfg: downloader.cfg, libraryAttempted: true}
	for _, source := range []string{"cloudfront", sourceHuangguoAI, sourceHuangguoVideo, sourceHuangdou} {
		request := httptest.NewRequest(http.MethodGet, "/api/ui/dramas?update=1&source="+source, nil)
		request = request.WithContext(withSourceScope(context.Background(), accountRecord{Sources: []string{sourceHongguo}}))
		writer := httptest.NewRecorder()
		app.handleDramas(writer, request)
		if writer.Code != http.StatusForbidden {
			t.Errorf("restricted source %s returned %d instead of 403", source, writer.Code)
		}
	}
	if calls.Load() != 0 || app.libraryLoading != nil {
		t.Fatal("unauthorized source update triggered background work")
	}
}

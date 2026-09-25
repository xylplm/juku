package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type providerSearchResult struct {
	source  string
	items   []Drama
	more    bool
	warning string
	err     error
	done    bool
}

func (a *UIApp) handleProviderLibrarySearch(w http.ResponseWriter, r *http.Request, keyword, source string) {
	if source != "all" && !paginatedProvider(source) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该站源不支持联网搜索"})
		return
	}
	if source != "all" && !requireSource(w, r.Context(), source) {
		return
	}
	pages := map[string]int{}
	if raw := r.URL.Query().Get("pages"); raw != "" {
		if len(raw) > 1024 || json.Unmarshal([]byte(raw), &pages) != nil || len(pages) == 0 || len(pages) > 4 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索续页参数无效"})
			return
		}
		for provider, page := range pages {
			if provider != sourceHongguo && !paginatedProvider(provider) || source != "all" && provider != source || page < 1 || page > 1000000 || provider == sourceHongguo && page != 1 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "搜索续页参数无效"})
				return
			}
			if !requireSource(w, r.Context(), provider) {
				return
			}
		}
	} else {
		for _, provider := range []string{sourceHongguo, sourceHuangju, sourceYeguo, sourceDSD} {
			if (source == "all" || source == provider) && sourceAllowed(r.Context(), provider) {
				pages[provider] = 1
			}
		}
	}
	if len(pages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "当前账号没有可联网搜索的站源"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	results := make(chan providerSearchResult, 16)
	send := func(result providerSearchResult) {
		select {
		case results <- result:
		case <-ctx.Done():
		}
	}
	for provider, page := range pages {
		go func(provider string, page int) {
			result := providerSearchResult{source: provider, done: true}
			if provider == sourceHongguo {
				entry, err := a.downloader.searchHongguoDramasProgress(ctx, keyword, func(entry hongguoSearchEntry) {
					send(providerSearchResult{source: provider, items: entry.Dramas})
				})
				result.items, result.warning, result.err = entry.Dramas, entry.Warning, err
			} else {
				result.items, result.more, result.err = a.downloader.fetchProviderCatalogPage(ctx, provider, page, "", keyword)
			}
			send(result)
		}(provider, page)
	}
	stream := r.URL.Query().Get("stream") == "1"
	encoder, controller := json.NewEncoder(w), http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	if stream {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	items := map[string]Drama{}
	nextPages := map[string]int{}
	warnings := map[string]string{}
	finished := map[string]bool{}
	imported := false
	defer func() {
		if imported {
			a.persistLibrary()
		}
	}()
	write := func(done bool) bool {
		if r.Context().Err() != nil {
			return false
		}
		dramas := make([]Drama, 0, len(items))
		for _, drama := range items {
			dramas = append(dramas, drama)
		}
		sort.SliceStable(dramas, func(i, j int) bool { return dramas[i].ID < dramas[j].ID })
		sortHongguoSearchDramas(dramas, keyword)
		var messages []string
		for provider, warning := range warnings {
			if warning != "" {
				name := provider
				for _, choice := range accountSourceChoices {
					if choice.ID == provider {
						name = choice.Name
						break
					}
				}
				messages = append(messages, name+"："+warning)
			}
		}
		sort.Strings(messages)
		if done && imported {
			a.persistLibrary()
			imported = false
		}
		a.mu.Lock()
		saved := a.librarySaved && !a.libraryDirty
		a.mu.Unlock()
		payload := map[string]any{"query": keyword, "source": source, "data": dramas, "total": len(dramas), "done": done,
			"limited": !done || len(messages) > 0 || len(nextPages) > 0, "warning": strings.Join(messages, "；"),
			"hasMore": len(nextPages) > 0, "nextPages": nextPages, "saved": saved}
		if !stream {
			if done {
				writeJSON(w, http.StatusOK, payload)
			}
			return true
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if encoder.Encode(payload) != nil || controller.Flush() != nil {
			cancel()
			return false
		}
		_ = controller.SetWriteDeadline(time.Time{})
		return true
	}
	for len(finished) < len(pages) {
		select {
		case <-ctx.Done():
			if errors.Is(r.Context().Err(), context.Canceled) {
				return
			}
			for provider, page := range pages {
				if !finished[provider] {
					warnings[provider] = "搜索已到时限，已保留收到的结果，可继续重试"
					nextPages[provider] = page
				}
			}
			write(true)
			return
		case result := <-results:
			valid := make([]Drama, 0, len(result.items))
			for _, drama := range result.items {
				provider, _, ok := splitProviderDramaID(drama.ID)
				if ok && provider == result.source && dramaAllowed(r.Context(), drama.ID, drama.Source) {
					valid = append(valid, drama)
				}
			}
			for _, drama := range a.importSourceSearchDramas(result.source, valid) {
				items[drama.ID] = drama
				imported = true
			}
			if result.done {
				finished[result.source] = true
				warnings[result.source] = result.warning
				if result.err != nil {
					warnings[result.source] = "暂未完成，已保留有效结果，可继续重试"
					nextPages[result.source] = pages[result.source]
				} else if result.more {
					nextPages[result.source] = pages[result.source] + 1
				}
			}
			if !write(len(finished) == len(pages)) {
				return
			}
		}
	}
}

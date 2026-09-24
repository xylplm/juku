package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type providerCatalogCursor struct {
	Page        int       `json:"page"`
	Initialized bool      `json:"initialized"`
	Exhausted   bool      `json:"exhausted"`
	Signature   string    `json:"signature,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type providerCatalogFeed struct {
	loading sync.Mutex
	cursor  providerCatalogCursor
}

type providerCatalogStore struct {
	mu    sync.Mutex
	feeds map[string]*providerCatalogFeed
}

func providerCatalogKey(source, category string) string {
	source = canonicalProviderSource(source)
	category = strings.TrimSpace(category)
	if category != "" {
		return source + "|" + category
	}
	return source
}

func providerCatalogSource(key string) string {
	source, _, _ := strings.Cut(key, "|")
	return canonicalProviderSource(source)
}

func cloneProviderCatalog(cursors map[string]providerCatalogCursor) map[string]providerCatalogCursor {
	cloned := make(map[string]providerCatalogCursor, len(cursors))
	for key, cursor := range cursors {
		source, category, _ := strings.Cut(key, "|")
		source = canonicalProviderSource(source)
		if paginatedProvider(source) && validProviderCategory(source, category) && cursor.Initialized && cursor.Page >= 1 && cursor.Page <= 1000000 && len(cursor.Signature) <= 64 {
			cloned[providerCatalogKey(source, category)] = cursor
		}
	}
	return cloned
}

func (d *Downloader) providerCatalogSnapshot() map[string]providerCatalogCursor {
	d.providerCatalog.mu.Lock()
	defer d.providerCatalog.mu.Unlock()
	cursors := map[string]providerCatalogCursor{}
	for source, feed := range d.providerCatalog.feeds {
		if feed.cursor.Initialized {
			cursors[source] = feed.cursor
		}
	}
	return cursors
}

func (d *Downloader) restoreProviderCatalog(cursors map[string]providerCatalogCursor) {
	d.providerCatalog.mu.Lock()
	defer d.providerCatalog.mu.Unlock()
	if d.providerCatalog.feeds == nil {
		d.providerCatalog.feeds = map[string]*providerCatalogFeed{}
	}
	for source, cursor := range cloneProviderCatalog(cursors) {
		if d.providerCatalog.feeds[source] == nil {
			d.providerCatalog.feeds[source] = &providerCatalogFeed{cursor: cursor}
		}
	}
}

func (d *Downloader) fetchPagedProviderDramas(ctx context.Context, source string, categories ...string) ([]Drama, error) {
	category := ""
	if len(categories) > 0 {
		category = strings.TrimSpace(categories[0])
	}
	source = canonicalProviderSource(source)
	if !paginatedProvider(source) || !validProviderCategory(source, category) || !sourceAllowed(ctx, source) {
		return nil, errors.New("该站源不可用")
	}
	cacheKey := providerCatalogKey(source, category)
	d.providerCatalog.mu.Lock()
	if d.providerCatalog.feeds == nil {
		d.providerCatalog.feeds = map[string]*providerCatalogFeed{}
	}
	feed := d.providerCatalog.feeds[cacheKey]
	if feed == nil {
		feed = &providerCatalogFeed{}
		d.providerCatalog.feeds[cacheKey] = feed
	}
	d.providerCatalog.mu.Unlock()
	feed.loading.Lock()
	defer feed.loading.Unlock()
	d.providerCatalog.mu.Lock()
	cursor := feed.cursor
	d.providerCatalog.mu.Unlock()
	more, _ := ctx.Value(libraryMoreKey{}).(bool)
	update, _ := ctx.Value(libraryUpdateKey{}).(bool)
	pageLimit := 1
	if update {
		pageLimit = d.cfg.MaxPagesPerSort
		if pageLimit <= 0 {
			pageLimit = defaultConfig().MaxPagesPerSort
		}
	}
	var pages []int
	if more {
		if cursor.Initialized && !cursor.Exhausted {
			pages = append(pages, cursor.Page+1)
		}
	} else {
		pages = append(pages, 1)
		if cursor.Initialized && !cursor.Exhausted {
			if cursor.Page >= 1000000 {
				return nil, errors.New("站源分页已达到接口范围，已保留目录位置")
			}
			if update {
				for page := cursor.Page + 1; page <= 1000000 && len(pages) < pageLimit; page++ {
					pages = append(pages, page)
				}
			} else {
				pages = append(pages, cursor.Page+1)
			}
		} else if update {
			for page := 2; page <= 1000000 && len(pages) < pageLimit; page++ {
				pages = append(pages, page)
			}
		}
	}
	items := []Drama{}
	var failures []error
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return items, errors.Join(append(failures, err)...)
		}
		rows, hasMore, err := d.fetchProviderCatalogPage(ctx, source, page, category, "")
		items = mergeSourceDramas(items, rows, nil, source)
		if err != nil {
			failures = append(failures, err)
			reportLibraryProgress(ctx, source, rows, nil, false)
			break
		}
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		sort.Strings(ids)
		digest := sha256.Sum256([]byte(strings.Join(ids, "\n")))
		signature := hex.EncodeToString(digest[:])
		if hasMore && len(rows) == 0 {
			failures = append(failures, errors.New("站源分页未前进，已保留上次位置"))
			break
		}
		progressCursor := cursor
		if !cursor.Initialized || page > cursor.Page {
			progressCursor = providerCatalogCursor{Page: page, Initialized: true, Exhausted: !hasMore, Signature: signature, UpdatedAt: time.Now()}
			d.providerCatalog.mu.Lock()
			feed.cursor = progressCursor
			d.providerCatalog.mu.Unlock()
		}
		if err := reportLibraryProgress(ctx, source, rows, nil, false); err != nil {
			failures = append(failures, fmt.Errorf("站源分页缓存保存失败，已暂停继续加载: %w", err))
			break
		}
		cursor = progressCursor
		if !hasMore {
			break
		}
	}
	return items, errors.Join(failures...)
}

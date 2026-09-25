package app

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

const (
	sourceHuangju = "huangju"
	sourceYeguo   = "yeguo"
	sourceDSD     = "dsd"
)

var webProviderNumericID = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)

type providerCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func providerValueText(value any) string {
	return mapString(map[string]any{"value": value}, "value")
}

func webProviderInteger(value any, maximum int) (int, bool) {
	number, err := strconv.Atoi(providerValueText(value))
	return number, err == nil && number >= 0 && number <= maximum
}

func validProviderCategory(source, category string) bool {
	if category == "" {
		return true
	}
	if len(category) > 128 || strings.TrimSpace(category) != category || strings.ContainsAny(category, "|/\\\x00\r\n") {
		return false
	}
	switch source {
	case sourceHongguo:
		for _, genre := range hongguoAppGenres {
			if category == genre.key {
				return true
			}
		}
		return false
	case sourceHuangju:
		return category == huangjuNewestCategory || validHuangjuID(category) && !strings.HasPrefix(category, "@")
	case sourceYeguo:
		return validYeguoCategory(category)
	case sourceDSD:
		return webProviderNumericID.MatchString(category)
	}
	return false
}

func hongguoProviderCategories() []providerCategory {
	categories := []providerCategory{{Name: "全部"}}
	for _, genre := range hongguoAppGenres {
		categories = append(categories, providerCategory{ID: genre.key, Name: genre.name})
	}
	return categories
}

func (d *Downloader) fetchProviderCategories(ctx context.Context, source string) ([]providerCategory, error) {
	if !sourceAllowed(ctx, source) {
		return nil, errors.New("当前账号无权访问该站源")
	}
	switch canonicalProviderSource(source) {
	case sourceHongguo:
		return hongguoProviderCategories(), nil
	case sourceHuangju:
		return d.fetchHuangjuCategories(ctx)
	case sourceYeguo:
		return d.fetchYeguoCategories(ctx)
	case sourceDSD:
		return d.fetchDSDCategories(ctx, false)
	}
	return nil, errors.New("该站源暂未提供实时分类")
}

func paginatedProvider(source string) bool {
	return source == sourceHuangju || source == sourceYeguo || source == sourceDSD
}

func (d *Downloader) fetchProviderCatalogPage(ctx context.Context, source string, page int, category, query string) ([]Drama, bool, error) {
	if !sourceAllowed(ctx, source) {
		return nil, false, errors.New("当前账号无权访问该站源")
	}
	switch source {
	case sourceHuangju:
		return d.fetchHuangjuCatalogPage(ctx, page, category, query)
	case sourceYeguo:
		return d.fetchYeguoCatalogPage(ctx, page, category, query)
	case sourceDSD:
		return d.fetchDSDCatalogPage(ctx, page, category, query)
	}
	return nil, false, errors.New("该站源不支持在线分页查询")
}

func (d *Downloader) fetchProviderDetail(ctx context.Context, source, id string) (Drama, []Chapter, error) {
	if !sourceAllowed(ctx, source) {
		return Drama{}, nil, errors.New("当前账号无权访问该站源")
	}
	switch source {
	case sourceHuangju:
		return d.fetchHuangjuDetail(ctx, id)
	case sourceYeguo:
		return d.fetchYeguoDetail(ctx, id)
	case sourceDSD:
		return d.fetchDSDDetail(ctx, id)
	}
	return Drama{}, nil, errors.New("该站源不支持详情查询")
}

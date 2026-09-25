package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

func (d *Downloader) fetchProviderPage(ctx context.Context, address, referer, agent string) (*html.Node, string, error) {
	if agent != "" {
		ctx = context.WithValue(ctx, providerTextUserAgentKey{}, agent)
	}
	body, actual, err := d.fetchProviderTextURL(ctx, address, referer)
	if err != nil {
		return nil, "", err
	}
	if len(body) > 4<<20 {
		return nil, "", errors.New("站源页面过大")
	}
	address = actual
	document, err := html.Parse(strings.NewReader(body))
	return document, address, err
}

func (d *Downloader) prepareWebProviderMedia(ctx context.Context, media providerMedia, sourceName string) (providerMedia, error) {
	address, err := url.Parse(media.URL)
	if err != nil || !isProviderHTTPMediaURL(media.URL) || address.User != nil {
		return providerMedia{}, fmt.Errorf("%s未返回有效播放地址，请刷新详情后重试", sourceName)
	}
	if strings.HasSuffix(strings.ToLower(address.Path), ".m3u8") {
		if media.credentials != nil {
			ctx = providerMediaContext(ctx, media.credentials)
		}
		media.Playlist, media.URL, err = d.fetchMediaPlaylist(ctx, media.URL, media.Referer)
		if err != nil {
			return providerMedia{}, fmt.Errorf("获取%s播放列表失败：%w", sourceName, err)
		}
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(media.Playlist, "\ufeff")), "#EXTM3U") {
			return providerMedia{}, fmt.Errorf("%s播放列表无效，请重新解析播放", sourceName)
		}
		if duration := m3u8Duration(media.Playlist); duration > 0 {
			media.Duration = duration
		}
	}
	return media, nil
}

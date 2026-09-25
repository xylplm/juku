package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	huangjuBaseURL        = "https://huangju.net"
	huangjuAPIBaseURL     = "https://api.huangju.net"
	huangjuSeedConfigURL  = "https://seed.huangju.net/seed/config"
	huangjuSeedConfigAlt  = "https://seed.yanyushorttv.bond/seed/config"
	huangjuUserAgent      = "Mozilla/5.0 (Linux; Android 11; Pixel 5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.91 Mobile Safari/537.36"
	huangjuPoolRetryLimit = 3
)

var (
	errHuangjuGuestExpired = errors.New("剧果访客授权已失效，请重试")
	huangjuSeedPublicKey   = ed25519.PublicKey{0xe2, 0x32, 0x99, 0x37, 0xab, 0x97, 0x60, 0x01, 0x64, 0x48, 0x64, 0xbf, 0xde, 0x90, 0x49, 0x92, 0xf7, 0x24, 0x43, 0xc6, 0x3f, 0x30, 0xf9, 0x32, 0xe3, 0x71, 0xc5, 0xf8, 0x2a, 0xea, 0xae, 0xf8}
)

type huangjuGuestCall struct {
	done  chan struct{}
	token string
	err   error
}

type huangjuAPIClient struct {
	downloader *Downloader
	base       string
	site       string
	configured bool
	mu         sync.Mutex
	deviceID   string
	token      string
	pending    *huangjuGuestCall
	pool       []string
	poolIndex  int
	poolLoaded time.Time
}

type huangjuAPIResponse struct {
	value   any
	headers http.Header
}

func (d *Downloader) huangjuClient() *huangjuAPIClient {
	d.huangjuOnce.Do(func() {
		d.huangju = &huangjuAPIClient{
			downloader: d,
			base:       strings.TrimRight(firstNonEmpty(d.cfg.HuangjuAPIURL, huangjuAPIBaseURL), "/"),
			site:       d.providerBaseURL(sourceHuangju),
			configured: strings.TrimSpace(d.cfg.HuangjuAPIURL) != "",
		}
	})
	return d.huangju
}

type huangjuSeedEnvelope struct {
	Alg       string `json:"alg"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type huangjuSeedPayload struct {
	API      []string `json:"api"`
	IssuedAt int64    `json:"issuedAt"`
	Seed     []string `json:"seed"`
	Track    []string `json:"track"`
	Version  int      `json:"version"`
	Video    []string `json:"video"`
}

func huangjuValidBase(raw string) (string, bool) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	address, err := url.Parse(base)
	if err != nil || !isProviderHTTPMediaURL(base) || address.User != nil || address.RawQuery != "" || address.Fragment != "" || address.Path != "" {
		return "", false
	}
	host := strings.ToLower(address.Hostname())
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.ContainsAny(host, "_%\\") {
		return "", false
	}
	return base, true
}

func huangjuSeedEndpoints() []string {
	return []string{huangjuSeedConfigURL, huangjuSeedConfigAlt}
}

func decodeHuangjuSeed(body []byte) (huangjuSeedPayload, error) {
	var envelope huangjuSeedEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Alg != "ed25519" || envelope.Payload == "" || envelope.Signature == "" {
		return huangjuSeedPayload{}, errors.New("剧果线路配置格式无效")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return huangjuSeedPayload{}, errors.New("剧果线路配置格式无效")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(huangjuSeedPublicKey, payload, signature) {
		return huangjuSeedPayload{}, errors.New("剧果线路配置签名无效")
	}
	var decoded huangjuSeedPayload
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if decoder.Decode(&decoded) != nil || decoder.Decode(new(any)) != io.EOF || decoded.Version < 1 || len(decoded.API) > 12 || len(decoded.Seed) > 12 {
		return huangjuSeedPayload{}, errors.New("剧果线路配置内容无效")
	}
	return decoded, nil
}

func (client *huangjuAPIClient) fetchSeedPool(ctx context.Context) ([]string, error) {
	var lastErr error
	for _, endpoint := range huangjuSeedEndpoints() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		request.Header.Set("User-Agent", huangjuUserAgent)
		request.Header.Set("Accept", "application/json")
		response, err := client.doRequest(ctx, request, 256<<10)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
		response.Body.Close()
		if readErr != nil || len(body) > 256<<10 {
			lastErr = errors.New("剧果线路配置读取失败")
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || catalogResponseBlockReason(response, body) != "" {
			lastErr = client.downloader.catalogResponseError(request, response, body)
			continue
		}
		seed, err := decodeHuangjuSeed(body)
		if err != nil {
			lastErr = err
			continue
		}
		var bases []string
		seen := map[string]bool{}
		for _, raw := range seed.API {
			if base, ok := huangjuValidBase(raw); ok && !seen[base] {
				seen[base] = true
				bases = append(bases, base)
			}
		}
		if len(bases) > 0 {
			return bases, nil
		}
		lastErr = errors.New("剧果线路配置没有可用接口")
	}
	if lastErr == nil {
		lastErr = errors.New("剧果线路配置不可用")
	}
	return nil, lastErr
}

func (client *huangjuAPIClient) apiPool(ctx context.Context) []string {
	client.mu.Lock()
	if client.configured {
		base := client.base
		client.mu.Unlock()
		return []string{base}
	}
	if len(client.pool) > 0 && time.Since(client.poolLoaded) >= 0 && time.Since(client.poolLoaded) < 30*time.Minute {
		pool := append([]string(nil), client.pool...)
		index := client.poolIndex
		client.mu.Unlock()
		if index > 0 && index < len(pool) {
			return append(append([]string(nil), pool[index:]...), pool[:index]...)
		}
		return pool
	}
	client.mu.Unlock()
	pool, err := client.fetchSeedPool(ctx)
	if err != nil || len(pool) == 0 {
		pool = []string{client.base, "https://api.yanyushorttv.cc", "https://api.yanyushorttv.top"}
	}
	seen := map[string]bool{}
	compact := make([]string, 0, len(pool)+1)
	for _, raw := range append([]string{client.base}, pool...) {
		if base, ok := huangjuValidBase(raw); ok && !seen[base] {
			seen[base] = true
			compact = append(compact, base)
		}
	}
	client.mu.Lock()
	client.pool = compact
	client.poolLoaded = time.Now()
	index := client.poolIndex
	client.mu.Unlock()
	if index > 0 && index < len(compact) {
		return append(append([]string(nil), compact[index:]...), compact[:index]...)
	}
	return compact
}

func (client *huangjuAPIClient) reportAPIResult(base string, err error) {
	if client.configured || base == "" {
		return
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	for index, candidate := range client.pool {
		if candidate != base {
			continue
		}
		if err == nil {
			client.poolIndex = index
		} else if len(client.pool) > 1 && index == client.poolIndex {
			client.poolIndex = (index + 1) % len(client.pool)
		}
		return
	}
}

func huangjuRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var backoff *requestBackoff
	if errors.As(err, &backoff) {
		return false
	}
	var status *httpStatusError
	if errors.As(err, &status) && status.status >= 400 && status.status < 500 && status.status != http.StatusRequestTimeout && status.status != http.StatusTooManyRequests {
		return false
	}
	return true
}

func (client *huangjuAPIClient) doRequest(ctx context.Context, request *http.Request, limit int64) (*http.Response, error) {
	d := client.downloader
	if d.limiter != nil {
		release, err := d.limiter.acquire(ctx, request)
		if err != nil {
			return nil, err
		}
		response, err := client.roundTrip(request)
		if err != nil {
			release()
			return nil, err
		}
		response.Body = &limitedResponseBody{ReadCloser: response.Body, release: release}
		return response, nil
	}
	return client.roundTrip(request)
}

func (client *huangjuAPIClient) roundTrip(request *http.Request) (*http.Response, error) {
	transport := *client.downloader.client
	transport.Jar = nil
	base := request.URL
	previousRedirect := transport.CheckRedirect
	transport.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || providerMediaOrigin(next.URL) != providerMediaOrigin(base) {
			return errors.New("剧果接口重定向地址异常")
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		return nil
	}
	return transport.Do(request)
}

func (client *huangjuAPIClient) guestToken(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	client.mu.Lock()
	if client.token != "" {
		token := client.token
		client.mu.Unlock()
		return token, nil
	}
	if pending := client.pending; pending != nil {
		client.mu.Unlock()
		select {
		case <-pending.done:
			if (errors.Is(pending.err, context.Canceled) || errors.Is(pending.err, context.DeadlineExceeded)) && ctx.Err() == nil {
				return client.guestToken(ctx)
			}
			return pending.token, pending.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if client.deviceID == "" {
		var identifier [16]byte
		if _, err := rand.Read(identifier[:]); err != nil {
			client.mu.Unlock()
			return "", errors.New("无法初始化剧果访客会话")
		}
		identifier[6] = identifier[6]&0x0f | 0x40
		identifier[8] = identifier[8]&0x3f | 0x80
		client.deviceID = fmt.Sprintf("%x-%x-%x-%x-%x", identifier[:4], identifier[4:6], identifier[6:8], identifier[8:10], identifier[10:])
	}
	deviceID := client.deviceID
	pending := &huangjuGuestCall{done: make(chan struct{})}
	client.pending = pending
	client.mu.Unlock()

	body, _ := json.Marshal(map[string]string{"deviceId": deviceID})
	response, err := client.request(ctx, http.MethodPost, "/auth/guest", nil, body, "", false, 64<<10)
	token := ""
	if err == nil {
		row, _ := response.value.(map[string]any)
		token, _ = row["token"].(string)
		if token == "" {
			data, _ := row["data"].(map[string]any)
			token, _ = data["token"].(string)
		}
		if token == "" || len(token) > 8192 || strings.IndexFunc(token, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			token = ""
			err = errors.New("剧果未返回有效的访客授权")
		}
	}
	client.mu.Lock()
	if err == nil {
		client.token = token
	}
	pending.token, pending.err = token, err
	client.pending = nil
	close(pending.done)
	client.mu.Unlock()
	return token, err
}

func (client *huangjuAPIClient) get(ctx context.Context, route string, query url.Values, limit int64) (huangjuAPIResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.guestToken(ctx)
		if err != nil {
			return huangjuAPIResponse{}, err
		}
		response, err := client.request(ctx, http.MethodGet, route, query, nil, token, attempt == 0, limit)
		if attempt == 0 && errors.Is(err, errHuangjuGuestExpired) {
			client.mu.Lock()
			if client.token == token {
				client.token = ""
			}
			client.mu.Unlock()
			continue
		}
		return response, err
	}
	return huangjuAPIResponse{}, errHuangjuGuestExpired
}

func (client *huangjuAPIClient) request(ctx context.Context, method, route string, query url.Values, body []byte, token string, retryAuth bool, limit int64) (huangjuAPIResponse, error) {
	timeout := 15 * time.Second
	if background, _ := ctx.Value(backgroundCatalogKey{}).(bool); background {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pools := client.apiPool(ctx)
	if len(pools) == 0 {
		return huangjuAPIResponse{}, errors.New("剧果接口地址无效")
	}
	tries := len(pools)
	if tries < huangjuPoolRetryLimit {
		tries = huangjuPoolRetryLimit
	}
	if tries > len(pools)*2 {
		tries = len(pools) * 2
	}
	if client.configured {
		tries = 1
	}
	var lastErr error
	for attempt := 0; attempt < tries; attempt++ {
		if err := ctx.Err(); err != nil {
			return huangjuAPIResponse{}, err
		}
		baseText := pools[attempt%len(pools)]
		base, valid := huangjuValidBase(baseText)
		if !valid {
			return huangjuAPIResponse{}, errors.New("剧果接口地址无效")
		}
		address := base + route
		if len(query) > 0 {
			address += "?" + query.Encode()
		}
		request, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
		if err != nil {
			return huangjuAPIResponse{}, errors.New("无法创建剧果请求")
		}
		request.Header.Set("User-Agent", huangjuUserAgent)
		request.Header.Set("Accept", "application/json, text/plain, */*")
		request.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
		request.Header.Set("Referer", client.site+"/")
		request.Header.Set("Origin", client.site)
		if len(body) > 0 {
			request.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.doRequest(ctx, request, 0)
		if err != nil {
			lastErr = fmt.Errorf("剧果连接失败：%w", publicError(err))
			client.reportAPIResult(base, lastErr)
			client.downloader.client.CloseIdleConnections()
			if !huangjuRetryableError(lastErr) {
				return huangjuAPIResponse{}, lastErr
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		closeErr := response.Body.Close()
		blocked := catalogResponseBlockReason(response, data) != ""
		authFailed := response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden
		if client.downloader.limiter != nil {
			client.downloader.limiter.observe(request, response)
		}
		if retryAuth && token != "" && authFailed && readErr == nil && closeErr == nil && int64(len(data)) <= limit && !blocked && json.Valid(data) {
			client.reportAPIResult(base, nil)
			return huangjuAPIResponse{}, errHuangjuGuestExpired
		}
		if readErr != nil || closeErr != nil || int64(len(data)) > limit {
			lastErr = errors.New("剧果返回的数据过大或读取失败")
			client.reportAPIResult(base, lastErr)
			if huangjuRetryableError(lastErr) {
				continue
			}
			return huangjuAPIResponse{}, lastErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || blocked {
			lastErr = client.downloader.catalogResponseError(request, response, data)
			client.reportAPIResult(base, lastErr)
			if huangjuRetryableError(lastErr) {
				continue
			}
			return huangjuAPIResponse{}, lastErr
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if decoder.Decode(&decoded) != nil || decoder.Decode(new(any)) != io.EOF {
			lastErr = errors.New("剧果返回的数据格式无效")
			client.reportAPIResult(base, lastErr)
			return huangjuAPIResponse{}, lastErr
		}
		client.reportAPIResult(base, nil)
		return huangjuAPIResponse{value: decoded, headers: response.Header.Clone()}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("剧果接口暂不可用")
	}
	return huangjuAPIResponse{}, lastErr
}

func huangjuPayload(value any) any {
	if row, ok := value.(map[string]any); ok && row["data"] != nil && row["id"] == nil && row["items"] == nil && row["url"] == nil {
		return row["data"]
	}
	return value
}

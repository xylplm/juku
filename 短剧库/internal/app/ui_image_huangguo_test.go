package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func syntheticHuangguoCover(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	plain := output.Bytes()
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	encrypted := make([]byte, len(padded))
	block, err := aes.NewCipher([]byte("f5d965df75336270"))
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, []byte("97b60394abc2fbe1")).CryptBlocks(encrypted, padded)
	return plain, encrypted
}

func TestHuangguoImageProxyAcceptsKnownCDNAliases(t *testing.T) {
	plain, encrypted := syntheticHuangguoCover(t)
	for _, host := range []string{"pic.zdmhyg.cn", "pic.tuafjz.cn", "PIC.TUAFJZ.CN", "new-covers.example.org"} {
		for _, payload := range []struct {
			name string
			data []byte
		}{{"plain", plain}, {"encrypted", encrypted}} {
			t.Run(host+"/"+payload.name, func(t *testing.T) {
				address := "https://" + host + "/upload/synthetic.jpg?auth_key=fixture-0-0-test&signature=%2F%2b%3D"
				var calls atomic.Int32
				downloader := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
					calls.Add(1)
					if request.URL.String() != address || request.Header.Get("Referer") != "https://huangguoai.com/" || request.Header.Get("User-Agent") == "" {
						t.Error("cover request changed the signed address or omitted source headers")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(payload.data)), Request: request}, nil
				})
				app := &UIApp{downloader: downloader, cfg: downloader.cfg, dramas: []Drama{{ID: "huangguoai:501", Source: sourceHuangguoAI, CoverURL: address}}}
				for range 2 {
					request := httptest.NewRequest(http.MethodGet, "/api/ui/image?url="+url.QueryEscape(address), nil)
					request = request.WithContext(withSourceScope(context.Background(), accountRecord{Sources: []string{"huangguo"}}))
					writer := httptest.NewRecorder()
					app.handleImage(writer, request)
					if writer.Code != http.StatusOK || !bytes.Equal(writer.Body.Bytes(), plain) || writer.Header().Get("Content-Type") != "image/png" {
						t.Fatalf("known Huangguo cover was rejected or decoded incorrectly: status=%d body=%q", writer.Code, writer.Body.String())
					}
					if writer.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("restricted-source cover became publicly cacheable")
					}
				}
				if calls.Load() != 1 {
					t.Fatal("repeat cover requests bypassed the cache", calls.Load())
				}
				if host != "new-covers.example.org" && !protectedCDNHost(host) {
					t.Fatal("known cover CDN lost its existing DNS and proxy fallback")
				}
			})
		}
	}
}

func TestHuangguoImageProxyValidatesRedirects(t *testing.T) {
	plain, _ := syntheticHuangguoCover(t)
	for _, test := range []struct {
		target string
		status int
		calls  int32
	}{
		{"https://pic.tuafjz.cn/upload/synthetic.jpg?auth_key=fixture", http.StatusOK, 2},
		{"https://new-covers.example.org/cover", http.StatusOK, 2},
		{"https://pic.tuafjz.cn.example.invalid/cover", http.StatusBadGateway, 1},
		{"https://127.0.0.1/cover", http.StatusBadGateway, 1},
	} {
		t.Run(test.target, func(t *testing.T) {
			var calls atomic.Int32
			downloader := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
				count := calls.Add(1)
				if count == 1 {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {test.target}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				if test.calls != 2 || request.URL.String() != test.target || request.Header.Get("Referer") != "https://huangguoai.com/" {
					t.Error("redirect requested a forbidden host or lost the correct referer")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(plain)), Request: request}, nil
			})
			app := &UIApp{downloader: downloader, cfg: downloader.cfg, dramas: []Drama{{ID: "huangguoai:501", Source: sourceHuangguoAI, CoverURL: "https://pic.zdmhyg.cn/upload/synthetic.jpg"}}}
			request := httptest.NewRequest(http.MethodGet, "/api/ui/image?url="+url.QueryEscape("https://pic.zdmhyg.cn/upload/synthetic.jpg"), nil)
			writer := httptest.NewRecorder()
			app.handleImage(writer, request)
			if writer.Code != test.status || calls.Load() != test.calls {
				t.Fatalf("unexpected redirect result: status=%d calls=%d", writer.Code, calls.Load())
			}
		})
	}
}

func TestHuangguoImageProxyRetainsURLAndSourceRestrictions(t *testing.T) {
	var calls atomic.Int32
	downloader := rankingTestDownloader(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return rankingHTTPResponse(request, http.StatusOK, "forbidden request"), nil
	})
	address := "https://pic.tuafjz.cn/upload/synthetic.jpg?auth_key=fixture"
	app := &UIApp{downloader: downloader, cfg: downloader.cfg, dramas: []Drama{{ID: "huangguoai:501", Source: sourceHuangguoAI, CoverURL: address}}}
	for _, target := range []string{
		"https://pic.tuafjz.cn.example.invalid/cover",
		"https://pic.tuafjz.cn@127.0.0.1/cover", "https://user@pic.tuafjz.cn/cover", "https://pic.tuafjz.cn:8443/cover",
		"http://pic.tuafjz.cn/cover", "https://127.0.0.1/cover", "https://[::1]/cover",
	} {
		writer := httptest.NewRecorder()
		app.handleImage(writer, httptest.NewRequest(http.MethodGet, "/api/ui/image?url="+url.QueryEscape(target), nil))
		if writer.Code != http.StatusBadRequest {
			t.Errorf("unsupported image URL was accepted: %s (%d)", target, writer.Code)
		}
	}
	for _, target := range []string{"https://tuafjz.cn/cover", "https://other.tuafjz.cn/cover", "https://new-covers.example.org/unregistered"} {
		writer := httptest.NewRecorder()
		app.handleImage(writer, httptest.NewRequest(http.MethodGet, "/api/ui/image?url="+url.QueryEscape(target), nil))
		if writer.Code != http.StatusForbidden {
			t.Errorf("unregistered cover was accepted: %s (%d)", target, writer.Code)
		}
	}
	writer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/ui/image?url="+url.QueryEscape(address), nil)
	request = request.WithContext(withSourceScope(context.Background(), accountRecord{Sources: []string{sourceHongguo}}))
	app.handleImage(writer, request)
	if writer.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("source-restricted request was not denied before fetching: status=%d calls=%d", writer.Code, calls.Load())
	}
}

func TestCoverRefererPreservesHuangdouMirrors(t *testing.T) {
	for address, want := range map[string]string{
		"https://tideember.cc/cover":           "https://tideember.cc/home",
		"https://xqjurgek.top/cover":           "https://xqjurgek.top/home",
		"https://new-covers.example.org/cover": huangdouBaseURL + "/home",
	} {
		if referer := sourceCoverReferer(sourceHuangdou, address); referer != want {
			t.Fatal(address, referer, want)
		}
	}
}

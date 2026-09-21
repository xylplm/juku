package app

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"testing"
)

func TestHuangdouResponsePlainJSONBeforeCipherPadding(t *testing.T) {
	key, err := hex.DecodeString("92f8c21e6747d8bec435722ed965d152c2d6af524d1aa2776833e9ee4ad18d4e")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"status":"y","data":{"id":"fixture"}}`)
	plain = append(plain, bytes.Repeat([]byte(" "), 64-len(plain))...)
	if _, err := aesCBCDecrypt(plain[16:], key, plain[:16]); err != nil {
		t.Fatal("text fixture must also have valid CBC padding", err)
	}
	decoded, err := huangdouDecode(plain, key)
	if err != nil || mapString(huangdouDataMap(decoded), "id") != "fixture" {
		t.Fatal("valid JSON depended on a random cipher padding result", err)
	}
}

func TestHuangdouResponseEncryptedTextAndGzip(t *testing.T) {
	key, iv := bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{7}, 16)
	plain := []byte(`{"data":{"id":"fixture","name":"text-only response"}}`)
	for _, compressed := range []bool{false, true} {
		data := bytes.Clone(plain)
		if compressed {
			var buffer bytes.Buffer
			writer := gzip.NewWriter(&buffer)
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data = buffer.Bytes()
		}
		encrypted, err := aesCBCEncrypt(data, key, iv)
		if err != nil {
			t.Fatal(err)
		}
		payload := append(bytes.Clone(iv), encrypted...)
		original := bytes.Clone(payload)
		decoded, err := huangdouDecode(payload, key)
		if err != nil || mapString(huangdouDataMap(decoded), "id") != "fixture" || !bytes.Equal(payload, original) {
			t.Fatal("encrypted text protocol regressed", compressed, err)
		}
	}
}

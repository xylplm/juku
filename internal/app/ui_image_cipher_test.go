package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestHuangguoCipherMatchesTextWorkerVectors(t *testing.T) {
	body, err := os.ReadFile("testdata/huangguo-cipher-text.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name       string `json:"name"`
		Ciphertext string `json:"ciphertext"`
		Plaintext  string `json:"plaintext"`
	}
	if err := json.Unmarshal(body, &vectors); err != nil || len(vectors) != 39 {
		t.Fatalf("invalid text fixtures: count=%d err=%v", len(vectors), err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			encrypted, err := hex.DecodeString(vector.Ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := hex.DecodeString(vector.Plaintext)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(encrypted)
			if actual := decryptHuangguoImage(encrypted); !bytes.Equal(actual, plain) {
				t.Fatalf("decoder differs from the source worker for a text-only fixture: got %x want %x", actual, plain)
			}
			if !bytes.Equal(encrypted, original) {
				t.Fatal("decoder mutated the input")
			}
			if actual := decodeImageBytes(encrypted, "https://covers.example.org/fixture"); !bytes.Equal(actual, original) || isKnownImage(actual) {
				t.Fatal("encrypted non-image text bypassed the cover type check")
			}
		})
	}
}

func TestHuangguoCipherRejectsEmptyTextEnvelopes(t *testing.T) {
	for _, input := range [][]byte{nil, {}, []byte("Salted__"), []byte("Salted__fixture!")} {
		if result := decryptHuangguoImage(input); len(result) != 0 {
			t.Fatal("empty or incomplete envelope was accepted")
		}
	}
}

func TestHuangguoLegacyPaddingUsesOnlyText(t *testing.T) {
	plain := []byte("legacy text fixture")
	for padding := 1; padding <= 16; padding++ {
		input := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
		if actual := trimHuangguoImagePadding(input); !bytes.Equal(actual, plain) {
			t.Fatalf("legacy padding %d was not preserved", padding)
		}
	}
	for _, input := range [][]byte{nil, []byte("unchanged text"), []byte("text\x01\x02"), []byte("text\x00")} {
		if actual := trimHuangguoImagePadding(input); !bytes.Equal(actual, input) {
			t.Fatal("invalid padding changed the payload")
		}
	}
}

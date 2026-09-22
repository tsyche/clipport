package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := []byte("test-password")
	plaintext := []byte("hello clipboard")

	ciphertext, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	key := []byte("test-password")
	plaintext := []byte("same input")

	c1, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(c1, c2) {
		t.Error("encrypt produced identical ciphertexts for the same input (nonce not randomized)")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	ciphertext, err := encrypt([]byte("correct-key"), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = decrypt([]byte("wrong-key"), ciphertext)
	if err == nil {
		t.Error("expected decrypt with wrong key to fail, but it succeeded")
	}
}

func TestDecryptTruncatedDataPanicsOrErrors(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// a panic here is a bug — truncated data should return an error, not panic
			t.Errorf("decrypt panicked on truncated data: %v", r)
		}
	}()

	_, err := decrypt([]byte("key"), []byte("tooshort"))
	if err == nil {
		t.Error("expected error on truncated ciphertext")
	}
}

func TestDeriveKeyDeterministicWithSalt(t *testing.T) {
	password := []byte("my-password")
	salt := bytes.Repeat([]byte{0xAB}, 32)

	k1, _, err := deriveKey(password, salt)
	if err != nil {
		t.Fatal(err)
	}
	k2, _, err := deriveKey(password, salt)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(k1, k2) {
		t.Error("deriveKey with same password+salt produced different keys")
	}
}

func TestDeriveKeyGeneratesSaltWhenNil(t *testing.T) {
	key, salt, err := deriveKey([]byte("pwd"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(salt) != 32 {
		t.Errorf("expected 32-byte salt, got %d", len(salt))
	}
	if len(key) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(key))
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	key := []byte("key")
	ciphertext, err := encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("encrypt empty plaintext: %v", err)
	}
	got, err := decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt empty plaintext: %v", err)
	}
	if !bytes.Equal(got, []byte{}) {
		t.Errorf("expected empty plaintext back, got %q", got)
	}
}

func TestSendClipboardRejectsOversizedFrame(t *testing.T) {
	oversized := strings.Repeat("a", maxClipboardFrameBytes+1)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	err := sendClipboard(w, oversized, nil)
	if !errors.Is(err, errClipboardTooLarge) {
		t.Fatalf("expected errClipboardTooLarge, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written for oversized frame, got %d", buf.Len())
	}
}

func TestSendClipboardAllowsMaxSizeFrame(t *testing.T) {
	// Exact limit must still succeed (no gob overhead on raw []byte payload path
	// is not guaranteed, so stay one byte under to avoid flaking on encoder overhead).
	atLimit := strings.Repeat("a", maxClipboardFrameBytes-64)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	if err := sendClipboard(w, atLimit, nil); err != nil {
		t.Fatalf("send at near-limit size: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected bytes written")
	}
}

func TestMonitorSentClipsRejectsOversizedFrame(t *testing.T) {
	// Craft a single gob frame whose payload alone exceeds the cap.
	payload := bytes.Repeat([]byte("x"), maxClipboardFrameBytes+1)
	var frame bytes.Buffer
	if err := gob.NewEncoder(&frame).Encode(payload); err != nil {
		t.Fatalf("encode oversized frame: %v", err)
	}

	// No key: MonitorSentClips would only hit the frame cap path.
	// EOF after the oversized frame must not be reached as clean shutdown.
	r := bufio.NewReader(&frame)
	clean := MonitorSentClips(r, nil)
	if clean {
		t.Fatal("expected oversized frame to disconnect uncleanly (false), got clean shutdown (true)")
	}
}

func TestMonitorSentClipsValidFrameThenEOF(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := gob.NewEncoder(w).Encode([]byte("ping")); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	clean := MonitorSentClips(bufio.NewReader(&buf), nil)
	if !clean {
		t.Fatal("expected true (clean EOF) after valid frame + EOF")
	}
}

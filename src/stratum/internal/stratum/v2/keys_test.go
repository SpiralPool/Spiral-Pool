// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package v2

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestLoadOrCreateKey_CreatesOwnerOnlyFileAndReloadsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stratum-v2", AuthorityKeyFile)

	key, created, err := LoadOrCreateKey(path)
	if err != nil || !created {
		t.Fatalf("first load: created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}\n$`).Match(data) {
		t.Errorf("key file content %q, want 64 hex characters and a newline", data)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("key file mode %v, want 0600", info.Mode().Perm())
		}
	}

	again, created, err := LoadOrCreateKey(path)
	if err != nil || created {
		t.Fatalf("second load: created=%v err=%v", created, err)
	}
	if !again.Key.Equals(&key.Key) {
		t.Error("reloaded key differs from the created one")
	}
}

func TestReadKeyFile_RejectsBadContent(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"not-hex":  "zz",
		"short":    strings.Repeat("ab", 31),
		"zero":     strings.Repeat("00", 32),
		"over-n":   strings.Repeat("ff", 32),
		"too-long": strings.Repeat("ab", 33),
		"empty":    "",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadKeyFile(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, _, err := LoadOrCreateKey(path); err == nil {
			t.Errorf("%s: LoadOrCreateKey replaced a bad key file instead of failing", name)
		}
	}
}

func TestWriteKeyFile_NeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), StaticKeyFile)
	keys, _ := GenerateServerKeys()
	if err := WriteKeyFile(path, keys.Static); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyFile(path, keys.Authority); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write error = %v, want ErrExist", err)
	}
	stored, err := ReadKeyFile(path)
	if err != nil || !stored.Key.Equals(&keys.Static.Key) {
		t.Fatal("existing key file was changed")
	}
}

// The expected string is the authority key in the Stratum Reference
// Implementation's example translator configuration.
func TestEncodeAuthorityKey_MatchesReferenceImplementationFormat(t *testing.T) {
	raw, _ := hex.DecodeString("24ee3c3804a1aaa4c03b80ea19f7a5863c916e8994b7db94a3bad7ee092b6ce7")
	var pub [32]byte
	copy(pub[:], raw)
	if got, want := EncodeAuthorityKey(pub), "9auqWEzQDVyd2oe1JVGFLMLHZtCo2FFqZwtKA5gd9xbuEu7PH72"; got != want {
		t.Errorf("EncodeAuthorityKey = %s, want %s", got, want)
	}
}

func TestLoadServerKeys_CreatesBothOnceAndKeepsTheAuthority(t *testing.T) {
	dir := t.TempDir()
	keys, created, err := LoadServerKeys(dir)
	if err != nil || len(created) != 2 {
		t.Fatalf("created %v, err %v", created, err)
	}
	again, created, err := LoadServerKeys(dir)
	if err != nil || len(created) != 0 {
		t.Fatalf("reload created %v, err %v", created, err)
	}
	if again.AuthorityPublicKey() != keys.AuthorityPublicKey() {
		t.Error("authority public key changed between loads")
	}
	if again.Static.Key.Equals(&again.Authority.Key) {
		t.Error("static and authority keys must differ")
	}
}

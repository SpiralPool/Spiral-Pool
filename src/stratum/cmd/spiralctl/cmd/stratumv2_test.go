// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	v2 "github.com/spiralpool/stratum/internal/stratum/v2"
)

var authorityLine = regexp.MustCompile(`Authority public key: ([0-9a-f]{64})`)

func TestStratumV2Keygen_CreatesOnceAndPubkeyMatches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stratum-v2")

	var out bytes.Buffer
	if err := stratumV2Keygen(&out, dir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := authorityLine.FindStringSubmatch(out.String())
	if m == nil || strings.Count(out.String(), "Created ") != 2 {
		t.Fatalf("keygen output:\n%s", out.String())
	}

	var pub bytes.Buffer
	if err := stratumV2Pubkey(&pub, dir); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(pub.String()), "\n")
	if len(lines) != 2 || lines[0] != m[1] {
		t.Fatalf("pubkey %q, keygen reported %q", pub.String(), m[1])
	}
	raw, _ := hex.DecodeString(m[1])
	var key [32]byte
	copy(key[:], raw)
	if want := v2.EncodeAuthorityKey(key); lines[1] != want || !strings.Contains(out.String(), want) {
		t.Errorf("base58 key: pubkey %q, want %q in both outputs", lines[1], want)
	}

	out.Reset()
	if err := stratumV2Keygen(&out, dir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already exist") || !strings.Contains(out.String(), m[1]) {
		t.Errorf("second keygen changed or recreated the keys:\n%s", out.String())
	}
}

func TestStratumV2Keygen_RotateKeepsBackupsAndChangesTheKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stratum-v2")
	var first bytes.Buffer
	if err := stratumV2Keygen(&first, dir, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := authorityLine.FindStringSubmatch(first.String())[1]

	var rotated bytes.Buffer
	when := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if err := stratumV2Keygen(&rotated, dir, true, when); err != nil {
		t.Fatal(err)
	}
	after := authorityLine.FindStringSubmatch(rotated.String())[1]
	if after == before {
		t.Error("rotate kept the same authority key")
	}
	for _, name := range []string{"authority.key", "static.key"} {
		if _, err := os.Stat(filepath.Join(dir, name+".bak-20260915T120000Z")); err != nil {
			t.Errorf("no backup of %s: %v", name, err)
		}
	}
}

func TestStratumV2Pubkey_ExplainsAMissingKey(t *testing.T) {
	err := stratumV2Pubkey(&bytes.Buffer{}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "spiralctl v2 keygen") {
		t.Fatalf("error = %v", err)
	}
}

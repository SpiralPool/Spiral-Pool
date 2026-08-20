// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package stratum

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/spiralpool/stratum/internal/config"
	"github.com/spiralpool/stratum/pkg/protocol"
	"go.uber.org/zap"
)

// =============================================================================
// TEST SUITE: set_difficulty wire/session agreement
// =============================================================================
// SendDifficulty renders the difficulty with %f, which is six decimal places,
// but the session stores the value used to validate shares. When those two
// disagree the miner mines to the announced difficulty while the pool checks
// against a different one, and every rounding that goes down opens a band of
// shares that meet what the miner was told and still reject as low-difficulty.
//
// The band is narrow (order 0.02% of shares at lottery magnitudes, where the
// seventh decimal is significant) which is exactly why it needs a test rather
// than a field report: it is far too small to see in a reject-rate summary.

// readAnnouncedDifficulty returns the difficulty carried by the next
// mining.set_difficulty notification on conn.
func readAnnouncedDifficulty(t *testing.T, conn net.Conn) float64 {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("reading set_difficulty: %v", err)
	}

	var msg struct {
		Method string    `json:"method"`
		Params []float64 `json:"params"`
	}
	if err := json.Unmarshal([]byte(trimLine(string(buf[:n]))), &msg); err != nil {
		t.Fatalf("unmarshalling %q: %v", string(buf[:n]), err)
	}
	if msg.Method != protocol.Methods.SetDifficulty {
		t.Fatalf("method = %q, want %q", msg.Method, protocol.Methods.SetDifficulty)
	}
	if len(msg.Params) != 1 {
		t.Fatalf("params = %v, want exactly one difficulty", msg.Params)
	}
	return msg.Params[0]
}

// trimLine drops the trailing newline the stratum framing adds.
func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// TestSendDifficulty_AnnouncedMatchesStored asserts that the difficulty on the
// wire and the difficulty the session validates against are the same number.
func TestSendDifficulty_AnnouncedMatchesStored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		send float64
		want float64
	}{
		{
			// A vardiff retarget at lottery magnitudes. %f drops the 4321 tail,
			// so before the fix the session validated against a difficulty the
			// miner was never told about.
			name: "seventh decimal rounds down",
			send: 0.0018694321,
			want: 0.001869,
		},
		{
			name: "seventh decimal rounds up",
			send: 0.0013485,
			want: 0.001349,
		},
		{
			name: "value already exact at six decimals",
			send: 0.001,
			want: 0.001,
		},
		{
			// Lottery MinDiff scaled to the shortest block time this pool
			// supports still lands well above the six-decimal limit.
			name: "scaled lottery floor",
			send: 0.000025,
			want: 0.000025,
		},
		{
			// A positive difficulty below what %f can express would otherwise be
			// announced and stored as zero, dividing by zero on both sides.
			name: "below six decimals floors instead of reaching zero",
			send: 1e-9,
			want: 1e-6,
		},
		{
			name: "ASIC magnitudes unaffected",
			send: 25600,
			want: 25600,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logger, _ := zap.NewDevelopment()
			server := NewServer(&config.StratumConfig{
				Listen:     "0.0.0.0:0",
				Difficulty: config.DifficultyConfig{Initial: 1},
			}, logger)

			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			session := &protocol.Session{ID: 1, Conn: serverConn}

			// net.Pipe is unbuffered, so the read has to be in flight before the write.
			announced := make(chan float64, 1)
			go func() { announced <- readAnnouncedDifficulty(t, clientConn) }()

			if err := server.SendDifficulty(session, tc.send); err != nil {
				t.Fatalf("SendDifficulty(%v): %v", tc.send, err)
			}

			gotWire := <-announced
			gotStored := session.GetDifficulty()

			if gotWire != tc.want {
				t.Errorf("announced difficulty = %v, want %v", gotWire, tc.want)
			}
			if gotStored != tc.want {
				t.Errorf("stored difficulty = %v, want %v", gotStored, tc.want)
			}
			// The point of the fix: a share meeting what the miner was told can
			// never miss what the pool validates against.
			if gotStored != gotWire {
				t.Errorf("announced %v but validating against %v: shares in that band reject as low-difficulty",
					gotWire, gotStored)
			}
		})
	}
}

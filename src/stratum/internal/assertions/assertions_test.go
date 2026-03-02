// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

// Package assertions — comprehensive tests for runtime invariant checking.
//
// IMPORTANT: These tests modify package-level global state (enabled, mode,
// totalChecks, totalViolations, logger). They MUST NOT use t.Parallel().
// Each test calls resetState() to ensure isolation.
//
// ModeFatal calls os.Exit(1) and cannot be tested directly.
// We verify its violation detection by testing ModePanic (recoverable)
// and checking that stats counters are correctly incremented.
package assertions

import (
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// resetState clears all package-level global state between tests.
func resetState() {
	enabled.Store(false)
	mode.Store(int32(ModeDisabled))
	logger.Store(nil)
	totalChecks.Store(0)
	totalViolations.Store(0)
}

// assertPanics verifies that f() panics with a message containing substr.
func assertPanics(t *testing.T, f func(), substr string) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic but none occurred")
		}
		msg := fmt.Sprintf("%v", r)
		if !strings.Contains(msg, substr) {
			t.Errorf("panic message %q does not contain %q", msg, substr)
		}
	}()
	f()
}

// assertNoPanic verifies that f() does NOT panic.
func assertNoPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	f()
}

// ═══════════════════════════════════════════════════════════════════════════════
// Lifecycle tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestEnable_DefaultMode(t *testing.T) {
	resetState()

	Enable()

	if !IsEnabled() {
		t.Error("expected assertions to be enabled after Enable()")
	}
	if Mode(mode.Load()) != ModeFatal {
		t.Errorf("expected ModeFatal after Enable(), got %d", mode.Load())
	}
}

func TestEnableWithMode_Panic(t *testing.T) {
	resetState()

	EnableWithMode(ModePanic)

	if !IsEnabled() {
		t.Error("expected assertions to be enabled")
	}
	if Mode(mode.Load()) != ModePanic {
		t.Errorf("expected ModePanic, got %d", mode.Load())
	}
}

func TestEnableWithMode_Log(t *testing.T) {
	resetState()

	EnableWithMode(ModeLog)

	if !IsEnabled() {
		t.Error("expected assertions to be enabled")
	}
	if Mode(mode.Load()) != ModeLog {
		t.Errorf("expected ModeLog, got %d", mode.Load())
	}
}

func TestDisable(t *testing.T) {
	resetState()

	Enable()
	if !IsEnabled() {
		t.Fatal("precondition failed: assertions should be enabled")
	}

	Disable()
	if IsEnabled() {
		t.Error("expected assertions to be disabled after Disable()")
	}
}

func TestIsEnabled_DefaultFalse(t *testing.T) {
	resetState()

	if IsEnabled() {
		t.Error("expected assertions disabled by default")
	}
}

func TestSetLogger(t *testing.T) {
	resetState()

	l := zap.NewNop().Sugar()
	SetLogger(l)

	if logger.Load() != l {
		t.Error("expected logger to be stored")
	}
}

func TestStats_CountersInitiallyZero(t *testing.T) {
	resetState()

	checks, violations := Stats()
	if checks != 0 || violations != 0 {
		t.Errorf("expected (0, 0), got (%d, %d)", checks, violations)
	}
}

func TestStats_ChecksIncrementOnPass(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	Assert(true, "passing check")
	Assert(true, "another passing check")

	checks, violations := Stats()
	if checks != 2 {
		t.Errorf("expected 2 checks, got %d", checks)
	}
	if violations != 0 {
		t.Errorf("expected 0 violations, got %d", violations)
	}
}

func TestStats_ViolationsIncrementOnFailure(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	// Trigger a violation (catch the panic)
	func() {
		defer func() { recover() }()
		Assert(false, "failing check")
	}()

	checks, violations := Stats()
	if checks != 1 {
		t.Errorf("expected 1 check, got %d", checks)
	}
	if violations != 1 {
		t.Errorf("expected 1 violation, got %d", violations)
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Assert tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssert_Passing(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		Assert(true, "condition is true")
	})
}

func TestAssert_Failing_Panic(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		Assert(false, "test condition")
	}, "test condition")
}

func TestAssert_Disabled_NoOp(t *testing.T) {
	resetState()
	// Assertions disabled by default

	assertNoPanic(t, func() {
		Assert(false, "should not fire when disabled")
	})

	checks, _ := Stats()
	if checks != 0 {
		t.Errorf("expected 0 checks when disabled, got %d", checks)
	}
}

func TestAssert_ModeLog_ContinuesExecution(t *testing.T) {
	resetState()
	EnableWithMode(ModeLog)

	// ModeLog should NOT panic — execution continues
	assertNoPanic(t, func() {
		Assert(false, "logged violation")
	})

	_, violations := Stats()
	if violations != 1 {
		t.Errorf("expected 1 violation in ModeLog, got %d", violations)
	}
}

func TestAssert_FormattedMessage(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		Assert(false, "block %d hash %s", 12345, "abcdef")
	}, "block 12345 hash abcdef")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertNotNil tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertNotNil_Passing(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	x := 42
	assertNoPanic(t, func() {
		AssertNotNil(&x, "pointer")
	})
}

func TestAssertNotNil_Failing_NilInterface(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertNotNil(nil, "value")
	}, "value must not be nil")
}

func TestAssertNotNil_Disabled(t *testing.T) {
	resetState()

	assertNoPanic(t, func() {
		AssertNotNil(nil, "should not fire")
	})
}

// Note: Go interface nil gotcha — a typed nil pointer (e.g. (*int)(nil))
// passed as interface{} is NOT == nil. This is a known Go language behavior,
// not a bug in AssertNotNil. The function checks interface-level nil only.
func TestAssertNotNil_TypedNilPointer_DoesNotTrigger(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	var p *int // typed nil pointer
	assertNoPanic(t, func() {
		AssertNotNil(p, "typed nil ptr")
	})
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertNotEmpty tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertNotEmpty_Passing(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertNotEmpty("hello", "greeting")
	})
}

func TestAssertNotEmpty_Failing(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertNotEmpty("", "miner address")
	}, "miner address must not be empty")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertPositive tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertPositive_Passing_Int(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertPositive(1, "count")
	})
}

func TestAssertPositive_Failing_Zero_Int(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertPositive(0, "count")
	}, "count must be positive")
}

func TestAssertPositive_Failing_Negative_Int(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertPositive(-5, "count")
	}, "count must be positive")
}

func TestAssertPositive_Float64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertPositive(0.001, "reward")
	})

	assertPanics(t, func() {
		AssertPositive(0.0, "reward")
	}, "reward must be positive")
}

func TestAssertPositive_Int64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertPositive(int64(1000000000), "satoshis")
	})
}

func TestAssertPositive_Uint64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertPositive(uint64(100), "block height")
	})

	// uint64(0) is <= 0 per the generic constraint
	assertPanics(t, func() {
		AssertPositive(uint64(0), "block height")
	}, "block height must be positive")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertNonNegative tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertNonNegative_Passing_Positive(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertNonNegative(5, "confirmations")
	})
}

func TestAssertNonNegative_Passing_Zero(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertNonNegative(0, "confirmations")
	})
}

func TestAssertNonNegative_Failing_Negative(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertNonNegative(-1, "confirmations")
	}, "confirmations must be non-negative")
}

func TestAssertNonNegative_Float64_Negative(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertNonNegative(-0.5, "balance")
	}, "balance must be non-negative")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertEqual tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertEqual_Passing_String(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertEqual("abc123", "abc123", "block hash")
	})
}

func TestAssertEqual_Failing_String(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertEqual("expected", "actual", "block hash")
	}, "block hash mismatch")
}

func TestAssertEqual_Passing_Uint64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertEqual(uint64(100), uint64(100), "height")
	})
}

func TestAssertEqual_Failing_Uint64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertEqual(uint64(100), uint64(200), "height")
	}, "height mismatch")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertLessOrEqual tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertLessOrEqual_Passing_Less(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertLessOrEqual(int64(50), int64(100), "paid vs rewards")
	})
}

func TestAssertLessOrEqual_Passing_Equal(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertLessOrEqual(int64(100), int64(100), "paid vs rewards")
	})
}

func TestAssertLessOrEqual_Failing_Greater(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertLessOrEqual(int64(150), int64(100), "paid vs rewards")
	}, "paid vs rewards exceeds limit: 150 > 100")
}

func TestAssertLessOrEqual_Float64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertLessOrEqual(9.99, 10.0, "fee ratio")
	})

	assertPanics(t, func() {
		AssertLessOrEqual(10.01, 10.0, "fee ratio")
	}, "fee ratio exceeds limit")
}

// ═══════════════════════════════════════════════════════════════════════════════
// AssertJobValid tests
// ═══════════════════════════════════════════════════════════════════════════════

// mockJob implements the interface { IsValid() bool } for testing.
type mockJob struct {
	valid bool
}

func (m *mockJob) IsValid() bool {
	return m.valid
}

func TestAssertJobValid_Passing(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertJobValid(&mockJob{valid: true}, "job_1")
	})
}

func TestAssertJobValid_NilJob(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertJobValid(nil, "job_1")
	}, "job job_1 is nil")
}

func TestAssertJobValid_InvalidState(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertJobValid(&mockJob{valid: false}, "job_1")
	}, "job job_1 is in invalid state")
}

// ═══════════════════════════════════════════════════════════════════════════════
// Economic invariant assertion tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestAssertNoDoublePayout_NotPaid(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertNoDoublePayout("block_hash_abc", func(hash string) bool {
			return false // not paid
		})
	})
}

func TestAssertNoDoublePayout_AlreadyPaid(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertNoDoublePayout("block_hash_abc", func(hash string) bool {
			return true // already paid
		})
	}, "double payout attempt for block block_hash_abc")
}

func TestAssertNoDoublePayout_Disabled(t *testing.T) {
	resetState()
	// Assertions disabled — should not call isPaid or panic

	called := false
	assertNoPanic(t, func() {
		AssertNoDoublePayout("block_hash_abc", func(hash string) bool {
			called = true
			return true
		})
	})

	if called {
		t.Error("isPaid should not be called when assertions are disabled")
	}
}

func TestAssertNoDoublePayout_VerifiesCorrectHash(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	var receivedHash string
	assertNoPanic(t, func() {
		AssertNoDoublePayout("0000abcdef123456", func(hash string) bool {
			receivedHash = hash
			return false
		})
	})

	if receivedHash != "0000abcdef123456" {
		t.Errorf("isPaid received wrong hash: %q", receivedHash)
	}
}

func TestAssertOnChainConfirmation_Matching(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertOnChainConfirmation(100000, "expected_hash", func(height uint64) (string, error) {
			return "expected_hash", nil
		})
	})
}

func TestAssertOnChainConfirmation_Mismatch(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertOnChainConfirmation(100000, "expected_hash", func(height uint64) (string, error) {
			return "different_hash", nil
		})
	}, "block 100000 hash mismatch")
}

func TestAssertOnChainConfirmation_ChainError(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertOnChainConfirmation(100000, "expected_hash", func(height uint64) (string, error) {
			return "", fmt.Errorf("daemon unreachable")
		})
	}, "failed to verify block 100000 on chain")
}

func TestAssertOnChainConfirmation_Disabled(t *testing.T) {
	resetState()
	// Disabled — getChainHash should not be called

	called := false
	assertNoPanic(t, func() {
		AssertOnChainConfirmation(100000, "hash", func(height uint64) (string, error) {
			called = true
			return "different", nil
		})
	})

	if called {
		t.Error("getChainHash should not be called when assertions are disabled")
	}
}

func TestAssertMaturityReached_Mature(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertMaturityReached(120, 100, 50000)
	})
}

func TestAssertMaturityReached_ExactlyAtMaturity(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertMaturityReached(100, 100, 50000)
	})
}

func TestAssertMaturityReached_NotMature(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertMaturityReached(50, 100, 50000)
	}, "block 50000 not mature: 50/100 confirmations")
}

func TestAssertWalletBinding_Matching(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertWalletBinding("TAddress123", "TAddress123")
	})
}

func TestAssertWalletBinding_Mismatch(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertWalletBinding("TAddress123", "TAddressXYZ")
	}, "wallet binding mismatch: share=TAddress123, coinbase=TAddressXYZ")
}

func TestAssertIdempotentOperation_Consistent(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertIdempotentOperation("result_A", "result_A", "calculateReward")
	})
}

func TestAssertIdempotentOperation_Inconsistent(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertPanics(t, func() {
		AssertIdempotentOperation("result_A", "result_B", "calculateReward")
	}, "non-idempotent operation calculateReward")
}

func TestAssertIdempotentOperation_Uint64(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	assertNoPanic(t, func() {
		AssertIdempotentOperation(uint64(500), uint64(500), "hashrate calc")
	})

	assertPanics(t, func() {
		AssertIdempotentOperation(uint64(500), uint64(501), "hashrate calc")
	}, "non-idempotent operation hashrate calc")
}

// ═══════════════════════════════════════════════════════════════════════════════
// handleViolation behavior tests
// ═══════════════════════════════════════════════════════════════════════════════

func TestHandleViolation_LoggerCalled(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	// Set up observable logger
	core, recorded := observer.New(zapcore.ErrorLevel)
	testLogger := zap.New(core).Sugar()
	SetLogger(testLogger)

	// Trigger violation (catch panic)
	func() {
		defer func() { recover() }()
		Assert(false, "test violation for logger")
	}()

	// Verify logger received the violation
	entries := recorded.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	if entries[0].Message != "INVARIANT VIOLATION" {
		t.Errorf("expected log message 'INVARIANT VIOLATION', got %q", entries[0].Message)
	}

	// Check that "message" field contains our violation text
	fields := entries[0].ContextMap()
	msg, ok := fields["message"]
	if !ok {
		t.Fatal("expected 'message' field in log entry")
	}
	if !strings.Contains(msg.(string), "test violation for logger") {
		t.Errorf("log message field %q does not contain violation text", msg)
	}

	// Check that "stack" field is present
	if _, ok := fields["stack"]; !ok {
		t.Error("expected 'stack' field in log entry")
	}
}

func TestHandleViolation_PanicContainsStackTrace(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg = fmt.Sprintf("%v", r)
			}
		}()
		Assert(false, "stack trace test")
	}()

	if !strings.Contains(panicMsg, "INVARIANT VIOLATION") {
		t.Errorf("panic message missing 'INVARIANT VIOLATION': %s", panicMsg)
	}
	if !strings.Contains(panicMsg, "stack trace test") {
		t.Errorf("panic message missing violation text: %s", panicMsg)
	}
	if !strings.Contains(panicMsg, "Stack trace:") {
		t.Errorf("panic message missing stack trace: %s", panicMsg)
	}
	// Verify our test function appears in the stack
	if !strings.Contains(panicMsg, "assertions_test") {
		t.Errorf("panic stack trace missing test file reference: %s", panicMsg)
	}
}

func TestHandleViolation_ModeDisabled_NoAction(t *testing.T) {
	resetState()
	// Enable assertions but set mode to ModeDisabled (edge case)
	enabled.Store(true)
	mode.Store(int32(ModeDisabled))

	// Should not panic even though condition is false
	assertNoPanic(t, func() {
		Assert(false, "disabled mode violation")
	})

	// Violation counter should still be incremented (handleViolation was called)
	_, violations := Stats()
	if violations != 1 {
		t.Errorf("expected 1 violation (counter increments regardless of mode), got %d", violations)
	}
}

func TestHandleViolation_ModeLog_NoPanic(t *testing.T) {
	resetState()
	EnableWithMode(ModeLog)

	// Should NOT panic
	assertNoPanic(t, func() {
		Assert(false, "logged violation")
	})

	// But violation should be counted
	checks, violations := Stats()
	if checks != 1 {
		t.Errorf("expected 1 check, got %d", checks)
	}
	if violations != 1 {
		t.Errorf("expected 1 violation, got %d", violations)
	}
}

func TestHandleViolation_ModePanic_Recoverable(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	// Panic should be recoverable
	recovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				recovered = true
			}
		}()
		Assert(false, "recoverable violation")
	}()

	if !recovered {
		t.Error("expected panic to be recoverable in ModePanic")
	}
}

// ═══════════════════════════════════════════════════════════════════════════════
// Edge cases and integration
// ═══════════════════════════════════════════════════════════════════════════════

func TestMultipleViolations_AllCounted(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	// Trigger 5 violations
	for i := 0; i < 5; i++ {
		func() {
			defer func() { recover() }()
			Assert(false, "violation %d", i)
		}()
	}

	checks, violations := Stats()
	if checks != 5 {
		t.Errorf("expected 5 checks, got %d", checks)
	}
	if violations != 5 {
		t.Errorf("expected 5 violations, got %d", violations)
	}
}

func TestMixedChecks_CorrectCounts(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	// 3 passing checks
	Assert(true, "pass 1")
	AssertNotEmpty("hello", "pass 2")
	AssertPositive(42, "pass 3")

	// 2 failing checks
	func() {
		defer func() { recover() }()
		Assert(false, "fail 1")
	}()
	func() {
		defer func() { recover() }()
		AssertPositive(-1, "fail 2")
	}()

	checks, violations := Stats()
	if checks != 5 {
		t.Errorf("expected 5 total checks, got %d", checks)
	}
	if violations != 2 {
		t.Errorf("expected 2 violations, got %d", violations)
	}
}

func TestEnableDisableToggle(t *testing.T) {
	resetState()

	// Disabled → no-op
	Assert(false, "should not fire")

	// Enable → should fire
	EnableWithMode(ModePanic)
	assertPanics(t, func() {
		Assert(false, "should fire")
	}, "should fire")

	// Disable → no-op again
	Disable()
	assertNoPanic(t, func() {
		Assert(false, "should not fire again")
	})

	// Only the one enabled assertion should count
	checks, violations := Stats()
	if checks != 1 {
		t.Errorf("expected 1 check (only the enabled one), got %d", checks)
	}
	if violations != 1 {
		t.Errorf("expected 1 violation, got %d", violations)
	}
}

// TestEconomicInvariants_FullScenario runs a realistic economic validation flow.
func TestEconomicInvariants_FullScenario(t *testing.T) {
	resetState()
	EnableWithMode(ModePanic)

	blockHash := "0000000000000000000123456789abcdef"
	minerWallet := "TMinorAddress123"
	blockHeight := uint64(100000)
	confirmations := uint64(120)
	requiredConfs := uint64(100)

	// 1. Verify block is on chain
	assertNoPanic(t, func() {
		AssertOnChainConfirmation(blockHeight, blockHash, func(height uint64) (string, error) {
			if height == blockHeight {
				return blockHash, nil
			}
			return "", fmt.Errorf("unknown height")
		})
	})

	// 2. Verify maturity
	assertNoPanic(t, func() {
		AssertMaturityReached(confirmations, requiredConfs, blockHeight)
	})

	// 3. Verify wallet binding
	assertNoPanic(t, func() {
		AssertWalletBinding(minerWallet, minerWallet)
	})

	// 4. Verify no double payout
	paidBlocks := map[string]bool{}
	assertNoPanic(t, func() {
		AssertNoDoublePayout(blockHash, func(hash string) bool {
			return paidBlocks[hash]
		})
	})

	// Mark as paid, then verify double-payout detection
	paidBlocks[blockHash] = true
	assertPanics(t, func() {
		AssertNoDoublePayout(blockHash, func(hash string) bool {
			return paidBlocks[hash]
		})
	}, "double payout attempt")

	// 5. Verify paid amount doesn't exceed reward
	assertNoPanic(t, func() {
		AssertLessOrEqual(int64(5000000000), int64(6250000000), "total paid vs block reward")
	})

	checks, violations := Stats()
	if violations != 1 {
		t.Errorf("expected exactly 1 violation (double payout), got %d", violations)
	}
	if checks < 6 {
		t.Errorf("expected at least 6 checks in full scenario, got %d", checks)
	}
}

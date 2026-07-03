// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import "testing"

// DGB and DGB-Scrypt must request the "digidollar-oracle" getblocktemplate rule
// (DigiByte v9.26.3+) so the node includes default_oracle_commitment once
// DigiDollar is active. DGB-Scrypt inherits GBTRules from DigiByteCoin via struct
// embedding — this test guards that the promotion keeps working.
func TestDigiByteGBTRulesIncludeDigiDollarOracle(t *testing.T) {
	for _, c := range []Coin{NewDigiByteCoin(), NewDigiByteScryptCoin()} {
		grc, ok := c.(GBTRulesCoin)
		if !ok {
			t.Fatalf("%s does not implement GBTRulesCoin", c.Symbol())
		}
		rules := grc.GBTRules()
		var hasSegwit, hasOracle bool
		for _, r := range rules {
			switch r {
			case "segwit":
				hasSegwit = true
			case "digidollar-oracle":
				hasOracle = true
			}
		}
		if !hasSegwit || !hasOracle {
			t.Errorf("%s GBTRules = %v, want to include both segwit and digidollar-oracle", c.Symbol(), rules)
		}
	}
}

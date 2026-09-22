// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// payoutKeyV2 is the per-coin stratum setting in a version 2 config; payoutKeyV1
// is the same setting in the older single-coin layout, which spells its stratum
// keys in camelCase.
const (
	payoutKeyV2 = "payout_from_worker_name"
	payoutKeyV1 = "payoutFromWorkerName"
)

// runMiningPayout implements "spiralctl mining payout <status|wallet|worker>".
//
// Default is "wallet": every block pays the address in the config, whatever a
// miner calls itself. "worker" opts in to paying the address in a miner's own
// stratum username, which is how an operator routes their own rigs to their own
// addresses. It stays off by default because anything that can reach the stratum
// port can name an address, so it is only safe on a pool reachable by nothing
// but the operator's own hardware.
func runMiningPayout(args []string, autoYes bool) error {
	action := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "status":
		return payoutStatus()
	case "wallet":
		return setPayoutSource(false, autoYes)
	case "worker":
		return setPayoutSource(true, autoYes)
	default:
		return fmt.Errorf("unknown payout action: %s. Use 'status', 'wallet', or 'worker'", action)
	}
}

// stratumSections returns every stratum mapping the setting lives in: one per
// coin for a version 2 config, or the single root section for the older layout.
// The key name differs between the two, so it is returned alongside.
func stratumSections(doc *yaml.Node) ([]*yaml.Node, string, error) {
	root := docRoot(doc)
	if root == nil {
		return nil, "", fmt.Errorf("config file has empty or malformed YAML document")
	}

	if isV2Config(doc) {
		var coinsNode *yaml.Node
		for i := 0; i < len(root.Content)-1; i += 2 {
			if root.Content[i].Value == "coins" {
				coinsNode = root.Content[i+1]
				break
			}
		}
		if coinsNode == nil || coinsNode.Kind != yaml.SequenceNode {
			return nil, payoutKeyV2, fmt.Errorf("config has no coins to configure")
		}
		var sections []*yaml.Node
		for _, coin := range coinsNode.Content {
			if coin.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j < len(coin.Content)-1; j += 2 {
				if coin.Content[j].Value == "stratum" && coin.Content[j+1].Kind == yaml.MappingNode {
					sections = append(sections, coin.Content[j+1])
				}
			}
		}
		if len(sections) == 0 {
			return nil, payoutKeyV2, fmt.Errorf("no coin in this config has a stratum section")
		}
		return sections, payoutKeyV2, nil
	}

	for i := 0; i < len(root.Content)-1; i += 2 {
		if root.Content[i].Value == "stratum" && root.Content[i+1].Kind == yaml.MappingNode {
			return []*yaml.Node{root.Content[i+1]}, payoutKeyV1, nil
		}
	}
	return nil, payoutKeyV1, fmt.Errorf("config has no stratum section")
}

// readPayoutSetting reports how many stratum sections have the setting on, out of
// how many exist. A config that has never seen the key reads off, which is the
// default. A count between the two means coins were configured separately.
func readPayoutSetting() (on int, total int, err error) {
	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return 0, 0, fmt.Errorf("failed to parse config: %w", err)
	}

	sections, key, err := stratumSections(&doc)
	if err != nil {
		return 0, 0, err
	}

	for _, s := range sections {
		for j := 0; j < len(s.Content)-1; j += 2 {
			if s.Content[j].Value == key && s.Content[j+1].Value == "true" {
				on++
				break
			}
		}
	}
	return on, len(sections), nil
}

func payoutStatus() error {
	printBanner()
	fmt.Printf("%s=== BLOCK REWARD PAYOUT ===%s\n\n", ColorBold, ColorReset)

	on, total, err := readPayoutSetting()
	if err != nil {
		return err
	}

	switch {
	case on == 0:
		fmt.Printf("  Mode:    %sconfigured wallet%s (default)\n", ColorGreen, ColorReset)
		fmt.Printf("  Effect:  every block pays the address configured for the coin,\n")
		fmt.Printf("           whatever a miner calls itself. Worker names are labels only.\n\n")
		fmt.Printf("  Pay each rig at the address in its own worker name:\n")
		fmt.Printf("    %sspiralctl mining payout worker%s\n", ColorYellow, ColorReset)
	case on == total:
		fmt.Printf("  Mode:    %sworker name%s (%d/%d coins)\n", ColorYellow, ColorReset, on, total)
		fmt.Printf("  Effect:  a miner that authorizes with a valid address for the coin\n")
		fmt.Printf("           is paid at that address. Any other worker name, and the\n")
		fmt.Printf("           multi-coin smart port, pay the configured wallet.\n")
		fmt.Printf("           Merge-mined auxiliary chains always pay their own\n")
		fmt.Printf("           configured address.\n\n")
		printWarning("Anything that can reach the stratum port can name its own payout address.")
		fmt.Println()
		printPayoutDisclosure()
		fmt.Printf("  Switch back with: %sspiralctl mining payout wallet%s\n", ColorYellow, ColorReset)
	default:
		fmt.Printf("  Mode:    %smixed%s - worker name on %d of %d coins\n", ColorYellow, ColorReset, on, total)
		fmt.Printf("  Effect:  the remaining coins pay their configured wallet.\n\n")
		fmt.Printf("  Make it uniform: %sspiralctl mining payout wallet%s or %sworker%s\n",
			ColorYellow, ColorReset, ColorYellow, ColorReset)
	}

	return nil
}

// printPayoutDisclosure states the conditions the operator accepts by enabling
// worker-name payout. It is shown before the confirmation prompt, and printed
// again by "spiralctl mining payout status" whenever the setting is on, so the
// terms are visible to whoever inspects the pool later and not only to whoever
// switched it on.
func printPayoutDisclosure() {
	fmt.Printf("%s=== CONDITIONS OF USE - READ BEFORE ENABLING ===%s\n\n", ColorBold, ColorReset)
	fmt.Println("  Enabling worker-name payout directs block rewards to addresses supplied")
	fmt.Println("  by connecting miners, in the coinbase transaction, irreversibly. This")
	fmt.Println("  Software is licensed for single-operator use. By enabling it you")
	fmt.Println("  represent and warrant that:")
	fmt.Println()
	fmt.Println("    1. You are the sole operator of this pool, and you own and control")
	fmt.Println("       every wallet address that any connected miner supplies, and every")
	fmt.Println("       mining device that connects to it.")
	fmt.Println("    2. The stratum port is reachable only from a private network under")
	fmt.Println("       your control. This Software must not be exposed to the public")
	fmt.Println("       internet or to any third-party miner.")
	fmt.Println("    3. You accept sole responsibility for every reward paid to an address")
	fmt.Println("       named by a miner, including rewards paid to an address you do not")
	fmt.Println("       control as a result of misconfiguration, device compromise, or")
	fmt.Println("       unauthorised access to the stratum port.")
	fmt.Println("    4. Payments in the coinbase transaction are final and cannot be")
	fmt.Println("       recalled, reversed, or recovered by this Software, its authors, or")
	fmt.Println("       its contributors.")
	fmt.Println()
	// The conditions above say "the stratum port", which reads as one port. A
	// pool with V2 or TLS enabled serves three, and the switch is gated in the
	// shared job manager, so it opens every one of them at once. An operator who
	// secured the V1 port and assumed that was the whole exposure would be wrong.
	fmt.Println("  This applies to EVERY stratum port this pool serves, not only the")
	fmt.Println("  default one: Stratum V1, the TLS port, and Stratum V2 — where the")
	fmt.Println("  address is read from the channel's user identity rather than from a")
	fmt.Println("  worker name. Enabling this opens all of them at once.")
	fmt.Println()
	fmt.Println("  This setting applies to the coin's own chain. Rewards from a merge-mined")
	fmt.Println("  auxiliary chain always pay the address configured for that chain.")
	fmt.Println()
	fmt.Println("  THE AUTHORS AND CONTRIBUTORS DISCLAIM ALL LIABILITY FOR ANY LOSS OF")
	fmt.Println("  FUNDS, MISDIRECTED REWARD, OR CLAIM BY ANY THIRD PARTY ARISING FROM THE")
	fmt.Println("  USE OF THIS SETTING, TO THE FULLEST EXTENT PERMITTED BY LAW. SEE")
	fmt.Println("  TERMS.md SECTION 5E AND WARNINGS.md.")
	fmt.Println()
	fmt.Println("  If any of the above is not true, leave this setting off. The default")
	fmt.Println("  pays every block to the wallet configured for the coin.")
	fmt.Println()
}

func setPayoutSource(fromWorker bool, autoYes bool) error {
	printBanner()
	if fromWorker {
		fmt.Printf("%s=== PAYOUT: MINER WORKER NAME ===%s\n\n", ColorBold, ColorReset)
	} else {
		fmt.Printf("%s=== PAYOUT: CONFIGURED WALLET ===%s\n\n", ColorBold, ColorReset)
	}

	on, total, err := readPayoutSetting()
	if err != nil {
		return err
	}
	if (fromWorker && on == total) || (!fromWorker && on == 0) {
		if fromWorker {
			printInfo("Block rewards already follow the miner's worker name")
		} else {
			printInfo("Block rewards already go to the configured wallet")
		}
		return nil
	}

	if fromWorker {
		printPayoutDisclosure()
		if !autoYes && !confirmActionMining("Do you accept these conditions and enable worker-name payout?") {
			printInfo("No change made")
			return nil
		}
	}

	changed, err := writePayoutSetting(fromWorker)
	if err != nil {
		return err
	}

	if fromWorker {
		printSuccess(fmt.Sprintf("Worker-name payout enabled on %d coin(s)", changed))
	} else {
		printSuccess(fmt.Sprintf("Block rewards now go to the configured wallet on %d coin(s)", changed))
	}
	printInfo("Restart to apply: sudo systemctl restart spiralstratum")
	return nil
}

// writePayoutSetting sets the payout key in every stratum section, creating it
// where a config predates the setting. Edits the YAML node tree so comments and
// ordering survive. Returns how many sections were written.
func writePayoutSetting(fromWorker bool) (int, error) {
	if err := backupFile(DefaultConfigFile); err != nil {
		printWarning(fmt.Sprintf("Failed to backup config: %v", err))
	}

	data, err := os.ReadFile(DefaultConfigFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read config: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("failed to parse config: %w", err)
	}

	written, err := applyPayoutSetting(&doc, fromWorker)
	if err != nil {
		return 0, err
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return 0, fmt.Errorf("failed to encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return 0, fmt.Errorf("failed to close encoder: %w", err)
	}

	// SECURITY: config file contains credentials, use 0600
	if err := atomicWriteFile(DefaultConfigFile, []byte(buf.String()), 0600); err != nil {
		return 0, fmt.Errorf("failed to write config: %w", err)
	}
	return written, nil
}

// applyPayoutSetting writes the payout key into every stratum section of a parsed
// config and reports how many it touched. Split out from writePayoutSetting so it
// can be tested against real config shapes without touching the live file.
func applyPayoutSetting(doc *yaml.Node, fromWorker bool) (int, error) {
	sections, key, err := stratumSections(doc)
	if err != nil {
		return 0, err
	}

	value := "false"
	if fromWorker {
		value = "true"
	}

	for _, s := range sections {
		found := false
		for j := 0; j < len(s.Content)-1; j += 2 {
			if s.Content[j].Value == key {
				s.Content[j+1].Value = value
				s.Content[j+1].Tag = "!!bool"
				found = true
				break
			}
		}
		if !found {
			s.Content = append(s.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: value},
			)
		}
	}
	return len(sections), nil
}

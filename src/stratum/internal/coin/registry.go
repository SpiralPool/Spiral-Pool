// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package coin

import (
	"fmt"
	"strings"
	"sync"
)

// CoinFactory creates a new instance of a Coin implementation.
type CoinFactory func() Coin

// registry holds all registered coin implementations.
var (
	registry   = make(map[string]CoinFactory)
	registryMu sync.RWMutex
)

// Register adds a coin implementation to the registry.
// This is typically called from init() functions in coin implementation files.
func Register(symbol string, factory CoinFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()

	symbol = strings.ToUpper(symbol)
	if _, exists := registry[symbol]; exists {
		panic(fmt.Sprintf("coin already registered: %s", symbol))
	}
	registry[symbol] = factory
}

// Create instantiates a coin by its symbol.
// Returns an error if the coin is not registered.
func Create(symbol string) (Coin, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()

	symbol = strings.ToUpper(symbol)
	factory, ok := registry[symbol]
	if !ok {
		return nil, fmt.Errorf("unknown coin: %s (registered: %s)", symbol, ListRegistered())
	}
	return factory(), nil
}

// IsRegistered checks if a coin symbol is registered.
func IsRegistered(symbol string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()

	_, ok := registry[strings.ToUpper(symbol)]
	return ok
}

// ListRegistered returns all registered coin symbols.
func ListRegistered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	symbols := make([]string, 0, len(registry))
	for symbol := range registry {
		symbols = append(symbols, symbol)
	}
	return symbols
}

// MustCreate is like Create but panics on error.
// Use this only when the coin is known to exist (e.g., in tests or init).
func MustCreate(symbol string) Coin {
	c, err := Create(symbol)
	if err != nil {
		panic(err)
	}
	return c
}

package coin

import "testing"

// The address the regtest daemon actually generated, which the pool rejected with
// "invalid address length: 52" before ecregtest: was understood.
func TestECashDecodesRegtestCashAddr(t *testing.T) {
	c := NewECashCoin()
	const regtestAddr = "ecregtest:qq2vtyhecy7e9qjh9aww3yl4zkpe8l8ceuas336mu9"
	hash, typ, err := c.DecodeAddress(regtestAddr)
	if err != nil {
		t.Fatalf("DecodeAddress(%q): %v", regtestAddr, err)
	}
	if typ != AddressTypeP2PKH {
		t.Errorf("type = %v, want P2PKH", typ)
	}
	if len(hash) != 20 {
		t.Errorf("hash length = %d, want 20", len(hash))
	}
}

// A mainnet address checksummed for "ecash" must not validate as regtest.
func TestECashRejectsWrongNetworkPrefix(t *testing.T) {
	c := NewECashCoin()
	if _, _, err := c.DecodeAddress("ecregtest:qr4pqy6q0h7cr6dhtcqbj7zxo0k0nx0kkq0k0nx0kk"); err == nil {
		t.Error("a bogus regtest address decoded without error")
	}
}

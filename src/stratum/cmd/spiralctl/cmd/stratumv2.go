// SPDX-License-Identifier: BSD-3-Clause
// SPDX-FileCopyrightText: Copyright (c) 2026 Spiral Pool Contributors

package cmd

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	v2 "github.com/spiralpool/stratum/internal/stratum/v2"
)

// DefaultStratumV2KeyDir is where the pool keeps its Stratum V2 keys when the
// config sets no stratum_v2_key_dir: stratum-v2/ next to the pool config.
var DefaultStratumV2KeyDir = filepath.Join(filepath.Dir(DefaultConfigFile), "stratum-v2")

// runStratumV2 handles "spiralctl v2 keygen|pubkey".
func runStratumV2(args []string) error {
	if len(args) < 1 {
		printStratumV2Usage()
		return nil
	}
	fs := flag.NewFlagSet("v2", flag.ContinueOnError)
	dir := fs.String("dir", DefaultStratumV2KeyDir, "Directory holding authority.key and static.key")
	rotate := fs.Bool("rotate", false, "keygen: replace the existing keys")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch args[0] {
	case "keygen":
		if err := ensureRoot(); err != nil {
			return err
		}
		return stratumV2Keygen(os.Stdout, *dir, *rotate, time.Now())
	case "pubkey":
		if err := ensureRoot(); err != nil {
			return err
		}
		return stratumV2Pubkey(os.Stdout, *dir)
	default:
		printStratumV2Usage()
		return fmt.Errorf("unknown v2 command: %s", args[0])
	}
}

func printStratumV2Usage() {
	fmt.Println("Usage: spiralctl v2 <command> [--dir DIR]")
	fmt.Println()
	fmt.Println("Stratum V2 miners and proxies authenticate the pool with its authority public key.")
	fmt.Println("The pool creates its keys the first time a Stratum V2 port starts.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  pubkey             Print the authority public key to configure on miners and proxies")
	fmt.Println("  keygen             Create the keys if they are missing")
	fmt.Println("  keygen --rotate    Replace the keys (old files kept as .bak-<time>); every miner")
	fmt.Println("                     and proxy must then be given the new authority key")
	fmt.Println()
	fmt.Printf("Default key directory: %s\n", DefaultStratumV2KeyDir)
}

// stratumV2Keygen creates the Stratum V2 keys in dir when they are missing. With
// rotate it first moves the existing key files aside as .bak-<time>.
func stratumV2Keygen(out io.Writer, dir string, rotate bool, now time.Time) error {
	if rotate {
		for _, name := range []string{v2.AuthorityKeyFile, v2.StaticKeyFile} {
			path := filepath.Join(dir, name)
			backup := fmt.Sprintf("%s.bak-%s", path, now.UTC().Format("20060102T150405Z"))
			if err := os.Rename(path, backup); err == nil {
				fmt.Fprintf(out, "Moved %s to %s\n", path, backup)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	keys, created, err := v2.LoadServerKeys(dir)
	if err != nil {
		return err
	}
	// Keys created by root must stay readable by the pool user that owns the config.
	if err := matchOwner(filepath.Dir(dir), append([]string{dir}, created...)); err != nil {
		return err
	}

	for _, path := range created {
		fmt.Fprintf(out, "Created %s\n", path)
	}
	authority := keys.AuthorityPublicKey()
	fmt.Fprintf(out, "Authority public key: %x\n", authority[:])
	fmt.Fprintf(out, "Base58 form (SRI translator and other Stratum Reference Implementation configs): %s\n", v2.EncodeAuthorityKey(authority))
	if len(created) == 0 {
		fmt.Fprintln(out, "The keys already exist; use --rotate to replace them.")
	} else {
		fmt.Fprintln(out, "Configure this key on your Stratum V2 miners and proxies, then restart the pool: sudo systemctl restart spiralstratum")
	}
	return nil
}

// stratumV2Pubkey prints the authority public key: hex on the first line, the base58
// form SRI configs take on the second.
func stratumV2Pubkey(out io.Writer, dir string) error {
	key, err := v2.ReadKeyFile(filepath.Join(dir, v2.AuthorityKeyFile))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no authority key in %s: the pool creates it when a Stratum V2 port starts, or run: sudo spiralctl v2 keygen", dir)
	}
	if err != nil {
		return err
	}
	pub := v2.SchnorrPubKey(key)
	fmt.Fprintf(out, "%x\n%s\n", pub[:], v2.EncodeAuthorityKey(pub))
	return nil
}

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Run locally: go run ./scripts/gen_license_keypair.go
// Prints keys to stdout only — store PRIVATE offline; never commit it.
func main() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	fmt.Println("# TR Driver license authority keypair — save PRIVATE offline")
	fmt.Println("LICENSE_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub))
	fmt.Println("LICENSE_PRIVATE_KEY=" + base64.StdEncoding.EncodeToString(priv.Seed()))
	fmt.Println()
	fmt.Println("# Optional hex forms")
	fmt.Println("PUBLIC_HEX=" + hex.EncodeToString(pub))
	fmt.Println("PRIVATE_SEED_HEX=" + hex.EncodeToString(priv.Seed()))
}

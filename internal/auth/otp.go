package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
)

const otpDigits = 6
const maxOTPAttempts = 5

func generateOTPCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashOTP(code string) string {
	sum := sha256.Sum256([]byte("trdriver-otp:" + code))
	return hex.EncodeToString(sum[:])
}

func generateChallengeToken() (string, string, error) {
	return newSessionToken()
}

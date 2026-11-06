package auth

import (
	"crypto/hmac"
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

// hashOTP binds the short-lived email code to the server session secret.
// The prefix lets migrations distinguish keyed hashes from legacy unsalted ones.
func (s *Service) hashOTP(code string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("trdriver-otp:" + code))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func generateChallengeToken() (string, string, error) {
	return newSessionToken()
}

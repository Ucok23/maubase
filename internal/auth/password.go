package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost trades hashing latency for brute-force resistance. 12 is a
// reasonable default for a login endpoint in 2026; bump it later if a
// benchmark on the target VPS shows headroom.
const bcryptCost = 12

func hashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func verifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

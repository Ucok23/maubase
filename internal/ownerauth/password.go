package ownerauth

import "golang.org/x/crypto/bcrypt"

// Deliberately not shared with internal/auth's identical-looking helpers:
// the two planes are kept independent on purpose (see package doc), and
// this is cheap enough to duplicate that sharing it isn't worth coupling
// the two packages together.
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

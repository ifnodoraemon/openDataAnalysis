package auth

import (
	"errors"
	"sync"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const passwordHashCost = bcrypt.DefaultCost

func HashPassword(password string) (string, error) {
	encoded, err := bcrypt.GenerateFromPassword([]byte(password), passwordHashCost)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func VerifyPassword(password, encoded string) bool {
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)) == nil
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// DummyPasswordHash returns a bcrypt hash of an opaque constant so that login
// attempts against unknown emails still pay the full bcrypt cost. This removes
// the timing oracle that allowed email enumeration.
func DummyPasswordHash() string {
	dummyHashOnce.Do(func() {
		hash, err := HashPassword("oda-login-timing-equalizer-7c1f4d")
		if err == nil {
			dummyHash = hash
		}
	})
	return dummyHash
}

// ValidatePasswordStrength checks that password is at least 8 characters long
// and contains both letters and digits.
func ValidatePasswordStrength(password string) error {
	if len(password) < 8 {
		return errors.New("密码长度至少为 8 个字符")
	}
	var hasLetter, hasDigit bool
	for _, char := range password {
		if unicode.IsLetter(char) {
			hasLetter = true
		}
		if unicode.IsDigit(char) {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码必须同时包含字母和数字")
	}
	return nil
}

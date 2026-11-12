package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost matches bcrypt.DefaultCost (10), spelled out because it is a
// value the operator's stored hash format depends on.
const bcryptCost = bcrypt.DefaultCost

// HashPassword returns the bcrypt hash of password, for use as
// cfg.Auth.PasswordHash. Used by the panel hash-password CLI subcommand.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches the bcrypt hash.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyPasswordHash is a fixed bcrypt hash (cost 10) with no known
// plaintext. It keeps password verification expensive when the username
// does not match an account.
const dummyPasswordHash = "$2a$10$Ph6q3hjW8AtIjY.5GquOtOrqc7qWvB.h31kXJw0nDJAWSgXRPekmq"

// VerifyAccountPassword performs a dummy bcrypt comparison for a missing account.
func VerifyAccountPassword(hash, password string) bool {
	if hash == "" {
		_ = VerifyPassword(dummyPasswordHash, password)
		return false
	}
	return VerifyPassword(hash, password)
}

// VerifyCredentials reports whether a username and password match the account.
func VerifyCredentials(cfgUsername, cfgPasswordHash, username, password string) bool {
	if username != cfgUsername {
		cfgPasswordHash = ""
	}
	return VerifyAccountPassword(cfgPasswordHash, password)
}

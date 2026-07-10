package deploy

import "crypto/rand"

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// RandomSecret returns n crypto/rand alphanumeric characters. The modulo bias
// across 62 symbols is negligible for credential purposes.
func RandomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b), nil
}

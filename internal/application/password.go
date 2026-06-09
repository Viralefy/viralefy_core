package application

import (
	"crypto/rand"
	"math/big"
)

const pwLower = "abcdefghijkmnpqrstuvwxyz"
const pwUpper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
const pwDigit = "23456789"
const pwSymbol = "!@#$%&*?"

// GeneratePassword gera uma senha forte e legível para o autocadastro,
// garantindo ao menos um caractere de cada classe. Usa crypto/rand.
func GeneratePassword() string {
	const length = 16
	classes := []string{pwLower, pwUpper, pwDigit, pwSymbol}
	all := pwLower + pwUpper + pwDigit + pwSymbol

	out := make([]byte, length)
	for i, set := range classes {
		out[i] = set[randInt(len(set))]
	}
	for i := len(classes); i < length; i++ {
		out[i] = all[randInt(len(all))]
	}
	// Embaralha para não deixar as classes fixas no início.
	for i := length - 1; i > 0; i-- {
		j := randInt(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

package application

import "math"

// Camada ABAC (atributos): decisões que dependem de atributos da requisição,
// não só do papel. Complementa o RBAC (domain.Principal.Can).

// largeRateChangeThreshold: variação relativa de taxa de câmbio acima da qual
// a operação é considerada sensível.
const largeRateChangeThreshold = 0.25

// IsLargeRateChange decide, a partir do atributo "magnitude da mudança", se a
// alteração de taxa é grande o suficiente para exigir um papel privilegiado.
func IsLargeRateChange(oldRate, newRate float64) bool {
	if oldRate <= 0 {
		return newRate > 0
	}
	return math.Abs(newRate-oldRate)/oldRate > largeRateChangeThreshold
}

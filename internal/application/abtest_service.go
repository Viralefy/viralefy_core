package application

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

// ABTestService — harness simples de A/B testing.
//
// Decisões de design:
//   - Variant assignment é sticky: persistido em ab_assignments. Mesmo
//     visitor sempre vê a mesma variant.
//   - Hash determinístico (sha256(visitor_id|experiment_key)) → ponto no
//     espaço [0, 1). Se a row de assignment for perdida (cache wipe, etc.),
//     o mesmo visitor reproduz a mesma variant porque o hash é estável.
//   - Pesos são relativos: {"control":50,"variant_a":50} e
//     {"control":1,"variant_a":1} produzem 50/50. Normalização interna.
//   - Variants iteradas em ordem alfabética pra estabilidade entre boots.
type ABTestService struct {
	repo domain.ABTestRepository
}

func NewABTestService(repo domain.ABTestRepository) *ABTestService {
	return &ABTestService{repo: repo}
}

// GetAssignment devolve a variant atribuída ao visitor naquele experimento.
//
// Fluxo:
//   1. Tenta ler assignment existente — hit retorna direto.
//   2. Carrega experimento. Se inativo, devolve ErrExperimentInactive
//      (caller decide fallback, normalmente "control").
//   3. Calcula variant via hash determinístico + pesos.
//   4. Persiste assignment (ON CONFLICT DO NOTHING — corrida benigna).
//   5. Devolve a variant.
func (s *ABTestService) GetAssignment(ctx context.Context, visitorID, experimentKey string) (string, error) {
	if visitorID == "" || experimentKey == "" {
		return "", domain.ErrInvalidInput
	}
	// 1. Sticky lookup.
	if a, err := s.repo.GetAssignment(ctx, visitorID, experimentKey); err == nil {
		return a.Variant, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	// 2. Carrega experimento.
	exp, err := s.repo.GetExperiment(ctx, experimentKey)
	if err != nil {
		return "", err
	}
	if !exp.Active {
		return "", domain.ErrExperimentInactive
	}
	// 3. Hash determinístico.
	variant := pickVariant(visitorID, experimentKey, exp.Variants)
	if variant == "" {
		// Experimento sem variants válidas — erro de configuração.
		return "", domain.ErrInvalidInput
	}
	// 4. Persiste (idempotente).
	if err := s.repo.CreateAssignment(ctx, domain.ABAssignment{
		VisitorID:     visitorID,
		ExperimentKey: experimentKey,
		Variant:       variant,
	}); err != nil {
		return "", err
	}
	return variant, nil
}

// TrackEvent grava um evento append-only em ab_events. eventName pode ser
// "exposure" (auto-disparado pelo componente quando renderiza), "conversion"
// (chamado da página de obrigado) ou qualquer string custom.
func (s *ABTestService) TrackEvent(ctx context.Context, visitorID, experimentKey, eventName string, payload map[string]any) error {
	if visitorID == "" || experimentKey == "" || eventName == "" {
		return domain.ErrInvalidInput
	}
	// Resolve a variant atual pra gravar no event sem o caller precisar
	// mandá-la. Se não há assignment, cria um (hash determinístico).
	variant, err := s.GetAssignment(ctx, visitorID, experimentKey)
	if err != nil {
		// Experimento inativo: ainda assim gravar o evento seria útil pra
		// auditoria pós-fato, mas como não temos variant, ignoramos.
		return err
	}
	return s.repo.CreateEvent(ctx, domain.ABEvent{
		ID:            uuid.New().String(),
		VisitorID:     visitorID,
		ExperimentKey: experimentKey,
		Variant:       variant,
		EventName:     eventName,
		Payload:       payload,
	})
}

// AdminListExperiments — todos os experimentos pro backoffice.
func (s *ABTestService) AdminListExperiments(ctx context.Context) ([]domain.ABExperiment, error) {
	return s.repo.ListExperiments(ctx)
}

// AdminCreateExperiment valida + persiste. Pesos zero/negativos rejeitados.
func (s *ABTestService) AdminCreateExperiment(ctx context.Context, e domain.ABExperiment) (*domain.ABExperiment, error) {
	if e.Key == "" || len(e.Variants) < 2 {
		return nil, domain.ErrInvalidInput
	}
	for _, w := range e.Variants {
		if w <= 0 {
			return nil, domain.ErrInvalidInput
		}
	}
	if err := s.repo.CreateExperiment(ctx, e); err != nil {
		return nil, err
	}
	return s.repo.GetExperiment(ctx, e.Key)
}

// AdminUpdateExperiment — atualiza descrição, pesos e ativo. Key imutável.
func (s *ABTestService) AdminUpdateExperiment(ctx context.Context, e domain.ABExperiment) (*domain.ABExperiment, error) {
	if e.Key == "" {
		return nil, domain.ErrInvalidInput
	}
	if len(e.Variants) > 0 {
		for _, w := range e.Variants {
			if w <= 0 {
				return nil, domain.ErrInvalidInput
			}
		}
	}
	if err := s.repo.UpdateExperiment(ctx, e); err != nil {
		return nil, err
	}
	return s.repo.GetExperiment(ctx, e.Key)
}

// pickVariant mapeia (visitor_id|experiment_key) → uint64 → ponto em
// [0, total_weight) → variant correspondente. Iteração em ordem alfabética
// das chaves garante reprodutibilidade independente da ordem do mapa Go.
func pickVariant(visitorID, experimentKey string, variants map[string]int) string {
	if len(variants) == 0 {
		return ""
	}
	// Ordena chaves pra estabilidade.
	keys := make([]string, 0, len(variants))
	total := 0
	for k, w := range variants {
		keys = append(keys, k)
		total += w
	}
	if total <= 0 {
		return ""
	}
	sort.Strings(keys)

	// Hash → uint64 → ponto em [0, total).
	h := sha256.Sum256([]byte(visitorID + "|" + experimentKey))
	n := binary.BigEndian.Uint64(h[:8])
	point := int(n % uint64(total))

	cum := 0
	for _, k := range keys {
		cum += variants[k]
		if point < cum {
			return k
		}
	}
	// Fallback (não deveria acontecer com aritmética correta).
	return keys[len(keys)-1]
}

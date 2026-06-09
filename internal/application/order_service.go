package application

import (
	"context"

	"github.com/Viralefy/viralefy_core/internal/domain"
)

// OrderService oferece leitura de pedidos pelo dono — usado pelo detalhe
// de tracking em /account/orders/{id}. Mantém o repo fora dos handlers e
// concentra a regra de autorização (UserID == dono) num único ponto.
type OrderService struct {
	repo  domain.OrderRepository
	plans domain.PlanRepository
}

func NewOrderService(repo domain.OrderRepository, plans domain.PlanRepository) *OrderService {
	return &OrderService{repo: repo, plans: plans}
}

// GetByIDForUser devolve o pedido se (e somente se) ele pertence ao userID
// passado. Qualquer outro caso (não-existe, pertence a outro user) retorna
// domain.ErrNotFound — não vazar diferença entre os dois evita enumeration.
func (s *OrderService) GetByIDForUser(ctx context.Context, userID, orderID string) (*domain.Order, error) {
	if userID == "" || orderID == "" {
		return nil, domain.ErrNotFound
	}
	o, err := s.repo.GetByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o == nil || o.UserID != userID {
		return nil, domain.ErrNotFound
	}
	return o, nil
}

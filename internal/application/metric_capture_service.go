package application

import (
	"context"
	"time"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/external/metrics"
	"github.com/Viralefy/viralefy_core/internal/infrastructure/observability"
)

// MetricCaptureService tira snapshots públicos do alvo do pedido (perfil
// IG/TikTok ou publicação) e persiste em orders.baseline_metrics ou
// .delivery_metrics conforme o caller.
type MetricCaptureService struct {
	orders   domain.OrderRepository
	plans    domain.PlanRepository
	profiles domain.ProfileRepository
	scraper  *metrics.Service
}

func NewMetricCaptureService(
	orders domain.OrderRepository,
	plans domain.PlanRepository,
	profiles domain.ProfileRepository,
) *MetricCaptureService {
	return &MetricCaptureService{
		orders:   orders,
		plans:    plans,
		profiles: profiles,
		scraper:  metrics.NewService(),
	}
}

// CaptureBaseline grava o snapshot pré-entrega pra um pedido.
// Resolve o alvo via plan.TargetType + order.ProfileID/PublicationURL.
func (s *MetricCaptureService) CaptureBaseline(ctx context.Context, orderID string) error {
	return s.capture(ctx, orderID, true)
}

// CaptureDelivery grava o snapshot pós-entrega. Usado após confirmação
// + N horas pra comparar (delivery - baseline) ≈ followers_qty.
func (s *MetricCaptureService) CaptureDelivery(ctx context.Context, orderID string) error {
	return s.capture(ctx, orderID, false)
}

func (s *MetricCaptureService) capture(ctx context.Context, orderID string, baseline bool) error {
	ord, err := s.orders.GetByID(ctx, orderID)
	if err != nil {
		return err
	}
	plan, err := s.plans.GetByID(ctx, ord.PlanID)
	if err != nil {
		return err
	}

	// Timeout maior que o scraper interno (10s) pra cobrir o lookup do DB.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var snap metrics.Snapshot
	var source string
	switch plan.TargetType {
	case string(domain.TargetProfile):
		if ord.ProfileID == nil {
			return nil
		}
		p, err := s.profiles.GetByID(ctx, *ord.ProfileID)
		if err != nil || p == nil {
			return err
		}
		snap, source, err = s.scraper.CaptureProfile(ctx, string(p.Platform), p.Handle)
		if err != nil {
			observability.FromContext(ctx).Warn("baseline_capture failed",
				"order_id", orderID,
				"platform", p.Platform,
				"handle", p.Handle,
				"error", err.Error(),
			)
			// Ainda grava o snapshot vazio + erro, com source ajustado pra
			// "manual_pending" — admin enxerga o pedido e preenche.
			source = "manual_pending"
		}
	case string(domain.TargetPublication):
		if ord.PublicationURL == nil || *ord.PublicationURL == "" {
			return nil
		}
		snap, source, err = s.scraper.CapturePublication(ctx, *ord.PublicationURL)
		if err != nil {
			observability.FromContext(ctx).Warn("baseline_capture failed",
				"order_id", orderID,
				"url", *ord.PublicationURL,
				"error", err.Error(),
			)
			source = "manual_pending"
		}
	default:
		return nil
	}

	// Persiste com qualquer payload (vazio quando scrape falhou) — UI mostra
	// "manual_pending" e admin pode editar.
	payload := map[string]any{
		"followers": snap.Followers,
		"following": snap.Following,
		"posts":     snap.Posts,
		"likes":     snap.Likes,
		"comments":  snap.Comments,
		"views":     snap.Views,
		"shares":    snap.Shares,
		"username":  snap.Username,
		"title":     snap.Title,
		"url":       snap.URL,
		"fetched_at": snap.FetchedAt,
	}
	if len(snap.Errors) > 0 {
		payload["errors"] = snap.Errors
	}
	if baseline {
		return s.orders.SetBaselineMetrics(ctx, orderID, payload, source)
	}
	return s.orders.SetDeliveryMetrics(ctx, orderID, payload, source)
}

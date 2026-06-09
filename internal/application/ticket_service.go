package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type TicketService struct {
	repo    domain.TicketRepository
	users   domain.UserRepository
	email   EmailSender
	siteURL string
}

func NewTicketService(repo domain.TicketRepository, users domain.UserRepository, email EmailSender, siteURL string) *TicketService {
	return &TicketService{repo: repo, users: users, email: email, siteURL: siteURL}
}

type OpenTicketInput struct {
	UserID  string
	Subject string
	Body    string
	OrderID *string
}

type TicketDetail struct {
	Ticket   domain.Ticket          `json:"ticket"`
	View     *domain.TicketView     `json:"view,omitempty"`
	Messages []domain.TicketMessage `json:"messages"`
}

func (s *TicketService) Open(ctx context.Context, in OpenTicketInput) (*domain.Ticket, error) {
	in.Subject = strings.TrimSpace(in.Subject)
	in.Body = strings.TrimSpace(in.Body)
	if in.UserID == "" || in.Subject == "" || in.Body == "" {
		return nil, domain.ErrInvalidInput
	}
	t := domain.Ticket{
		ID:       uuid.New().String(),
		UserID:   in.UserID,
		Subject:  in.Subject,
		Status:   domain.TicketStatusOpen,
		Priority: domain.TicketPriorityNormal,
		OrderID:  in.OrderID,
	}
	if err := s.repo.Create(ctx, t); err != nil {
		return nil, err
	}
	if err := s.repo.AppendMessage(ctx, domain.TicketMessage{
		ID: uuid.New().String(), TicketID: t.ID,
		AuthorType: domain.TicketAuthorUser, AuthorID: in.UserID, Body: in.Body,
	}); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *TicketService) ListForUser(ctx context.Context, userID string) ([]domain.Ticket, error) {
	return s.repo.ListByUser(ctx, userID)
}

// CountOpenForUser conta os tickets com status open OR pending — o que o
// usuário precisa de atenção. Usado no badge do Header da loja.
func (s *TicketService) CountOpenForUser(ctx context.Context, userID string) (int, error) {
	list, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range list {
		if t.Status == domain.TicketStatusOpen || t.Status == domain.TicketStatusPending {
			n++
		}
	}
	return n, nil
}

// GetForUser garante que o ticket pertence ao usuário antes de devolver.
func (s *TicketService) GetForUser(ctx context.Context, ticketID, userID string) (*TicketDetail, error) {
	t, err := s.repo.GetByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if t.UserID != userID {
		return nil, domain.ErrForbidden
	}
	msgs, err := s.repo.ListMessages(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	return &TicketDetail{Ticket: *t, Messages: msgs}, nil
}

// ReplyAsUser anexa mensagem e reabre o ticket se estava resolved/pending.
func (s *TicketService) ReplyAsUser(ctx context.Context, ticketID, userID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return domain.ErrInvalidInput
	}
	t, err := s.repo.GetByID(ctx, ticketID)
	if err != nil {
		return err
	}
	if t.UserID != userID {
		return domain.ErrForbidden
	}
	if t.Status == domain.TicketStatusClosed {
		return domain.ErrConflict
	}
	if err := s.repo.AppendMessage(ctx, domain.TicketMessage{
		ID: uuid.New().String(), TicketID: ticketID,
		AuthorType: domain.TicketAuthorUser, AuthorID: userID, Body: body,
	}); err != nil {
		return err
	}
	// Resposta do cliente reabre para o suporte trabalhar.
	if t.Status != domain.TicketStatusOpen {
		_ = s.repo.UpdateStatus(ctx, ticketID, domain.TicketStatusOpen)
	}
	return nil
}

// AdminList lista tickets para o backoffice. statusFilter vazio = todos.
func (s *TicketService) AdminList(ctx context.Context, statusFilter string) ([]domain.TicketView, error) {
	return s.repo.ListAllView(ctx, statusFilter)
}

func (s *TicketService) AdminGet(ctx context.Context, ticketID string) (*TicketDetail, error) {
	v, err := s.repo.GetView(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	msgs, err := s.repo.ListMessages(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	return &TicketDetail{Ticket: v.Ticket, View: v, Messages: msgs}, nil
}

// ReplyAsAdmin anexa resposta do admin, marca como "pending" (aguardando cliente)
// e dispara e-mail de notificação para o cliente.
func (s *TicketService) ReplyAsAdmin(ctx context.Context, ticketID, adminID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return domain.ErrInvalidInput
	}
	t, err := s.repo.GetByID(ctx, ticketID)
	if err != nil {
		return err
	}
	if err := s.repo.AppendMessage(ctx, domain.TicketMessage{
		ID: uuid.New().String(), TicketID: ticketID,
		AuthorType: domain.TicketAuthorAdmin, AuthorID: adminID, Body: body,
	}); err != nil {
		return err
	}
	if t.Status == domain.TicketStatusOpen {
		_ = s.repo.UpdateStatus(ctx, ticketID, domain.TicketStatusPending)
	}
	// Notificação por e-mail (não bloqueia se falhar).
	s.notifyUserOfReply(ctx, t, body)
	return nil
}

func (s *TicketService) AdminUpdateStatus(ctx context.Context, ticketID string, status domain.TicketStatus) error {
	switch status {
	case domain.TicketStatusOpen, domain.TicketStatusPending, domain.TicketStatusResolved, domain.TicketStatusClosed:
	default:
		return domain.ErrInvalidInput
	}
	return s.repo.UpdateStatus(ctx, ticketID, status)
}

func (s *TicketService) AdminUpdatePriority(ctx context.Context, ticketID string, priority domain.TicketPriority) error {
	switch priority {
	case domain.TicketPriorityLow, domain.TicketPriorityNormal, domain.TicketPriorityHigh, domain.TicketPriorityUrgent:
	default:
		return domain.ErrInvalidInput
	}
	return s.repo.UpdatePriority(ctx, ticketID, priority)
}

func (s *TicketService) notifyUserOfReply(ctx context.Context, t *domain.Ticket, body string) {
	u, err := s.users.GetByID(ctx, t.UserID)
	if err != nil || u == nil {
		return
	}
	subject, html, text, err := BuildTicketReplyEmail(TicketReplyEmailData{
		SiteURL:  s.siteURL,
		Name:     u.Name,
		Subject:  t.Subject,
		Body:     body,
		TicketID: t.ID,
	})
	if err != nil {
		return
	}
	_ = s.email.Send(ctx, EmailMessage{To: u.Email, Subject: subject, HTMLBody: html, TextBody: text})
}

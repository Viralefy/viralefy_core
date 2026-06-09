package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/Viralefy/viralefy_core/internal/domain"
)

type TicketRepo struct{ db *DB }

func NewTicketRepo(db *DB) *TicketRepo { return &TicketRepo{db: db} }

const ticketCols = `id, user_id, subject, status, priority, order_id, assigned_admin_id, created_at, updated_at`

func (r *TicketRepo) Create(ctx context.Context, t domain.Ticket) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO tickets (id, user_id, subject, status, priority, order_id, assigned_admin_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t.ID, t.UserID, t.Subject, t.Status, t.Priority, t.OrderID, t.AssignedAdminID)
	return err
}

func (r *TicketRepo) GetByID(ctx context.Context, id string) (*domain.Ticket, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets WHERE id=$1`, id)
	var t domain.Ticket
	err := row.Scan(&t.ID, &t.UserID, &t.Subject, &t.Status, &t.Priority,
		&t.OrderID, &t.AssignedAdminID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &t, err
}

func (r *TicketRepo) ListByUser(ctx context.Context, userID string) ([]domain.Ticket, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+ticketCols+`
		FROM tickets WHERE user_id=$1 ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.Ticket{}
	for rows.Next() {
		var t domain.Ticket
		if err := rows.Scan(&t.ID, &t.UserID, &t.Subject, &t.Status, &t.Priority,
			&t.OrderID, &t.AssignedAdminID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

const ticketViewCols = `t.id, t.user_id, t.subject, t.status, t.priority, t.order_id, t.assigned_admin_id, t.created_at, t.updated_at,
	COALESCE(u.name, ''), COALESCE(u.email, ''),
	COALESCE((SELECT count(*) FROM ticket_messages m WHERE m.ticket_id = t.id), 0),
	COALESCE((SELECT MAX(created_at) FROM ticket_messages m WHERE m.ticket_id = t.id), t.created_at),
	COALESCE((SELECT author_type FROM ticket_messages m WHERE m.ticket_id = t.id ORDER BY created_at DESC LIMIT 1), '')`

func (r *TicketRepo) ListAllView(ctx context.Context, statusFilter string) ([]domain.TicketView, error) {
	var rows pgx.Rows
	var err error
	if statusFilter != "" {
		rows, err = r.db.pool.Query(ctx, `SELECT `+ticketViewCols+`
			FROM tickets t LEFT JOIN users u ON u.id = t.user_id
			WHERE t.status = $1 ORDER BY t.updated_at DESC LIMIT 500`, statusFilter)
	} else {
		rows, err = r.db.pool.Query(ctx, `SELECT `+ticketViewCols+`
			FROM tickets t LEFT JOIN users u ON u.id = t.user_id
			ORDER BY t.updated_at DESC LIMIT 500`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.TicketView{}
	for rows.Next() {
		var v domain.TicketView
		if err := rows.Scan(&v.ID, &v.UserID, &v.Subject, &v.Status, &v.Priority,
			&v.OrderID, &v.AssignedAdminID, &v.CreatedAt, &v.UpdatedAt,
			&v.UserName, &v.UserEmail, &v.MessageCount, &v.LastMessageAt, &v.LastAuthorType); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

func (r *TicketRepo) GetView(ctx context.Context, id string) (*domain.TicketView, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+ticketViewCols+`
		FROM tickets t LEFT JOIN users u ON u.id = t.user_id WHERE t.id=$1`, id)
	var v domain.TicketView
	err := row.Scan(&v.ID, &v.UserID, &v.Subject, &v.Status, &v.Priority,
		&v.OrderID, &v.AssignedAdminID, &v.CreatedAt, &v.UpdatedAt,
		&v.UserName, &v.UserEmail, &v.MessageCount, &v.LastMessageAt, &v.LastAuthorType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &v, err
}

func (r *TicketRepo) UpdateStatus(ctx context.Context, id string, status domain.TicketStatus) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE tickets SET status=$2, updated_at=NOW() WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *TicketRepo) UpdatePriority(ctx context.Context, id string, priority domain.TicketPriority) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE tickets SET priority=$2, updated_at=NOW() WHERE id=$1`, id, priority)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *TicketRepo) AssignAdmin(ctx context.Context, id string, adminID *string) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE tickets SET assigned_admin_id=$2, updated_at=NOW() WHERE id=$1`, id, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *TicketRepo) AppendMessage(ctx context.Context, m domain.TicketMessage) error {
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO ticket_messages (id, ticket_id, author_type, author_id, body)
		VALUES ($1,$2,$3,$4,$5)`,
		m.ID, m.TicketID, m.AuthorType, m.AuthorID, m.Body)
	if err != nil {
		return err
	}
	// updated_at do ticket reflete a última atividade.
	_, _ = r.db.pool.Exec(ctx, `UPDATE tickets SET updated_at=NOW() WHERE id=$1`, m.TicketID)
	return nil
}

// ListMessages devolve mensagens com nome do autor resolvido (user ou admin).
func (r *TicketRepo) ListMessages(ctx context.Context, ticketID string) ([]domain.TicketMessage, error) {
	rows, err := r.db.pool.Query(ctx, `
		SELECT m.id, m.ticket_id, m.author_type, m.author_id, m.body, m.created_at,
			COALESCE(
				CASE WHEN m.author_type='user'  THEN (SELECT name FROM users  WHERE id = m.author_id)
				     WHEN m.author_type='admin' THEN (SELECT name FROM admins WHERE id = m.author_id)
				END, '') AS author_name
		FROM ticket_messages m
		WHERE m.ticket_id=$1 ORDER BY m.created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.TicketMessage{}
	for rows.Next() {
		var m domain.TicketMessage
		if err := rows.Scan(&m.ID, &m.TicketID, &m.AuthorType, &m.AuthorID, &m.Body, &m.CreatedAt, &m.AuthorName); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

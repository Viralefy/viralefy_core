package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/Viralefy/viralefy_core/internal/domain"
	"github.com/jackc/pgx/v5"
)

type UserRepo struct{ db *DB }

func NewUserRepo(db *DB) *UserRepo { return &UserRepo{db: db} }

const userCols = `id, email, name, instagram,
	COALESCE(phone, ''), COALESCE(telegram, ''),
	password_hash, created_at, tracking_data,
	deleted_at, deleted_by_admin_id, delete_reason`

func (r *UserRepo) Create(ctx context.Context, u domain.User) error {
	tracking, _ := json.Marshal(u.TrackingData)
	if len(tracking) == 0 {
		tracking = []byte("{}")
	}
	_, err := r.db.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, instagram, phone, telegram, password_hash, tracking_data)
		VALUES ($1,$2,$3,$4, NULLIF($5,''), NULLIF($6,''), $7, $8)`,
		u.ID, u.Email, u.Name, u.Instagram, u.Phone, u.Telegram, u.PasswordHash, tracking)
	return err
}

// GetByEmail retorna apenas users ATIVOS. Soft-deletados ficam invisíveis pra
// auth (login, register-conflict-check, recovery), permitindo reuso do email
// após exclusão LGPD (Art. 18 IV). Migration 047 reforça com índice parcial.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE email=$1 AND deleted_at IS NULL`, email)
	return scanUser(row)
}

func (r *UserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	row := r.db.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id)
	return scanUser(row)
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var tracking []byte
	err := row.Scan(
		&u.ID, &u.Email, &u.Name, &u.Instagram,
		&u.Phone, &u.Telegram,
		&u.PasswordHash, &u.CreatedAt, &tracking,
		&u.DeletedAt, &u.DeletedByAdminID, &u.DeleteReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err == nil && len(tracking) > 0 {
		u.TrackingData = map[string]any{}
		_ = json.Unmarshal(tracking, &u.TrackingData)
	}
	return &u, err
}

// escapeLikePattern neutraliza os curingas de LIKE num termo de busca.
//
// O quê: prefixa `\` em `\`, `%` e `_` pra que o termo case literalmente.
// Onde:  usada por ListPageWithCreditBalance ao montar o ILIKE da busca admin.
// Entradas: `s` — termo cru digitado pelo admin.
// Saídas: termo seguro pra interpolar entre `%...%` num LIKE com ESCAPE '\'.
// Efeitos: nenhum — pura.
//
// Ordem importa: a barra é escapada PRIMEIRO, senão as barras que este próprio
// código insere seriam escapadas de novo.
func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// testEmailSuffix é o sufixo reservado de fixtures (TLD `.test`, RFC 2606).
// Usuário real nunca termina nisso, então serve de filtro seguro pra tirar
// persona de smoke/CI da lista de clientes.
const testEmailSuffix = "%@viralefy.test"

// ListPageWithCreditBalance — página de clientes pro backoffice, com saldo.
//
// O quê: uma página keyset (created_at, id) DESC, com busca opcional por email
//
//	ou nome, mais o total que casa com os mesmos filtros.
//
// Onde:  chamada por AdminListUsers (GET /v1/admin/users). É o que alimenta a
//
//	tabela de clientes e o contador "N clientes".
//
// Fluxo: vem da query string já validada (domain.UserListQuery) → SQL
//
//	parametrizado → volta pro handler, que monta o cursor da próxima.
//
// Entradas: `q` — Limit (>=1), cursor opcional, Search opcional, IncludeTest.
// Saídas: itens da página, total (sem cursor), erro de banco.
// Efeitos: leitura no Postgres (users LEFT JOIN credit_accounts).
//
// Keyset e não OFFSET: com OFFSET, um cadastro novo durante a navegação empurra
// a lista e o admin vê a mesma linha duas vezes (ou pula uma). O par
// (created_at, id) é estável e ainda usa o índice de created_at.
func (r *UserRepo) ListPageWithCreditBalance(ctx context.Context, q domain.UserListQuery) ([]domain.UserView, int, error) {
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}

	// Filtros compartilhados entre a contagem e a página — montados uma vez pra
	// que total e itens NUNCA divirjam de critério.
	where := []string{"u.deleted_at IS NULL"}
	args := []any{}
	add := func(clause string, vals ...any) {
		for i := range vals {
			clause = strings.Replace(clause, "?", "$"+strconv.Itoa(len(args)+i+1), 1)
		}
		args = append(args, vals...)
		where = append(where, clause)
	}
	if !q.IncludeTest {
		add("u.email NOT LIKE ?", testEmailSuffix)
	}
	if q.Search != "" {
		// O termo vai como PARÂMETRO (nunca concatenado), mas isso sozinho não
		// basta: `%` e `_` são wildcards do LIKE. Sem escapar, buscar "%"
		// devolvia a base inteira e "a_b" casava "axb". Escapamos e declaramos
		// o ESCAPE explicitamente.
		like := "%" + escapeLikePattern(q.Search) + "%"
		add(`(u.email ILIKE ? ESCAPE '\' OR u.name ILIKE ? ESCAPE '\')`, like, like)
	}

	countSQL := "SELECT count(*) FROM users u WHERE " + strings.Join(where, " AND ")
	var total int
	if err := r.db.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Predicado keyset só na página (o total é do conjunto inteiro).
	pageWhere := append([]string{}, where...)
	pageArgs := append([]any{}, args...)
	if !q.CursorTime.IsZero() && q.CursorID != "" {
		pageArgs = append(pageArgs, q.CursorTime, q.CursorID)
		pageWhere = append(pageWhere, "(u.created_at, u.id) < ($"+strconv.Itoa(len(pageArgs)-1)+", $"+strconv.Itoa(len(pageArgs))+")")
	}
	pageArgs = append(pageArgs, q.Limit)

	rows, err := r.db.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.instagram,
		       COALESCE(u.phone, ''), COALESCE(u.telegram, ''),
		       u.created_at, COALESCE(c.balance_cents, 0),
		       u.deleted_at, u.deleted_by_admin_id, u.delete_reason
		FROM users u
		LEFT JOIN credit_accounts c ON c.user_id = u.id
		WHERE `+strings.Join(pageWhere, " AND ")+`
		ORDER BY u.created_at DESC, u.id DESC
		LIMIT $`+strconv.Itoa(len(pageArgs)), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list := []domain.UserView{}
	for rows.Next() {
		var v domain.UserView
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.Instagram, &v.Phone, &v.Telegram, &v.CreatedAt, &v.BalanceCents,
			&v.DeletedAt, &v.DeletedByAdminID, &v.DeleteReason); err != nil {
			return nil, 0, err
		}
		list = append(list, v)
	}
	return list, total, rows.Err()
}

// ListDeletedWithCreditBalance devolve usuários soft-deleted pra aba Trash
// do superadmin. Inclui balance pra deixar óbvio quanto crédito foi parado.
func (r *UserRepo) ListDeletedWithCreditBalance(ctx context.Context, limit int) ([]domain.UserView, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.instagram,
		       COALESCE(u.phone, ''), COALESCE(u.telegram, ''),
		       u.created_at, COALESCE(c.balance_cents, 0),
		       u.deleted_at, u.deleted_by_admin_id, u.delete_reason
		FROM users u
		LEFT JOIN credit_accounts c ON c.user_id = u.id
		WHERE u.deleted_at IS NOT NULL
		ORDER BY u.deleted_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []domain.UserView{}
	for rows.Next() {
		var v domain.UserView
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.Instagram, &v.Phone, &v.Telegram, &v.CreatedAt, &v.BalanceCents,
			&v.DeletedAt, &v.DeletedByAdminID, &v.DeleteReason); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// SoftDeleteUser marca usuário como apagado. Vide order_repo.go pra contrato.
// Idempotente — não sobrescreve trilha original.
//
// NOTA: depois do soft-delete, queries de login (viralefy_auth) já filtram
// DeletedAt != NULL e bloqueiam sessão. /v1/me/* responde 401 igualmente
// porque o token original ainda é válido até expirar (TTL 15min) — pra
// invalidar imediatamente, o admin deve usar a tela de admin sessions
// (PHASE-9 hot-set, fora do escopo deste PR).
func (r *UserRepo) SoftDeleteUser(ctx context.Context, id, adminID, reason string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE users
		   SET deleted_at = COALESCE(deleted_at, NOW()),
		       deleted_by_admin_id = COALESCE(deleted_by_admin_id, $2),
		       delete_reason = COALESCE(delete_reason, NULLIF($3, ''))
		 WHERE id = $1`, id, adminID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// HardDeleteUser remove a row do DB. Só superadmin. O ON DELETE CASCADE
// dos FKs encadeia em orders, invoices, profiles, reviews, tickets — em
// outras palavras, EXPURGO TOTAL. UI deve sempre confirmar antes de chamar.
func (r *UserRepo) HardDeleteUser(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// RestoreUser tira o soft-delete (deleted_at = NULL). Idempotente.
func (r *UserRepo) RestoreUser(ctx context.Context, id string) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE users SET deleted_at=NULL, deleted_by_admin_id=NULL, delete_reason=NULL
		WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

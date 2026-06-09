package domain

import "context"

// Permissões granulares (RBAC). Formato "<recurso>:<ação>".
const (
	PermPlansRead       = "plans:read"
	PermPlansWrite      = "plans:write"
	PermGatewaysRead    = "gateways:read"
	PermGatewaysWrite   = "gateways:write"
	PermCurrenciesRead  = "currencies:read"
	PermCurrenciesWrite = "currencies:write"
	PermOrdersRead      = "orders:read"
	PermTicketsRead     = "tickets:read"
	PermTicketsWrite    = "tickets:write"
	PermReviewsRead     = "reviews:read"
	PermReviewsModerate = "reviews:moderate"
	PermAdminsManage    = "admins:manage"
	PermCouponsRead     = "coupons:read"
	PermCouponsWrite    = "coupons:write"
)

// RoleSuperadmin tem acesso total (bypass de permissão).
const RoleSuperadmin = "superadmin"

type Role struct {
	Code        string   `json:"code"`
	Label       string   `json:"label"`
	Permissions []string `json:"permissions"`
}

type RoleRepository interface {
	GetPermissions(ctx context.Context, roleCode string) ([]string, error)
	List(ctx context.Context) ([]Role, error)
}

// Principal é o sujeito autenticado do backoffice (RBAC/ABAC).
type Principal struct {
	AdminID     string   `json:"admin_id"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

// Can implementa a checagem RBAC. Superadmin tem bypass.
func (p Principal) Can(permission string) bool {
	if p.Role == RoleSuperadmin {
		return true
	}
	for _, perm := range p.Permissions {
		if perm == permission {
			return true
		}
	}
	return false
}

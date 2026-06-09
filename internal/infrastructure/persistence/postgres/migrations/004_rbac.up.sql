-- RBAC: papéis e permissões granulares para o admin (backoffice).
CREATE TABLE IF NOT EXISTS roles (
    code TEXT PRIMARY KEY,
    label TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_code  TEXT NOT NULL REFERENCES roles(code) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (role_code, permission)
);

ALTER TABLE admins ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'superadmin';

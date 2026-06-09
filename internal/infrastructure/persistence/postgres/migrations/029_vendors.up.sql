-- Multi-vendor scaffold (Fase 7.4 — foundation).
--
-- Vendors são parceiros que podem registrar planos no catálogo. Por enquanto
-- só temos o data model + CRUD admin — settlement split (computar/pagar a
-- parte do vendor por transação paga) fica como roadmap v2.5.
--
-- revenue_share_pct é o % do GMV que vai pro vendor (70% default — restante
-- é receita da plataforma). Está em vendors (não em plans) pra simplificar
-- o MVP: 1 vendor → 1 share. Caso real precise override por plano, criar
-- coluna em plans depois sem migration destrutiva.
--
-- plans.vendor_id é NULLABLE: planos próprios da Viralefy ficam sem vendor.
-- O index parcial cobre só rows com vendor_id, evitando bloat no caso comum.

BEGIN;

CREATE TABLE IF NOT EXISTS vendors (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    contact_email     TEXT NOT NULL UNIQUE,
    revenue_share_pct NUMERIC(5,2) NOT NULL DEFAULT 70.00 CHECK (revenue_share_pct BETWEEN 0 AND 100),
    active            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE plans ADD COLUMN IF NOT EXISTS vendor_id TEXT REFERENCES vendors(id);

CREATE INDEX IF NOT EXISTS idx_plans_vendor ON plans(vendor_id) WHERE vendor_id IS NOT NULL;

COMMIT;

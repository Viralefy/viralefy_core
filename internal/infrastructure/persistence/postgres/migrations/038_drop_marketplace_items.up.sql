-- Remove categorias e planos de marketplace: BMs Facebook, perfis
-- envelhecidos e packs de e-mail validado. Decisão de produto 2026-06-09.
--
-- IMPORTANTE: a migration NÃO faz hard delete em planos que já tenham
-- referência de orders (FK plans → orders bloqueia). Estratégia:
--   1) DELETE de planos sem orders (limpeza propriamente dita)
--   2) UPDATE active=false nos remanescentes (some do storefront via
--      WHERE active=true; histórico de orders preservado)
--   3) Mesmo tratamento pra categorias (DELETE se vazia, UPDATE active=false
--      se ainda tiver plano herdado)

BEGIN;

-- Planos: tenta DELETE só dos que não têm orders/subscriptions associadas.
DELETE FROM plans p
WHERE p.category IN ('bms_facebook', 'perfis_redes', 'emails_validados')
  AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.plan_id = p.id)
  AND NOT EXISTS (SELECT 1 FROM subscriptions s WHERE s.plan_id = p.id);

-- Planos remanescentes (com histórico): só desativa.
UPDATE plans
SET active = false, updated_at = NOW()
WHERE category IN ('bms_facebook', 'perfis_redes', 'emails_validados')
  AND active = true;

-- Categorias: DELETE só se a categoria não tem mais nenhum plano (incluindo
-- inativos), senão UPDATE active=false (continua no schema mas some da loja).
DELETE FROM categories c
WHERE c.code IN ('bms_facebook', 'perfis_redes', 'emails_validados')
  AND NOT EXISTS (SELECT 1 FROM plans p WHERE p.category = c.code);

UPDATE categories
SET active = false
WHERE code IN ('bms_facebook', 'perfis_redes', 'emails_validados')
  AND active = true;

COMMIT;

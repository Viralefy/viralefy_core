-- Reativa as categorias e planos (sem re-inserir — só toggle).
-- O re-seed completo voltaria via seed.go (já removido, requer revert manual).

UPDATE categories
SET active = true
WHERE code IN ('bms_facebook', 'perfis_redes', 'emails_validados');

UPDATE plans
SET active = true, updated_at = NOW()
WHERE category IN ('bms_facebook', 'perfis_redes', 'emails_validados');

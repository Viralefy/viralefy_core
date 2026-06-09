-- Split engajamento_{instagram,tiktok} into 3 sub-categories per platform:
-- curtidas_*, comentarios_*, compartilhamentos_*. Saves do Instagram caem em
-- compartilhamentos_instagram (espalhamento + preservação são a mesma família
-- de "viral spread" pra fins de SEO e UX).
--
-- Estratégia:
--   1. Recategorizar plans existentes via heurística do nome (likes, comments,
--      shares, saves) — case-insensitive. Engagement só tem essas 4 famílias.
--   2. Inserir as 6 novas categorias no catálogo.
--   3. Deletar engajamento_{instagram,tiktok} do catálogo (sem plans órfãos
--      porque o step 1 já moveu todos eles).
--
-- 100% idempotente: re-runs não afetam plans que já estão nos códigos novos.

BEGIN;

-- ---- 1. Recategorizar plans existentes -----------------------------------
-- Likes
UPDATE plans SET category = 'curtidas_instagram'
WHERE category = 'engajamento_instagram' AND name ILIKE '%like%';
UPDATE plans SET category = 'curtidas_tiktok'
WHERE category = 'engajamento_tiktok' AND name ILIKE '%like%';

-- Comments
UPDATE plans SET category = 'comentarios_instagram'
WHERE category = 'engajamento_instagram' AND name ILIKE '%comment%';
UPDATE plans SET category = 'comentarios_tiktok'
WHERE category = 'engajamento_tiktok' AND name ILIKE '%comment%';

-- Shares + Saves (Instagram tem ambos; TikTok só shares)
UPDATE plans SET category = 'compartilhamentos_instagram'
WHERE category = 'engajamento_instagram' AND (name ILIKE '%share%' OR name ILIKE '%save%');
UPDATE plans SET category = 'compartilhamentos_tiktok'
WHERE category = 'engajamento_tiktok' AND name ILIKE '%share%';

-- ---- 2. Inserir novas categorias no catálogo -----------------------------
-- sort_order: planejado para a ordem
--   seguidores_instagram (1), seguidores_tiktok (2),
--   curtidas_instagram (3), curtidas_tiktok (4),
--   comentarios_instagram (5), comentarios_tiktok (6),
--   compartilhamentos_instagram (7), compartilhamentos_tiktok (8),
--   visualizacoes_instagram (9), visualizacoes_tiktok (10),
--   servicos (11)
INSERT INTO categories (code, label, sort_order, active) VALUES
  ('curtidas_instagram', 'Instagram likes', 3, true),
  ('curtidas_tiktok', 'TikTok likes', 4, true),
  ('comentarios_instagram', 'Instagram comments', 5, true),
  ('comentarios_tiktok', 'TikTok comments', 6, true),
  ('compartilhamentos_instagram', 'Instagram shares', 7, true),
  ('compartilhamentos_tiktok', 'TikTok shares', 8, true)
ON CONFLICT (code) DO UPDATE SET
  label = EXCLUDED.label,
  sort_order = EXCLUDED.sort_order,
  active = EXCLUDED.active;

-- Atualizar sort_order das categorias restantes para refletir a nova ordem
UPDATE categories SET sort_order = 9 WHERE code = 'visualizacoes_instagram';
UPDATE categories SET sort_order = 10 WHERE code = 'visualizacoes_tiktok';
UPDATE categories SET sort_order = 11 WHERE code = 'servicos';

-- ---- 3. Limpar engajamento_* do catálogo ---------------------------------
-- Por segurança, só remove se realmente não tem mais nenhum plan apontando.
DELETE FROM categories
 WHERE code IN ('engajamento_instagram', 'engajamento_tiktok')
   AND NOT EXISTS (SELECT 1 FROM plans WHERE plans.category = categories.code);

COMMIT;

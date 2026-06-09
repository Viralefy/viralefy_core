-- Reverte 008: re-mescla as 6 sub-categorias de engagement em
-- engajamento_{instagram,tiktok}.

BEGIN;

INSERT INTO categories (code, label, sort_order, active) VALUES
  ('engajamento_instagram', 'Instagram engagement', 3, true),
  ('engajamento_tiktok', 'TikTok engagement', 4, true)
ON CONFLICT (code) DO UPDATE SET active = true;

UPDATE plans SET category = 'engajamento_instagram'
 WHERE category IN ('curtidas_instagram', 'comentarios_instagram', 'compartilhamentos_instagram');
UPDATE plans SET category = 'engajamento_tiktok'
 WHERE category IN ('curtidas_tiktok', 'comentarios_tiktok', 'compartilhamentos_tiktok');

DELETE FROM categories
 WHERE code IN (
   'curtidas_instagram', 'curtidas_tiktok',
   'comentarios_instagram', 'comentarios_tiktok',
   'compartilhamentos_instagram', 'compartilhamentos_tiktok'
 );

UPDATE categories SET sort_order = 5 WHERE code = 'visualizacoes_instagram';
UPDATE categories SET sort_order = 6 WHERE code = 'visualizacoes_tiktok';
UPDATE categories SET sort_order = 7 WHERE code = 'servicos';

COMMIT;

-- 037_user_contact — phone + telegram obrigatórios no register (pelo menos um).
-- Canal alternativo de contato quando email cai em spam ou cliente não checa
-- inbox. Suporte usa pra contornar disputas e reduzir refund por "nunca
-- recebi notificação".
--
-- NULLs aceitos no schema porque (a) row é criada antes do register
-- (autocadastro no checkout cria user sem esses campos imediatamente, depois
-- o cliente informa), e (b) admin pode editar manualmente. Validação
-- "pelo menos um" mora no handler — não em CHECK constraint, pra manter
-- migração reversível sem perder dados.
BEGIN;

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS phone    TEXT,
  ADD COLUMN IF NOT EXISTS telegram TEXT;

COMMIT;

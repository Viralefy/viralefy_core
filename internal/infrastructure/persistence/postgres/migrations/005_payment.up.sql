-- Dados do pagamento criados via gateway (PIX/cripto). payment_url leva o
-- cliente para a tela de pagamento (ou contém o código copia-e-cola PIX/
-- carteira cripto, dependendo do provider). payment_extra mantém o "extras"
-- (br_code, qr_code_image, wallet_address, network etc.) em JSON.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_url TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS payment_extra JSONB NOT NULL DEFAULT '{}'::jsonb;

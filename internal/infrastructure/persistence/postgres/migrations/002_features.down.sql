DROP INDEX IF EXISTS idx_plans_category;
ALTER TABLE orders DROP COLUMN IF EXISTS settlement_amount;
ALTER TABLE orders DROP COLUMN IF EXISTS settlement_currency;
ALTER TABLE orders DROP COLUMN IF EXISTS display_amount;
ALTER TABLE orders DROP COLUMN IF EXISTS display_currency;
ALTER TABLE plans DROP COLUMN IF EXISTS category;
DROP TABLE IF EXISTS currencies;
DROP TABLE IF EXISTS categories;

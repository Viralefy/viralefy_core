-- Customer reviews — colhidos via email pós-entrega (7d após paid).
--
-- Restrições críticas (Google Search Console policy):
--   - 1 review por order (UNIQUE) — sem fabricação ou duplicação.
--   - rating 1..5 inteiro.
--   - order.status = 'paid' validado no service (não no DB) pra simplificar.
--
-- Denormaliza plan_id, plan_category, country_code pra aggregateRating
-- queries serem só índice + GROUP BY (sem JOIN de 3 tabelas em cada
-- carregamento de página de plano).

BEGIN;

CREATE TABLE IF NOT EXISTS reviews (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL REFERENCES users(id),
    order_id        TEXT NOT NULL UNIQUE REFERENCES orders(id),
    plan_id         TEXT NOT NULL REFERENCES plans(id),
    plan_category   TEXT NOT NULL,
    country_code    TEXT NOT NULL,            -- alpha-2, lowercase (br, us, ...)
    rating          INT  NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title           TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL DEFAULT '',
    visible         BOOLEAN NOT NULL DEFAULT TRUE,  -- moderação manual via admin
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reviews_plan         ON reviews(plan_id)        WHERE visible = TRUE;
CREATE INDEX IF NOT EXISTS idx_reviews_category     ON reviews(plan_category)  WHERE visible = TRUE;
CREATE INDEX IF NOT EXISTS idx_reviews_country      ON reviews(country_code)   WHERE visible = TRUE;
CREATE INDEX IF NOT EXISTS idx_reviews_created      ON reviews(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reviews_user         ON reviews(user_id);

-- Tracking de email enviado pra evitar spam. NULL = nunca enviado.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS review_email_sent_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_orders_review_email
  ON orders(status, review_email_sent_at)
  WHERE status = 'paid';

COMMIT;

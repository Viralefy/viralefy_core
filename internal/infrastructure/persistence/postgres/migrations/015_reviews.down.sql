BEGIN;

DROP INDEX IF EXISTS idx_orders_review_email;
ALTER TABLE orders DROP COLUMN IF EXISTS review_email_sent_at;

DROP INDEX IF EXISTS idx_reviews_user;
DROP INDEX IF EXISTS idx_reviews_created;
DROP INDEX IF EXISTS idx_reviews_country;
DROP INDEX IF EXISTS idx_reviews_category;
DROP INDEX IF EXISTS idx_reviews_plan;

DROP TABLE IF EXISTS reviews;

COMMIT;

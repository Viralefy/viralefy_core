package postgres

import (
	"context"
	"os"
	"strconv"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func Seed(ctx context.Context, db *DB) error {
	if err := seedCategories(ctx, db); err != nil {
		return err
	}
	if err := seedCurrencies(ctx, db); err != nil {
		return err
	}
	if err := seedPlans(ctx, db); err != nil {
		return err
	}
	if err := seedGateway(ctx, db); err != nil {
		return err
	}
	if err := seedRoles(ctx, db); err != nil {
		return err
	}
	return seedAdmin(ctx, db)
}

func seedRoles(ctx context.Context, db *DB) error {
	roles := []struct {
		code, label string
		perms       []string
	}{
		{"superadmin", "Super Admin", []string{
			"plans:read", "plans:write", "gateways:read", "gateways:write",
			"currencies:read", "currencies:write", "orders:read",
			"tickets:read", "tickets:write",
			"reviews:read", "reviews:moderate",
			"admins:manage",
		}},
		{"manager", "Gerente", []string{
			"plans:read", "plans:write", "gateways:read", "gateways:write",
			"currencies:read", "currencies:write", "orders:read",
			"tickets:read", "tickets:write",
			"reviews:read", "reviews:moderate",
		}},
		{"support", "Suporte", []string{
			"plans:read", "gateways:read", "currencies:read", "orders:read",
			"tickets:read", "tickets:write",
			"reviews:read",
		}},
		{"viewer", "Leitura", []string{
			"plans:read", "gateways:read", "currencies:read", "orders:read",
			"tickets:read",
			"reviews:read",
		}},
	}
	for _, r := range roles {
		// DO NOTHING (não UPDATE): admin pode renomear roles no backoffice sem
		// que o boot ressuscite o label antigo. Mudança de seed → migration nova.
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO roles (code, label) VALUES ($1,$2)
			ON CONFLICT (code) DO NOTHING`, r.code, r.label); err != nil {
			return err
		}
		for _, p := range r.perms {
			if _, err := db.pool.Exec(ctx, `
				INSERT INTO role_permissions (role_code, permission) VALUES ($1,$2)
				ON CONFLICT DO NOTHING`, r.code, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func seedCategories(ctx context.Context, db *DB) error {
	cats := []struct {
		code, label string
		order       int
	}{
		{"seguidores_instagram", "Instagram followers", 1},
		{"seguidores_tiktok", "TikTok followers", 2},
		{"curtidas_instagram", "Instagram likes", 3},
		{"curtidas_tiktok", "TikTok likes", 4},
		{"comentarios_instagram", "Instagram comments", 5},
		{"comentarios_tiktok", "TikTok comments", 6},
		{"compartilhamentos_instagram", "Instagram shares", 7},
		{"compartilhamentos_tiktok", "TikTok shares", 8},
		{"visualizacoes_instagram", "Instagram views", 9},
		{"visualizacoes_tiktok", "TikTok views", 10},
		{"servicos", "Premium services", 11},
		// Marketplace de recovery — única categoria não-engajamento mantida.
		// Categorias bms_facebook/perfis_redes/emails_validados foram REMOVIDAS
		// do catálogo (decisão de produto 2026-06-09). O ON CONFLICT do INSERT
		// acima não desativa rows existentes — limpeza em prod via SQL
		// (DELETE/UPDATE active=false) no migration ou backoffice.
		{"recuperacao_perfil", "Account recovery", 12},
	}
	for _, c := range cats {
		// DO NOTHING — admin pode renomear/reordenar categorias sem que o
		// boot ressuscite valores antigos. Mudança de seed real = migration.
		_, err := db.pool.Exec(ctx, `
			INSERT INTO categories (code, label, sort_order, active)
			VALUES ($1,$2,$3,true) ON CONFLICT (code) DO NOTHING`,
			c.code, c.label, c.order)
		if err != nil {
			return err
		}
	}
	return nil
}

func seedCurrencies(ctx context.Context, db *DB) error {
	// rate = unidades da moeda por 1 USD. Base = USD (rate 1).
	// Migração 011 fez o switch BRL-base → USD-base; mantemos espelhado aqui
	// pros fresh installs.
	curs := []struct {
		code, name, symbol, kind, settlement string
		rate                                 float64
		decimals, order                      int
		display                              bool
	}{
		// USDT é a moeda padrão da storefront. Símbolo "$" porque é 1:1
		// com USD e ficaria estranho mostrar "₮ 5,00" pro mundo todo.
		//
		// settlement_code aponta pra moeda em que a plataforma efetivamente
		// liquida. Pra cripto auto (Heleket), TODAS settle em USDT por
		// default — o processor converte na entrada.
		// display_enabled=true expõe no picker do front; false esconde do
		// picker mas mantém disponível como currency de cobrança no checkout
		// (cliente nunca veria preço em LTC, mas pode optar por pagar com LTC).
		{"USDT", "Tether", "$", "crypto", "USDT", 1.0, 2, 1, true},
		{"USD", "Dólar", "$", "fiat", "USDT", 1.0, 2, 2, true},
		{"EUR", "Euro", "€", "fiat", "EUR", 0.92, 2, 3, true},
		{"BRL", "Real", "R$", "fiat", "BRL", 5.41, 2, 4, true},
		{"GBP", "Libra", "£", "fiat", "GBP", 0.79, 2, 5, false},
		// Cryptos — pay-in para gateways automáticos (Heleket) ou manual_crypto.
		// settlement_code=USDT pra todas: plataforma sempre liquida em USDT.
		// Rates aproximadas (USD-base, unidades da moeda por 1 USD); cron de
		// drift atualiza periodicamente.
		{"BTC", "Bitcoin", "₿", "crypto", "USDT", 0.0000103, 8, 10, true},
		{"ETH", "Ethereum", "Ξ", "crypto", "USDT", 0.00028, 6, 11, false},
		{"LTC", "Litecoin", "Ł", "crypto", "USDT", 0.0093, 6, 12, false},
		{"BNB", "BNB", "BNB", "crypto", "USDT", 0.0014, 5, 13, false},
		{"SOL", "Solana", "◎", "crypto", "USDT", 0.0050, 5, 14, false},
		{"TRX", "Tron", "TRX", "crypto", "USDT", 4.05, 3, 15, false},
		{"MATIC", "Polygon", "MATIC", "crypto", "USDT", 2.20, 3, 16, false},
		{"XRP", "Ripple", "XRP", "crypto", "USDT", 1.65, 4, 17, false},
		{"DOGE", "Dogecoin", "Ð", "crypto", "USDT", 4.85, 3, 18, false},
		{"ADA", "Cardano", "ADA", "crypto", "USDT", 1.85, 3, 19, false},
		// Stablecoins alternativas — útil pra gateways que aceitam USDC.
		{"USDC", "USD Coin", "$", "crypto", "USDT", 1.0, 2, 20, false},
		{"DAI", "Dai", "$", "crypto", "USDT", 1.0, 2, 21, false},
	}
	for _, c := range curs {
		_, err := db.pool.Exec(ctx, `
			INSERT INTO currencies (code, name, symbol, rate, decimals, kind, display_enabled, settlement_code, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (code) DO NOTHING`,
			c.code, c.name, c.symbol, c.rate, c.decimals, c.kind, c.display, c.settlement, c.order)
		if err != nil {
			return err
		}
	}
	return nil
}

func seedPlans(ctx context.Context, db *DB) error {
	// Idempotent by (category, name). USD is the canonical price; per-currency
	// amounts in plan_prices are derived from USD via fixed inline rates in
	// seedPlanPrices. Re-runs refresh description/price/sort_order so the seed
	// stays authoritative even if labels evolve.
	type planSeed struct {
		name, desc, category, platform, target string
		qty                                    int
		usd                                    float64
		order                                  int
	}
	plans := []planSeed{
		// ===== INSTAGRAM FOLLOWERS (profile) =====
		{"100 followers Instagram", "Ideal for testing", "seguidores_instagram", "instagram", "profile", 100, 2.50, 1},
		{"250 followers Instagram", "First push", "seguidores_instagram", "instagram", "profile", 250, 5, 2},
		{"500 followers Instagram", "Initial growth", "seguidores_instagram", "instagram", "profile", 500, 9, 5},
		{"750 followers Instagram", "Steady boost", "seguidores_instagram", "instagram", "profile", 750, 13, 7},
		{"1,000 followers Instagram", "More reach", "seguidores_instagram", "instagram", "profile", 1000, 15, 10},
		{"1,500 followers Instagram", "Growing presence", "seguidores_instagram", "instagram", "profile", 1500, 22, 15},
		{"2,500 followers Instagram", "Traction", "seguidores_instagram", "instagram", "profile", 2500, 33, 25},
		{"5,000 followers Instagram", "Community", "seguidores_instagram", "instagram", "profile", 5000, 60, 50},
		{"7,500 followers Instagram", "Real audience", "seguidores_instagram", "instagram", "profile", 7500, 85, 75},
		{"10,000 followers Instagram", "Scale", "seguidores_instagram", "instagram", "profile", 10000, 100, 100},
		{"15,000 followers Instagram", "Strong influence", "seguidores_instagram", "instagram", "profile", 15000, 135, 150},
		{"25,000 followers Instagram", "Micro-influencer", "seguidores_instagram", "instagram", "profile", 25000, 200, 250},
		{"50,000 followers Instagram", "Established influence", "seguidores_instagram", "instagram", "profile", 50000, 350, 500},
		{"75,000 followers Instagram", "Major presence", "seguidores_instagram", "instagram", "profile", 75000, 475, 750},
		{"100,000 followers Instagram", "Authority", "seguidores_instagram", "instagram", "profile", 100000, 600, 1000},
		{"250,000 followers Instagram", "Personal brand", "seguidores_instagram", "instagram", "profile", 250000, 1250, 2500},
		{"500,000 followers Instagram", "Top tier", "seguidores_instagram", "instagram", "profile", 500000, 2250, 5000},
		{"1,000,000 followers Instagram", "Maximum reach", "seguidores_instagram", "instagram", "profile", 1000000, 4000, 10000},

		// ===== TIKTOK FOLLOWERS (profile) =====
		{"100 followers TikTok", "Ideal for testing", "seguidores_tiktok", "tiktok", "profile", 100, 5, 1},
		{"250 followers TikTok", "First push", "seguidores_tiktok", "tiktok", "profile", 250, 10, 2},
		{"500 followers TikTok", "Initial growth", "seguidores_tiktok", "tiktok", "profile", 500, 18, 5},
		{"750 followers TikTok", "Steady boost", "seguidores_tiktok", "tiktok", "profile", 750, 26, 7},
		{"1,000 followers TikTok", "Takeoff", "seguidores_tiktok", "tiktok", "profile", 1000, 30, 10},
		{"1,500 followers TikTok", "Growing presence", "seguidores_tiktok", "tiktok", "profile", 1500, 44, 15},
		{"2,500 followers TikTok", "Traction", "seguidores_tiktok", "tiktok", "profile", 2500, 66, 25},
		{"5,000 followers TikTok", "Solid growth", "seguidores_tiktok", "tiktok", "profile", 5000, 120, 50},
		{"7,500 followers TikTok", "Real audience", "seguidores_tiktok", "tiktok", "profile", 7500, 170, 75},
		{"10,000 followers TikTok", "TikTok scale", "seguidores_tiktok", "tiktok", "profile", 10000, 200, 100},

		// ===== INSTAGRAM LIKES (publication) =====
		{"100 likes Instagram", "Initial boost", "curtidas_instagram", "instagram", "publication", 100, 1, 1},
		{"250 likes Instagram", "Early traction", "curtidas_instagram", "instagram", "publication", 250, 2, 2},
		{"500 likes Instagram", "Average engagement", "curtidas_instagram", "instagram", "publication", 500, 3, 5},
		{"1,000 likes Instagram", "High visibility", "curtidas_instagram", "instagram", "publication", 1000, 5, 10},
		{"2,500 likes Instagram", "Picking up", "curtidas_instagram", "instagram", "publication", 2500, 11, 25},
		{"5,000 likes Instagram", "Going viral", "curtidas_instagram", "instagram", "publication", 5000, 20, 50},
		{"7,500 likes Instagram", "Trending fast", "curtidas_instagram", "instagram", "publication", 7500, 28, 75},
		{"10,000 likes Instagram", "Trending", "curtidas_instagram", "instagram", "publication", 10000, 35, 100},
		{"25,000 likes Instagram", "Hot post", "curtidas_instagram", "instagram", "publication", 25000, 80, 250},
		{"50,000 likes Instagram", "Top of feed", "curtidas_instagram", "instagram", "publication", 50000, 150, 500},
		{"100,000 likes Instagram", "Explosive", "curtidas_instagram", "instagram", "publication", 100000, 280, 1000},

		// ===== INSTAGRAM COMMENTS (publication) =====
		{"25 comments Instagram", "Light conversation", "comentarios_instagram", "instagram", "publication", 25, 5, 1},
		{"50 comments Instagram", "Conversation starter", "comentarios_instagram", "instagram", "publication", 50, 9, 2},
		{"100 comments Instagram", "Real engagement", "comentarios_instagram", "instagram", "publication", 100, 15, 3},
		{"250 comments Instagram", "Active discussion", "comentarios_instagram", "instagram", "publication", 250, 35, 5},
		{"500 comments Instagram", "Community talk", "comentarios_instagram", "instagram", "publication", 500, 65, 10},
		{"1,000 comments Instagram", "Viral debate", "comentarios_instagram", "instagram", "publication", 1000, 120, 20},

		// ===== INSTAGRAM SHARES + SAVES (publication) =====
		// Shares e saves caem juntos em "compartilhamentos" — ambos são sinais
		// de "espalhamento" e dividem a mesma página SEO.
		{"100 shares Instagram", "Spread the word", "compartilhamentos_instagram", "instagram", "publication", 100, 4, 1},
		{"250 shares Instagram", "Early diffusion", "compartilhamentos_instagram", "instagram", "publication", 250, 9, 3},
		{"500 shares Instagram", "Extra reach", "compartilhamentos_instagram", "instagram", "publication", 500, 16, 5},
		{"1,000 shares Instagram", "Trending content", "compartilhamentos_instagram", "instagram", "publication", 1000, 30, 10},
		{"2,500 shares Instagram", "Wide reach", "compartilhamentos_instagram", "instagram", "publication", 2500, 70, 25},
		{"5,000 shares Instagram", "Real virality", "compartilhamentos_instagram", "instagram", "publication", 5000, 130, 50},
		{"100 saves Instagram", "Valuable content", "compartilhamentos_instagram", "instagram", "publication", 100, 3, 2},
		{"250 saves Instagram", "Useful post", "compartilhamentos_instagram", "instagram", "publication", 250, 7, 4},
		{"500 saves Instagram", "Reference material", "compartilhamentos_instagram", "instagram", "publication", 500, 13, 6},
		{"1,000 saves Instagram", "Top of mind", "compartilhamentos_instagram", "instagram", "publication", 1000, 25, 11},
		{"2,500 saves Instagram", "Bookmark-worthy", "compartilhamentos_instagram", "instagram", "publication", 2500, 55, 26},
		{"5,000 saves Instagram", "Evergreen content", "compartilhamentos_instagram", "instagram", "publication", 5000, 100, 51},

		// ===== TIKTOK LIKES (publication) =====
		{"100 likes TikTok", "Initial boost", "curtidas_tiktok", "tiktok", "publication", 100, 2, 1},
		{"250 likes TikTok", "Early traction", "curtidas_tiktok", "tiktok", "publication", 250, 4, 2},
		{"500 likes TikTok", "Video boost", "curtidas_tiktok", "tiktok", "publication", 500, 6, 5},
		{"1,000 likes TikTok", "Visibility", "curtidas_tiktok", "tiktok", "publication", 1000, 10, 10},
		{"2,500 likes TikTok", "Picking up", "curtidas_tiktok", "tiktok", "publication", 2500, 22, 25},
		{"5,000 likes TikTok", "Trending", "curtidas_tiktok", "tiktok", "publication", 5000, 40, 50},
		{"7,500 likes TikTok", "Trending fast", "curtidas_tiktok", "tiktok", "publication", 7500, 56, 75},
		{"10,000 likes TikTok", "For You page", "curtidas_tiktok", "tiktok", "publication", 10000, 70, 100},
		{"25,000 likes TikTok", "Hot video", "curtidas_tiktok", "tiktok", "publication", 25000, 160, 250},

		// ===== TIKTOK COMMENTS (publication) =====
		{"25 comments TikTok", "Light conversation", "comentarios_tiktok", "tiktok", "publication", 25, 10, 1},
		{"50 comments TikTok", "Conversation starter", "comentarios_tiktok", "tiktok", "publication", 50, 18, 2},
		{"100 comments TikTok", "Real engagement", "comentarios_tiktok", "tiktok", "publication", 100, 30, 3},
		{"250 comments TikTok", "Active discussion", "comentarios_tiktok", "tiktok", "publication", 250, 70, 5},
		{"500 comments TikTok", "Community talk", "comentarios_tiktok", "tiktok", "publication", 500, 130, 10},

		// ===== TIKTOK SHARES (publication) =====
		{"100 shares TikTok", "Spread the word", "compartilhamentos_tiktok", "tiktok", "publication", 100, 8, 1},
		{"250 shares TikTok", "Early diffusion", "compartilhamentos_tiktok", "tiktok", "publication", 250, 18, 3},
		{"500 shares TikTok", "Extra reach", "compartilhamentos_tiktok", "tiktok", "publication", 500, 32, 5},
		{"1,000 shares TikTok", "Trending content", "compartilhamentos_tiktok", "tiktok", "publication", 1000, 60, 10},
		{"2,500 shares TikTok", "Wide reach", "compartilhamentos_tiktok", "tiktok", "publication", 2500, 140, 25},
		{"5,000 shares TikTok", "Real virality", "compartilhamentos_tiktok", "tiktok", "publication", 5000, 260, 50},

		// ===== INSTAGRAM VIEWS =====
		// Reels (publication)
		{"1,000 Reels views Instagram", "Initial pickup", "visualizacoes_instagram", "instagram", "publication", 1000, 1.50, 10},
		{"5,000 Reels views Instagram", "Building momentum", "visualizacoes_instagram", "instagram", "publication", 5000, 7, 50},
		{"10,000 Reels views Instagram", "Trending", "visualizacoes_instagram", "instagram", "publication", 10000, 13, 100},
		{"25,000 Reels views Instagram", "Picking up heat", "visualizacoes_instagram", "instagram", "publication", 25000, 30, 250},
		{"50,000 Reels views Instagram", "Hot", "visualizacoes_instagram", "instagram", "publication", 50000, 55, 500},
		{"100,000 Reels views Instagram", "Boom", "visualizacoes_instagram", "instagram", "publication", 100000, 100, 1000},
		{"250,000 Reels views Instagram", "Massive reach", "visualizacoes_instagram", "instagram", "publication", 250000, 230, 2500},
		{"500,000 Reels views Instagram", "Viral", "visualizacoes_instagram", "instagram", "publication", 500000, 450, 5000},
		{"1,000,000 Reels views Instagram", "National hit", "visualizacoes_instagram", "instagram", "publication", 1000000, 800, 10000},
		// Story (profile)
		{"500 Story views Instagram", "Story boost", "visualizacoes_instagram", "instagram", "profile", 500, 3, 5},
		{"1,000 Story views Instagram", "Solid story reach", "visualizacoes_instagram", "instagram", "profile", 1000, 5, 10},
		{"2,500 Story views Instagram", "High presence", "visualizacoes_instagram", "instagram", "profile", 2500, 11, 25},
		{"5,000 Story views Instagram", "Strong story", "visualizacoes_instagram", "instagram", "profile", 5000, 20, 50},
		{"10,000 Story views Instagram", "Massive", "visualizacoes_instagram", "instagram", "profile", 10000, 35, 100},
		{"25,000 Story views Instagram", "Top story reach", "visualizacoes_instagram", "instagram", "profile", 25000, 80, 250},

		// ===== TIKTOK VIEWS (publication) =====
		{"10,000 video views TikTok", "Pickup", "visualizacoes_tiktok", "tiktok", "publication", 10000, 20, 100},
		{"25,000 video views TikTok", "Building momentum", "visualizacoes_tiktok", "tiktok", "publication", 25000, 45, 250},
		{"50,000 video views TikTok", "Hot video", "visualizacoes_tiktok", "tiktok", "publication", 50000, 85, 500},
		{"100,000 video views TikTok", "Trending", "visualizacoes_tiktok", "tiktok", "publication", 100000, 160, 1000},
		{"250,000 video views TikTok", "Massive reach", "visualizacoes_tiktok", "tiktok", "publication", 250000, 380, 2500},
		{"500,000 video views TikTok", "Viral", "visualizacoes_tiktok", "tiktok", "publication", 500000, 750, 5000},
		{"1,000,000 video views TikTok", "Mega viral", "visualizacoes_tiktok", "tiktok", "publication", 1000000, 1400, 10000},

		// ===== PREMIUM SERVICES (consulting — profile) =====
		// Account recovery saiu de servicos pra própria categoria abaixo.
		{"Profile audit", "Diagnosis + recommendations", "servicos", "instagram", "profile", 1, 39, 1},
		{"Monthly management", "Profile management + strategy", "servicos", "instagram", "profile", 1, 99, 2},
		{"Product launch", "Integrated 30-day campaign", "servicos", "instagram", "profile", 1, 499, 3},
		{"New account setup", "Full setup and optimization for a new account", "servicos", "instagram", "profile", 1, 149, 5},
		{"Anti-shadowban package", "Shadowban diagnosis and removal plan", "servicos", "instagram", "profile", 1, 129, 6},
		{"Competitor analysis", "In-depth analysis of direct competitors", "servicos", "instagram", "profile", 1, 79, 7},
		{"Verification support", "Support to apply for the verified badge", "servicos", "instagram", "profile", 1, 299, 8},

		// ===== ACCOUNT RECOVERY (LP dedicada por país) ============================
		// Único item nessa categoria — preço alto reflete o esforço de
		// negociação direta com Meta/ByteDance e o risco operacional. Compra
		// abre ticket automático (handler em application/order).
		{"Account recovery", "Full account recovery — Instagram/TikTok suspended, hacked or restricted", "recuperacao_perfil", "instagram", "profile", 1, 10000, 1},

		// REMOVIDOS (2026-06-09): planos de marketplace de BMs do Facebook,
		// perfis envelhecidos e packs de e-mails validados. Decisão de produto.
		// A limpeza física dos rows em prod é feita pela migration 038
		// (ver migrations/038_drop_marketplace_items.up.sql).
	}
	for _, p := range plans {
		var existingID string
		// UPSERT por (category, name) — name é o identificador único do plano
		// dentro da categoria. UNIQUE em (category, name) é o equivalente físico
		// na DB (plans_category_name_key).
		_ = db.pool.QueryRow(ctx,
			`SELECT id FROM plans WHERE category=$1 AND name=$2 LIMIT 1`,
			p.category, p.name).Scan(&existingID)
		cents := int(p.usd*100 + 0.5)
		if existingID != "" {
			// Refresh description/price/sort_order/platform/target_type. Name fica
			// como está (lookup key), mas o resto vira authoritativo do seed.
			_, _ = db.pool.Exec(ctx,
				`UPDATE plans SET description=$2, price_cents=$3, sort_order=$4,
				                  platform=$5, target_type=$6, followers_qty=$7,
				                  currency='USD'
				 WHERE id=$1`,
				existingID, p.desc, cents, p.order, p.platform, p.target, p.qty)
			if err := seedPlanPrices(ctx, db, existingID, p.usd); err != nil {
				return err
			}
			continue
		}
		id := uuid.New().String()
		_, err := db.pool.Exec(ctx, `
			INSERT INTO plans (id, name, description, category, platform, target_type, followers_qty, price_cents, currency, active, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'USD',true,$9)`,
			id, p.name, p.desc, p.category, p.platform, p.target, p.qty, cents, p.order)
		if err != nil {
			return err
		}
		if err := seedPlanPrices(ctx, db, id, p.usd); err != nil {
			return err
		}
	}
	return nil
}

// seedPlanPrices gera os preços por moeda a partir do USD canônico usando
// as rates ATUAIS da tabela `currencies` (USD/USDT/EUR/BRL/BTC + qualquer
// outra que o admin tenha cadastrado).
//
// Antes lia rates hardcoded inline (BRL=5.41 fixo etc.) — qualquer edição
// no backoffice via /currencies era sobrescrita no próximo boot do API.
// Agora a tabela `currencies` é a fonte de verdade.
//
// UPSERT: re-runs atualizam o valor (mantém o seed autoritativo SEM ignorar
// edições de rate).
func seedPlanPrices(ctx context.Context, db *DB, planID string, usd float64) error {
	rows, err := db.pool.Query(ctx, `SELECT code, rate, decimals FROM currencies`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type rateRow struct {
		code     string
		rate     float64
		decimals int
	}
	var rates []rateRow
	for rows.Next() {
		var r rateRow
		if err := rows.Scan(&r.code, &r.rate, &r.decimals); err != nil {
			return err
		}
		rates = append(rates, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range rates {
		amount := strconv.FormatFloat(usd*c.rate, 'f', c.decimals, 64)
		// DO NOTHING — admin pode editar preços individuais sem que o boot
		// ressuscite o valor calculado pelo seed. Mudança em massa de preço
		// = migration nova ou comando admin (`viralefy-recalc-prices`).
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO plan_prices (plan_id, currency_code, amount) VALUES ($1,$2,$3)
			ON CONFLICT (plan_id, currency_code) DO NOTHING`,
			planID, c.code, amount); err != nil {
			return err
		}
	}
	return nil
}

func seedGateway(ctx context.Context, db *DB) error {
	// Idempotente por provider — adiciona o que faltar sem mexer no existente.
	gws := []struct {
		name, provider, config string
		active                 bool
	}{
		{"PIX Manual", "manual_pix", `{"pix_key":"contato@viralefy.com"}`, true},
		{"Woovi (PIX)", "woovi", `{"app_id":"","base_url":"https://api.woovi.com.br"}`, false},
		{"Heleket (cripto)", "heleket", `{"merchant_id":"","api_key":"","base_url":"https://api.heleket.com","url_callback":""}`, false},
	}
	for _, g := range gws {
		var exists int
		_ = db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM payment_gateways WHERE provider=$1`, g.provider).Scan(&exists)
		if exists > 0 {
			continue
		}
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO payment_gateways (id, name, provider, active, config)
			VALUES ($1,$2,$3,$4,$5::jsonb)`,
			uuid.New().String(), g.name, g.provider, g.active, g.config); err != nil {
			return err
		}
	}
	return nil
}

// seedAdmin cria o admin inicial se a tabela estiver vazia. Email e senha
// vêm do ambiente (ADMIN_BOOTSTRAP_EMAIL / ADMIN_BOOTSTRAP_PASSWORD). Sem
// essas variáveis o seed NÃO insere nada — antes hardcodava
// `admin@viralefy.local` / `SimTest!Admin2026`, o que vazava credencial
// real em HML (a senha do seed virava a do superadmin de prod).
func seedAdmin(ctx context.Context, db *DB) error {
	var n int
	_ = db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n)
	if n > 0 {
		return nil
	}
	email := os.Getenv("ADMIN_BOOTSTRAP_EMAIL")
	pass := os.Getenv("ADMIN_BOOTSTRAP_PASSWORD")
	if email == "" || pass == "" {
		// Sem bootstrap configurado: deixa a tabela vazia. Operador
		// promove um admin manualmente via SQL ou roda uma seed task
		// pontual com as envs setadas.
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), 12)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, `
		INSERT INTO admins (id, email, password_hash, name)
		VALUES ($1,$2,$3,'Administrador')`, uuid.New().String(), email, string(hash))
	return err
}

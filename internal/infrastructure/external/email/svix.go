package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrSvixSignatureMismatch é retornado quando nenhuma das versões/passes
// listados no header svix-signature bate com o HMAC computado.
var ErrSvixSignatureMismatch = errors.New("svix: signature mismatch")

// ErrSvixTimestampSkew é retornado quando o timestamp do header está fora
// da janela tolerada (5 min), prevenindo replay.
var ErrSvixTimestampSkew = errors.New("svix: timestamp out of tolerance")

// ErrSvixMissing é retornado quando um dos headers obrigatórios falta.
var ErrSvixMissing = errors.New("svix: missing required headers")

// svixTolerance é a janela máxima entre o timestamp do header e o relógio
// do servidor. Resend/Svix recomendam 5 minutos.
const svixTolerance = 5 * time.Minute

// VerifySvixSignature valida assinaturas Svix (formato usado pelo Resend).
//
// Conforme docs Svix:
//   - signed_payload = "{svix-id}.{svix-timestamp}.{body}"
//   - sig = base64(HMAC-SHA256(secret_raw, signed_payload))
//   - header svix-signature pode listar várias versões separadas por espaço,
//     cada uma no formato "v1,<base64>". Aceita-se se ALGUMA "v1,..." bater.
//   - secret vem como "whsec_<base64>"; decodificamos o base64 cru.
//
// Se secret está vazio retorna nil (caller decide skip — útil em HML).
func VerifySvixSignature(body []byte, svixID, svixTimestamp, svixSignature, secret string) error {
	if secret == "" {
		return nil
	}
	if svixID == "" || svixTimestamp == "" || svixSignature == "" {
		return ErrSvixMissing
	}

	// Janela anti-replay.
	tsInt, err := strconv.ParseInt(svixTimestamp, 10, 64)
	if err != nil {
		return ErrSvixMissing
	}
	now := time.Now().Unix()
	if diff := now - tsInt; diff > int64(svixTolerance.Seconds()) || diff < -int64(svixTolerance.Seconds()) {
		return ErrSvixTimestampSkew
	}

	// "whsec_<base64>" — recorta o prefixo se vier.
	raw := secret
	if strings.HasPrefix(raw, "whsec_") {
		raw = strings.TrimPrefix(raw, "whsec_")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// Tolerância: secret pode já estar em bytes crus (hex/raw).
		key = []byte(raw)
	}

	signedPayload := svixID + "." + svixTimestamp + "." + string(body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signedPayload))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Header pode conter "v1,sig1 v1,sig2 ..." — aceita se algum bater.
	for _, part := range strings.Fields(svixSignature) {
		comma := strings.IndexByte(part, ',')
		if comma < 0 {
			continue
		}
		ver, sig := part[:comma], part[comma+1:]
		if ver != "v1" {
			continue
		}
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return nil
		}
	}
	return ErrSvixSignatureMismatch
}

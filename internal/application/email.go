package application

import "context"

// EmailMessage é a mensagem de e-mail no modelo da aplicação.
type EmailMessage struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

// EmailSender é a porta de saída para envio de e-mail. A implementação
// concreta (SMTP) vive em infrastructure/external/email.
type EmailSender interface {
	Send(ctx context.Context, msg EmailMessage) error
}

// TemplatedEmailer é a porta opcional pra envio templated (PHASE-8 Wave 3).
// Quando o sender concreto é o microserviço viralefy_sender (via
// senderclient.Client), ele resolve template+vars in-flight (renderiza
// gohtml com layout/branding consistente). Implementações legadas (SMTP/
// Resend direto) NÃO implementam — caller faz fallback pro Send tradicional
// com HTML/Text já renderizado.
//
// Convenção: template é o nome registrado no sender ("checkout_paid",
// "proof_rejected", …). Vars vai pra map[string]string e alimenta a
// substituição no template. To é o email destino.
type TemplatedEmailer interface {
	SendTemplate(ctx context.Context, to, template string, vars map[string]string) error
}

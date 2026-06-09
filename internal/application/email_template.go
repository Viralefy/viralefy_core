package application

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// CheckoutEmailData é o que o template do e-mail de checkout precisa.
type CheckoutEmailData struct {
	LogoURL              string
	SiteURL              string
	Year                 int
	Name                 string
	Email                string
	PlanName             string
	DisplayCurrency      string
	DisplaySymbol        string
	DisplayAmount        string
	SettlementCurrency   string
	SettlementAmount     string
	AccountCreated       bool
	Password             string
	BrCode               string
	QrImage              string
	CryptoAddress        string
	CryptoNetwork        string
	PixKey               string
	PaymentURL           string
	FallbackInstructions string
}

// Subject decide o assunto baseado em flags.
//
// Templates ficaram em inglês porque storefront é global e default do sistema
// é EN. Localização por user.locale fica como follow-up — quando rodar, basta
// trocar o template carregado por `Build*Email` baseado em `d.Locale`.
func (d CheckoutEmailData) Subject() string {
	if d.AccountCreated {
		return "Viralefy — your account and order"
	}
	return "Viralefy — order received"
}

const checkoutHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>{{.Subject}}</title>
</head>
<body style="margin:0;padding:0;background:#0a0a0f;color:#f4f4f8;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#0a0a0f;">
<tr><td align="center" style="padding:24px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;background:#14141f;border:1px solid #2a2a3d;border-radius:16px;overflow:hidden;">
  <tr><td style="background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);height:4px;line-height:4px;">&nbsp;</td></tr>
  <tr><td align="center" style="padding:28px 24px 8px;">
    <a href="{{.SiteURL}}" style="text-decoration:none;"><img src="{{.LogoURL}}" alt="Viralefy" height="32" style="height:32px;width:auto;max-width:80%;display:block;" /></a>
  </td></tr>
  <tr><td style="padding:12px 32px 24px;font-size:16px;line-height:1.6;color:#f4f4f8;">
    <h1 style="margin:8px 0 12px;font-size:22px;font-weight:700;color:#f4f4f8;">Hi {{.Name}} 👋</h1>
    <p style="margin:0 0 12px;color:#cbd5e1;">We received your order for the plan <strong style="color:#f4f4f8;">{{.PlanName}}</strong>.</p>

    <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:16px 0;background:#0a0a0f;border:1px solid #2a2a3d;border-radius:8px;">
      <tr><td style="padding:16px 18px;">
        <p style="margin:0 0 6px;color:#9ca3af;font-size:12px;letter-spacing:.5px;text-transform:uppercase;">Amount</p>
        <p style="margin:0;font-size:26px;font-weight:800;background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);-webkit-background-clip:text;-webkit-text-fill-color:transparent;background-clip:text;color:#ec4899;">{{.DisplaySymbol}} {{.DisplayAmount}}</p>
        {{if ne .SettlementCurrency .DisplayCurrency}}<p style="margin:8px 0 0;color:#9ca3af;font-size:13px;">Charged in <strong style="color:#f4f4f8;">{{.SettlementAmount}} {{.SettlementCurrency}}</strong></p>{{end}}
      </td></tr>
    </table>

    {{if .AccountCreated}}
    <div style="margin:16px 0;padding:16px;background:rgba(34,197,94,.10);border:1px solid #22c55e;border-radius:8px;">
      <p style="margin:0 0 8px;font-size:14px;font-weight:700;color:#86efac;">✓ Account created</p>
      <p style="margin:0 0 8px;font-size:14px;color:#cbd5e1;">Use the credentials below to track your purchases and open support tickets:</p>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:13px;">
        <tr><td style="color:#9ca3af;padding-right:8px;">Email:</td><td style="color:#f4f4f8;">{{.Email}}</td></tr>
        <tr><td style="color:#9ca3af;padding-right:8px;">Password:</td><td style="color:#f4f4f8;"><strong>{{.Password}}</strong></td></tr>
      </table>
      <p style="margin:10px 0 0;font-size:12px;color:#9ca3af;">We recommend changing your password after first login.</p>
    </div>
    {{end}}

    <h2 style="margin:24px 0 12px;font-size:18px;color:#f4f4f8;">How to pay</h2>

    {{if .BrCode}}
      <p style="margin:0 0 12px;color:#cbd5e1;">Pay with <strong>Pix</strong> using the QR code or the copy-and-paste code:</p>
      {{if .QrImage}}<p style="text-align:center;margin:12px 0;"><img src="{{.QrImage}}" alt="Pix QR code" style="max-width:220px;width:80%;border-radius:8px;background:#fff;padding:8px;" /></p>{{end}}
      <p style="margin:14px 0 6px;font-size:12px;color:#9ca3af;letter-spacing:.5px;text-transform:uppercase;">Copy-and-paste code</p>
      <pre style="margin:0;padding:12px;background:#0a0a0f;border:1px solid #2a2a3d;border-radius:6px;font-size:11px;word-break:break-all;white-space:pre-wrap;color:#f4f4f8;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;">{{.BrCode}}</pre>
    {{else if .CryptoAddress}}
      <p style="margin:0 0 12px;color:#cbd5e1;">Send <strong>{{.SettlementAmount}} {{.SettlementCurrency}}</strong>{{if .CryptoNetwork}} (<strong>{{.CryptoNetwork}}</strong> network){{end}} to:</p>
      <pre style="margin:0;padding:12px;background:#0a0a0f;border:1px solid #2a2a3d;border-radius:6px;font-size:12px;word-break:break-all;white-space:pre-wrap;color:#f4f4f8;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;">{{.CryptoAddress}}</pre>
      {{if .PaymentURL}}<p style="margin:14px 0 0;"><a href="{{.PaymentURL}}" style="display:inline-block;padding:12px 22px;background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);color:#fff;text-decoration:none;border-radius:8px;font-weight:600;">Open payment page →</a></p>{{end}}
    {{else if .PaymentURL}}
      <p style="margin:0 0 12px;color:#cbd5e1;">Complete payment on the secure page:</p>
      <p style="margin:0;"><a href="{{.PaymentURL}}" style="display:inline-block;padding:12px 22px;background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);color:#fff;text-decoration:none;border-radius:8px;font-weight:600;">Go to payment →</a></p>
    {{else if .PixKey}}
      <p style="margin:0 0 8px;color:#cbd5e1;">Pay with <strong>Pix</strong> to the key:</p>
      <pre style="margin:0;padding:12px;background:#0a0a0f;border:1px solid #2a2a3d;border-radius:6px;font-size:13px;white-space:pre-wrap;color:#f4f4f8;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;">{{.PixKey}}</pre>
      <p style="margin:8px 0 0;font-size:12px;color:#9ca3af;">After paying, send the receipt via a support ticket.</p>
    {{else}}
      <p style="margin:0;color:#cbd5e1;">{{.FallbackInstructions}}</p>
    {{end}}

    <hr style="border:none;border-top:1px solid #2a2a3d;margin:28px 0 16px;" />
    <p style="margin:0;font-size:13px;color:#9ca3af;text-align:center;">Questions? <a href="{{.SiteURL}}/tickets" style="color:#a855f7;text-decoration:none;">Open a support ticket</a>.</p>
  </td></tr>
  <tr><td align="center" style="padding:14px 24px;background:#0a0a0f;border-top:1px solid #2a2a3d;font-size:11px;color:#6b7280;">
    © {{.Year}} Viralefy · Responsible use of social media
  </td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var checkoutTmpl = template.Must(template.New("checkout").
	Funcs(template.FuncMap{
		// nada por enquanto
	}).
	Parse(checkoutHTMLTemplate))

// addSubjectAndYear retorna template data com .Subject e .Year populados.
type checkoutEmailEnvelope struct {
	CheckoutEmailData
	Subject string
}

// BuildCheckoutEmail devolve subject, HTML e versão texto para o e-mail
// de confirmação de checkout.
func BuildCheckoutEmail(d CheckoutEmailData) (subject, html, text string, err error) {
	if d.Year == 0 {
		d.Year = time.Now().Year()
	}
	if d.LogoURL == "" {
		d.LogoURL = strings.TrimRight(d.SiteURL, "/") + "/logo.png"
	}
	if d.SiteURL == "" {
		d.SiteURL = "https://viralefy.com"
	}
	subject = d.Subject()
	env := checkoutEmailEnvelope{CheckoutEmailData: d, Subject: subject}

	var buf bytes.Buffer
	if err = checkoutTmpl.Execute(&buf, env); err != nil {
		return "", "", "", err
	}
	html = buf.String()
	text = renderCheckoutText(d)
	return
}

// TicketReplyEmailData é o que o template de notificação de resposta usa.
type TicketReplyEmailData struct {
	SiteURL  string
	LogoURL  string
	Year     int
	Name     string
	Subject  string
	Body     string
	TicketID string
}

const ticketReplyHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>{{.MailSubject}}</title>
</head>
<body style="margin:0;padding:0;background:#0a0a0f;color:#f4f4f8;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#0a0a0f;">
<tr><td align="center" style="padding:24px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;background:#14141f;border:1px solid #2a2a3d;border-radius:16px;overflow:hidden;">
  <tr><td style="background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);height:4px;line-height:4px;">&nbsp;</td></tr>
  <tr><td align="center" style="padding:28px 24px 8px;">
    <a href="{{.SiteURL}}" style="text-decoration:none;"><img src="{{.LogoURL}}" alt="Viralefy" height="32" style="height:32px;width:auto;display:block;" /></a>
  </td></tr>
  <tr><td style="padding:12px 32px 24px;font-size:16px;line-height:1.6;color:#f4f4f8;">
    <h1 style="margin:8px 0 12px;font-size:20px;font-weight:700;color:#f4f4f8;">Hi {{.Name}}</h1>
    <p style="margin:0 0 12px;color:#cbd5e1;">Support replied to your ticket <strong style="color:#f4f4f8;">"{{.Subject}}"</strong>:</p>
    <blockquote style="margin:12px 0;padding:14px 16px;background:#0a0a0f;border-left:3px solid #a855f7;border-radius:6px;font-size:14px;color:#cbd5e1;white-space:pre-wrap;">{{.Body}}</blockquote>
    <p style="margin:18px 0 0;"><a href="{{.SiteURL}}/tickets/{{.TicketID}}" style="display:inline-block;padding:11px 20px;background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);color:#fff;text-decoration:none;border-radius:8px;font-weight:600;">Open conversation →</a></p>
  </td></tr>
  <tr><td align="center" style="padding:14px 24px;background:#0a0a0f;border-top:1px solid #2a2a3d;font-size:11px;color:#6b7280;">© {{.Year}} Viralefy</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var ticketReplyTmpl = template.Must(template.New("ticket_reply").Parse(ticketReplyHTMLTemplate))

type ticketReplyEnvelope struct {
	TicketReplyEmailData
	MailSubject string
}

func BuildTicketReplyEmail(d TicketReplyEmailData) (subject, html, text string, err error) {
	if d.Year == 0 {
		d.Year = time.Now().Year()
	}
	if d.SiteURL == "" {
		d.SiteURL = "https://viralefy.com"
	}
	if d.LogoURL == "" {
		d.LogoURL = strings.TrimRight(d.SiteURL, "/") + "/logo.png"
	}
	subject = "Viralefy — support reply"
	env := ticketReplyEnvelope{TicketReplyEmailData: d, MailSubject: subject}
	var buf bytes.Buffer
	if err = ticketReplyTmpl.Execute(&buf, env); err != nil {
		return "", "", "", err
	}
	html = buf.String()
	text = fmt.Sprintf("Hi %s!\n\nSupport replied to your ticket \"%s\":\n\n%s\n\nOpen the conversation: %s/tickets/%s\n",
		d.Name, d.Subject, d.Body, strings.TrimRight(d.SiteURL, "/"), d.TicketID)
	return
}

// renderCheckoutText é a versão texto puro para clientes sem HTML.
func renderCheckoutText(d CheckoutEmailData) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Hi %s!\n\n", d.Name)
	fmt.Fprintf(&sb, "We received your order for the plan \"%s\".\n", d.PlanName)
	fmt.Fprintf(&sb, "Amount: %s %s", d.DisplaySymbol, d.DisplayAmount)
	if d.SettlementCurrency != d.DisplayCurrency {
		fmt.Fprintf(&sb, " (charged in %s %s)", d.SettlementAmount, d.SettlementCurrency)
	}
	sb.WriteString("\n\n")
	if d.AccountCreated {
		fmt.Fprintf(&sb, "Account created — use it to track your purchases:\n")
		fmt.Fprintf(&sb, "  Email:    %s\n", d.Email)
		fmt.Fprintf(&sb, "  Password: %s\n", d.Password)
		fmt.Fprintf(&sb, "We recommend changing your password after first login.\n\n")
	}
	sb.WriteString("How to pay:\n")
	switch {
	case d.BrCode != "":
		fmt.Fprintf(&sb, "Pay with Pix (copy-and-paste):\n%s\n", d.BrCode)
	case d.CryptoAddress != "":
		fmt.Fprintf(&sb, "Send %s %s to wallet: %s", d.SettlementAmount, d.SettlementCurrency, d.CryptoAddress)
		if d.CryptoNetwork != "" {
			fmt.Fprintf(&sb, " (%s network)", d.CryptoNetwork)
		}
		sb.WriteString("\n")
		if d.PaymentURL != "" {
			fmt.Fprintf(&sb, "Payment page: %s\n", d.PaymentURL)
		}
	case d.PaymentURL != "":
		fmt.Fprintf(&sb, "Complete payment at: %s\n", d.PaymentURL)
	case d.PixKey != "":
		fmt.Fprintf(&sb, "Pay with Pix to the key: %s\n", d.PixKey)
	default:
		sb.WriteString(d.FallbackInstructions + "\n")
	}
	fmt.Fprintf(&sb, "\nQuestions? Open a ticket at %s/tickets\n", strings.TrimRight(d.SiteURL, "/"))
	return sb.String()
}

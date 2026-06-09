package application

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// ReviewRequestEmailData carrega o que o template precisa pra pedir
// review pós-entrega (7d após o status virar paid).
type ReviewRequestEmailData struct {
	SiteURL  string
	LogoURL  string
	Year     int
	Name     string
	PlanName string
	OrderID  string
}

func (d ReviewRequestEmailData) Subject() string {
	return "Viralefy — how was your order?"
}

const reviewRequestHTMLTemplate = `<!doctype html>
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
    <h1 style="margin:8px 0 12px;font-size:22px;font-weight:700;color:#f4f4f8;">Hi {{.Name}} 👋</h1>
    <p style="margin:0 0 12px;color:#cbd5e1;">Your order for <strong style="color:#f4f4f8;">{{.PlanName}}</strong> should be fully delivered by now. We&apos;d love to hear how it went.</p>
    <p style="margin:0 0 16px;color:#cbd5e1;">Reviews help other buyers feel confident and they help us keep the quality bar up. It takes 30 seconds.</p>
    <p style="margin:24px 0;text-align:center;">
      <a href="{{.SiteURL}}/orders/{{.OrderID}}/review" style="display:inline-block;padding:14px 28px;background:linear-gradient(135deg,#7c3aed 0%,#ec4899 50%,#f59e0b 100%);color:#fff;text-decoration:none;border-radius:8px;font-weight:700;font-size:16px;">★ Leave a review</a>
    </p>
    <p style="margin:0;font-size:13px;color:#9ca3af;">If anything was off, reply to this email or open a support ticket — we&apos;ll make it right under our 30-day guarantee.</p>
    <hr style="border:none;border-top:1px solid #2a2a3d;margin:24px 0 16px;" />
    <p style="margin:0;font-size:13px;color:#9ca3af;text-align:center;">
      <a href="{{.SiteURL}}/tickets" style="color:#a855f7;text-decoration:none;">Open a support ticket</a>
    </p>
  </td></tr>
  <tr><td align="center" style="padding:14px 24px;background:#0a0a0f;border-top:1px solid #2a2a3d;font-size:11px;color:#6b7280;">© {{.Year}} Viralefy · Responsible use of social media</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var reviewRequestTmpl = template.Must(template.New("review_request").Parse(reviewRequestHTMLTemplate))

type reviewRequestEnvelope struct {
	ReviewRequestEmailData
	MailSubject string
}

// BuildReviewRequestEmail gera subject, HTML e versão texto pro pedido de
// review pós-entrega. Defaults: SiteURL=https://viralefy.com, LogoURL=/logo.png.
func BuildReviewRequestEmail(d ReviewRequestEmailData) (subject, html, text string, err error) {
	if d.Year == 0 {
		d.Year = time.Now().Year()
	}
	if d.SiteURL == "" {
		d.SiteURL = "https://viralefy.com"
	}
	if d.LogoURL == "" {
		d.LogoURL = strings.TrimRight(d.SiteURL, "/") + "/logo.png"
	}
	subject = d.Subject()
	env := reviewRequestEnvelope{ReviewRequestEmailData: d, MailSubject: subject}

	var buf bytes.Buffer
	if err = reviewRequestTmpl.Execute(&buf, env); err != nil {
		return "", "", "", err
	}
	html = buf.String()
	text = fmt.Sprintf("Hi %s!\n\nYour order for \"%s\" should be fully delivered by now. We'd love to hear how it went.\n\nLeave a review: %s/orders/%s/review\n\nIf anything was off, reply to this email or open a support ticket — we'll make it right under our 30-day guarantee.\n",
		d.Name, d.PlanName, strings.TrimRight(d.SiteURL, "/"), d.OrderID)
	return
}

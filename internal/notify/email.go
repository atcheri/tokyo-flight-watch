// Package notify renders and sends the alert e-mail.
package notify

import (
	"bytes"
	"fmt"
	"html/template"
	"mime"
	"net/smtp"
	"strings"
	"time"
)

type DealRow struct {
	Outbound    string
	Return      string
	StayDays    int
	Origin      string
	Destination string
	Price       int
	Stops       int
	Airlines    string
	Reasons     string
	Typical     string // Google's typical price range, if known
	URL         string
}

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       []string
}

var bodyTmpl = template.Must(template.New("mail").Parse(`<html><body style="font-family:sans-serif">
<h2>✈️ {{len .Deals}} offre(s) Tokyo</h2>
{{if not .Deals}}<p>Aucune offre ne correspond aux critères (e-mail de test).</p>{{end}}
<table cellpadding="6" style="border-collapse:collapse">
<tr style="background:#eee"><th>Aller</th><th>Retour</th><th>Jours</th><th>Trajet</th><th>Prix</th><th>Escales</th><th>Compagnies</th><th>Pourquoi</th><th>Prix habituel</th><th></th></tr>
{{range .Deals}}<tr style="border-top:1px solid #ddd">
<td>{{.Outbound}}</td><td>{{.Return}}</td><td>{{.StayDays}}</td><td>{{.Origin}} → {{.Destination}}</td>
<td><b>{{.Price}} €</b></td><td>{{if eq .Stops 0}}direct{{else}}{{.Stops}}{{end}}</td><td>{{.Airlines}}</td>
<td>{{.Reasons}}</td><td>{{.Typical}}</td><td>{{if .URL}}<a href="{{.URL}}">Google Flights</a>{{end}}</td>
</tr>{{end}}
</table>
<p style="color:#888">Prix aller-retour, 1 adulte, économique. Vérifiez toujours le tarif sur le site avant de réserver.</p>
</body></html>`))

func Render(deals []DealRow) (subject, html string, err error) {
	var buf bytes.Buffer
	if err := bodyTmpl.Execute(&buf, struct{ Deals []DealRow }{deals}); err != nil {
		return "", "", err
	}
	subject = "Vols Tokyo : aucune offre (test)"
	if len(deals) > 0 {
		cheapest := deals[0].Price
		for _, d := range deals {
			cheapest = min(cheapest, d.Price)
		}
		subject = fmt.Sprintf("Vols Tokyo : %d offre(s) dès %d €", len(deals), cheapest)
	}
	return subject, buf.String(), nil
}

func Send(cfg SMTPConfig, subject, html string) error {
	headers := []string{
		"From: " + cfg.From,
		"To: " + strings.Join(cfg.To, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
	}
	msg := strings.Join(headers, "\r\n") + "\r\n\r\n" + html
	auth := smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	if err := smtp.SendMail(addr, auth, cfg.From, cfg.To, []byte(msg)); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}

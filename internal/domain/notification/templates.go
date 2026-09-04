package notification

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"math"
	"strings"
	texttemplate "text/template"
)

// FormatMinorMoney formats int64 minor units into a currency string without using floats.
// Strictly integer division and modulo to ensure zero penny leakage and no float precision bugs.
// Examples:
//   1999, "USD" -> "$19.99"
//   50, "USD"   -> "$0.50"
//   5, "USD"    -> "$0.05"
//   0, "USD"    -> "$0.00"
//   -150, "USD" -> "-$1.50"
//   1050, "EUR" -> "10.50 EUR"
func FormatMinorMoney(amountMinor int64, currency string) string {
	if amountMinor == math.MinInt64 {
		amountMinor = math.MaxInt64
	}
	sign := ""
	if amountMinor < 0 {
		sign = "-"
		amountMinor = -amountMinor
	}
	units := amountMinor / 100
	cents := amountMinor % 100

	curr := strings.TrimSpace(strings.ToUpper(currency))
	if curr == "USD" || curr == "" {
		return fmt.Sprintf("%s$%d.%02d", sign, units, cents)
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, units, cents, curr)
}

const OrderConfirmedTextTemplate = `Thank you for your order!
Order ID: {{.OrderID}}
Confirmed At: {{.ConfirmedAt.Format "2006-01-02 15:04:05 MST"}}

Items Ordered:
{{range .LineItems}}- {{.Title}} (SKU: {{.SKU}}) x{{.Quantity}} @ {{formatMoney .UnitPriceMinor $.Currency}} = {{formatMoney .SubtotalMinor $.Currency}}
{{end}}
Total Amount: {{formatMoney .TotalAmountMinor .Currency}}

We are preparing your package for shipment.
`

const OrderConfirmedHTMLTemplate = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Order Confirmed</title></head>
<body style="font-family: Arial, sans-serif; color: #333; line-height: 1.6;">
  <h2 style="color: #2e7d32;">Order Confirmed!</h2>
  <p>Thank you for shopping with ShopFlow.</p>
  <p><strong>Order ID:</strong> {{.OrderID}}</p>
  <p><strong>Date:</strong> {{.ConfirmedAt.Format "2006-01-02 15:04:05 MST"}}</p>
  <table style="width: 100%; border-collapse: collapse; margin-top: 20px;">
    <thead>
      <tr style="background: #f5f5f5; text-align: left;">
        <th style="padding: 8px; border: 1px solid #ddd;">Item</th>
        <th style="padding: 8px; border: 1px solid #ddd;">SKU</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Qty</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Price</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Subtotal</th>
      </tr>
    </thead>
    <tbody>
      {{range .LineItems}}
      <tr>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.Title}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.SKU}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.Quantity}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{formatMoney .UnitPriceMinor $.Currency}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{formatMoney .SubtotalMinor $.Currency}}</td>
      </tr>
      {{end}}
    </tbody>
    <tfoot>
      <tr>
        <td colspan="4" style="padding: 8px; text-align: right; font-weight: bold; border: 1px solid #ddd;">Total:</td>
        <td style="padding: 8px; font-weight: bold; border: 1px solid #ddd;">{{formatMoney .TotalAmountMinor .Currency}}</td>
      </tr>
    </tfoot>
  </table>
</body>
</html>`

const OrderCancelledTextTemplate = `Your order has been cancelled.
Order ID: {{.OrderID}}
Reason: {{.Reason}}
Total Amount: {{formatMoney .TotalAmountMinor .Currency}}
{{if .LineItems}}
Cancelled Items:
{{range .LineItems}}- {{.Title}} (SKU: {{.SKU}}) x{{.Quantity}} @ {{formatMoney .UnitPriceMinor $.Currency}} = {{formatMoney .SubtotalMinor $.Currency}}
{{end}}{{end}}
Any authorizations or charges will be released. If you have questions, please contact support.
`

const OrderCancelledHTMLTemplate = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><title>Order Cancelled</title></head>
<body style="font-family: Arial, sans-serif; color: #333; line-height: 1.6;">
  <h2 style="color: #c62828;">Order Cancelled</h2>
  <p>Your order has been cancelled.</p>
  <p><strong>Order ID:</strong> {{.OrderID}}</p>
  <p><strong>Reason:</strong> {{.Reason}}</p>
  <p><strong>Total:</strong> {{formatMoney .TotalAmountMinor .Currency}}</p>
  {{if .LineItems}}
  <table style="width: 100%; border-collapse: collapse; margin-top: 20px;">
    <thead>
      <tr style="background: #f5f5f5; text-align: left;">
        <th style="padding: 8px; border: 1px solid #ddd;">Item</th>
        <th style="padding: 8px; border: 1px solid #ddd;">SKU</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Qty</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Price</th>
        <th style="padding: 8px; border: 1px solid #ddd;">Subtotal</th>
      </tr>
    </thead>
    <tbody>
      {{range .LineItems}}
      <tr>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.Title}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.SKU}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{.Quantity}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{formatMoney .UnitPriceMinor $.Currency}}</td>
        <td style="padding: 8px; border: 1px solid #ddd;">{{formatMoney .SubtotalMinor $.Currency}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>
  {{end}}
  <p>Any pre-authorizations or payments have been released or refunded.</p>
</body>
</html>`

// Renderer renders dual-part HTML and plain-text emails.
type Renderer struct {
	confirmedHTML *htmltemplate.Template
	confirmedText *texttemplate.Template
	cancelledHTML *htmltemplate.Template
	cancelledText *texttemplate.Template
}

// NewRenderer parses and prepares all notification email templates.
func NewRenderer() (*Renderer, error) {
	funcMapHTML := htmltemplate.FuncMap{
		"formatMoney": FormatMinorMoney,
	}
	funcMapText := texttemplate.FuncMap{
		"formatMoney": FormatMinorMoney,
	}

	cHTML, err := htmltemplate.New("confirmed_html").Funcs(funcMapHTML).Parse(OrderConfirmedHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse confirmed html: %w", err)
	}
	cText, err := texttemplate.New("confirmed_text").Funcs(funcMapText).Parse(OrderConfirmedTextTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse confirmed text: %w", err)
	}
	xHTML, err := htmltemplate.New("cancelled_html").Funcs(funcMapHTML).Parse(OrderCancelledHTMLTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse cancelled html: %w", err)
	}
	xText, err := texttemplate.New("cancelled_text").Funcs(funcMapText).Parse(OrderCancelledTextTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse cancelled text: %w", err)
	}

	return &Renderer{
		confirmedHTML: cHTML,
		confirmedText: cText,
		cancelledHTML: xHTML,
		cancelledText: xText,
	}, nil
}

// RenderOrderConfirmed generates text and HTML bodies for order confirmation.
func (r *Renderer) RenderOrderConfirmed(payload OrderConfirmedPayload) (string, string, error) {
	var textBuf, htmlBuf bytes.Buffer
	if err := r.confirmedText.Execute(&textBuf, payload); err != nil {
		return "", "", fmt.Errorf("render confirmed text: %w", err)
	}
	if err := r.confirmedHTML.Execute(&htmlBuf, payload); err != nil {
		return "", "", fmt.Errorf("render confirmed html: %w", err)
	}
	return textBuf.String(), htmlBuf.String(), nil
}

// RenderOrderCancelled generates text and HTML bodies for order cancellation.
func (r *Renderer) RenderOrderCancelled(payload OrderCancelledPayload) (string, string, error) {
	var textBuf, htmlBuf bytes.Buffer
	if err := r.cancelledText.Execute(&textBuf, payload); err != nil {
		return "", "", fmt.Errorf("render cancelled text: %w", err)
	}
	if err := r.cancelledHTML.Execute(&htmlBuf, payload); err != nil {
		return "", "", fmt.Errorf("render cancelled html: %w", err)
	}
	return textBuf.String(), htmlBuf.String(), nil
}

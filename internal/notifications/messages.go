package notifications

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/i18n"
	"github.com/Nikemas/cozy_backend/internal/orders"
)

// statusText is one push notification's title/body template; %s in Title
// is the order number.
type statusText struct {
	Title string
	Body  string
}

// statusTexts[lang][key] — key is the order status, with "_pickup"
// suffixed for the self-pickup variants where "courier"/"delivered"
// wording would be wrong. Kept in Go rather than locales/*.yaml because
// those files feed the website templates and are loaded from disk by
// web.RegisterRoutes; push copy is a handful of fixed strings.
var statusTexts = map[string]map[string]statusText{
	i18n.LangRU: {
		string(orders.StatusConfirmed):              {"Заказ %s подтверждён", "Мы начали собирать ваш заказ."},
		string(orders.StatusCourierAssigned):        {"Заказ %s передан курьеру", "Курьер скоро свяжется с вами."},
		string(orders.StatusCourierAssigned) + "_p": {"Заказ %s готов к выдаче", "Заберите его в выбранном магазине Cozy."},
		string(orders.StatusDelivered):              {"Заказ %s доставлен", "Спасибо за покупку в Cozy!"},
		string(orders.StatusDelivered) + "_p":       {"Заказ %s получен", "Спасибо за покупку в Cozy!"},
		string(orders.StatusCancelled):              {"Заказ %s отменён", "Если у вас есть вопросы — свяжитесь с нами."},
	},
	i18n.LangKY: {
		string(orders.StatusConfirmed):              {"%s буйрутмаңыз ырасталды", "Буйрутмаңызды чогултуп баштадык."},
		string(orders.StatusCourierAssigned):        {"%s буйрутмаңыз курьерге берилди", "Курьер жакында сиз менен байланышат."},
		string(orders.StatusCourierAssigned) + "_p": {"%s буйрутмаңыз даяр", "Аны тандалган Cozy дүкөнүнөн алып кетиңиз."},
		string(orders.StatusDelivered):              {"%s буйрутмаңыз жеткирилди", "Cozy'ден сатып алганыңыз үчүн рахмат!"},
		string(orders.StatusDelivered) + "_p":       {"%s буйрутмаңыз алынды", "Cozy'ден сатып алганыңыз үчүн рахмат!"},
		string(orders.StatusCancelled):              {"%s буйрутмаңыз жокко чыгарылды", "Суроолоруңуз болсо, биз менен байланышыңыз."},
	},
}

// statusPushText returns the localized title/body for order entering its
// current status, or ok=false when that status has no customer-facing
// notification (e.g. "placed" — the customer just placed it themselves).
func statusPushText(lang string, order orders.Order) (title, body string, ok bool) {
	texts, found := statusTexts[lang]
	if !found {
		texts = statusTexts[i18n.DefaultLang]
	}
	key := string(order.Status)
	if isPickup(order) {
		if _, has := texts[key+"_p"]; has {
			key += "_p"
		}
	}
	t, found := texts[key]
	if !found {
		return "", "", false
	}
	return fmt.Sprintf(t.Title, order.OrderNumber), t.Body, true
}

func isPickup(o orders.Order) bool { return o.AddressID == nil || *o.AddressID == "" }

// maxTelegramItems caps the item lines in a staff message — Telegram
// rejects messages over 4096 characters.
const maxTelegramItems = 25

// staffOrderMessage renders the Telegram-HTML staff notification for a new
// order. Every interpolated value is HTML-escaped (product names and
// addresses are user/admin-entered text).
func staffOrderMessage(o orders.Order, info OrderInfo, adminBaseURL string) string {
	e := html.EscapeString
	var b strings.Builder

	fmt.Fprintf(&b, "<b>Новый заказ %s</b>\n", e(o.OrderNumber))
	fmt.Fprintf(&b, "Сумма: <b>%s</b>\n", formatSom(o.TotalAmount))
	fmt.Fprintf(&b, "Оплата: %s\n", paymentLabel(o.PaymentMethod))

	if isPickup(o) {
		where := info.PointName
		if info.PointAddress != "" {
			where += ", " + info.PointAddress
		}
		if where == "" {
			where = "точка не найдена"
		}
		fmt.Fprintf(&b, "Получение: самовывоз — %s\n", e(where))
	} else {
		addr := info.AddressText
		if addr == "" {
			addr = "адрес не найден"
		}
		fmt.Fprintf(&b, "Получение: доставка — %s\n", e(addr))
		if info.PointName != "" {
			fmt.Fprintf(&b, "Склад: %s\n", e(info.PointName))
		}
	}

	if info.CustomerPhone != "" || info.CustomerName != "" {
		customer := strings.TrimSpace(info.CustomerName + " " + info.CustomerPhone)
		fmt.Fprintf(&b, "Покупатель: %s\n", e(customer))
	}
	if o.Comment != nil && *o.Comment != "" {
		fmt.Fprintf(&b, "Комментарий: %s\n", e(*o.Comment))
	}

	b.WriteString("\nТовары:\n")
	for i, it := range o.Items {
		if i == maxTelegramItems {
			fmt.Fprintf(&b, "… и ещё %d поз.\n", len(o.Items)-maxTelegramItems)
			break
		}
		details := strings.Join(nonEmpty(it.SizeSnapshot, it.ColorSnapshot), ", ")
		if details != "" {
			details = " (" + details + ")"
		}
		fmt.Fprintf(&b, "• %s%s × %d — %s\n", e(it.ProductNameSnapshot), e(details), it.Quantity, formatSom(it.Price*float64(it.Quantity)))
	}

	if base := strings.TrimRight(adminBaseURL, "/"); base != "" {
		fmt.Fprintf(&b, "\n<a href=\"%s/admin/orders/%s\">Открыть в админке</a>", e(base), e(o.ID))
	}
	return b.String()
}

func paymentLabel(m orders.PaymentMethod) string {
	switch m {
	case orders.PaymentCashOnDelivery:
		return "наличными при получении"
	case orders.PaymentOnlineCard:
		return "онлайн картой"
	default:
		return string(m)
	}
}

func nonEmpty(vals ...string) []string {
	out := vals[:0:0]
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// formatSom renders an amount as "12 500 сом" (or "12 500,50 сом" when
// there are tyiyns), space-grouped thousands like the storefront.
func formatSom(amount float64) string {
	neg := amount < 0
	amount = math.Abs(amount)
	cents := int64(math.Round(amount * 100))
	whole, frac := cents/100, cents%100

	digits := strconv.FormatInt(whole, 10)
	var grouped strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			grouped.WriteByte(' ')
		}
		grouped.WriteRune(d)
	}
	s := grouped.String()
	if frac != 0 {
		s += fmt.Sprintf(",%02d", frac)
	}
	if neg {
		s = "-" + s
	}
	return s + " сом"
}

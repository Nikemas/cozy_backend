// audit_summary.go renders a journal line's summary in the viewer's
// language from the message stored in details (msg_key / msg_args /
// msg_via, see internal/audit/messages.go). Old rows, and rows whose
// message can't be filled in, keep their stored Russian Summary.
//
// The result is plain text: the template escapes it like any other
// string, so names typed by staff (e.g. a product called "<script>")
// never become markup.
package admin

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/audit"
)

// summaryPlaceholder matches a named placeholder such as {name}.
var summaryPlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)

// auditSummaryFieldsArg is the argument holding product field codes.
const auditSummaryFieldsArg = "fields"

// localizedAuditSummary returns row's summary in t's language, or
// row.Summary when the row has no message, the language has no
// translation for it, or an argument is missing.
func localizedAuditSummary(t tr, row audit.Row) string {
	key, _ := row.Details[audit.DetailMsgKey].(string)
	if key == "" || !adminBundle.Has(t.Lang(), key) {
		return row.Summary
	}
	args, _ := row.Details[audit.DetailMsgArgs].(map[string]any)
	text, ok := fillAuditSummary(t, t.T(key), args)
	if !ok {
		return row.Summary
	}
	if via, _ := row.Details[audit.DetailMsgVia].(string); via != "" {
		viaKey := audit.ViaKeyPrefix + via
		if !adminBundle.Has(t.Lang(), viaKey) {
			return row.Summary
		}
		text += " " + t.T(viaKey)
	}
	return text
}

// fillAuditSummary substitutes args into tmpl's placeholders; ok is false
// when a placeholder has no argument. Substituted values are not scanned
// again, so a name containing "{x}" stays as typed.
func fillAuditSummary(t tr, tmpl string, args map[string]any) (string, bool) {
	ok := true
	out := summaryPlaceholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[1 : len(m)-1]
		v, has := args[name]
		if !has {
			ok = false
			return m
		}
		if name == auditSummaryFieldsArg {
			return auditFieldList(t, v)
		}
		return auditSummaryArg(v)
	})
	return out, ok
}

// auditFieldList translates a list of product field codes.
func auditFieldList(t tr, v any) string {
	items, _ := v.([]any)
	labels := make([]string, 0, len(items))
	for _, it := range items {
		code := auditSummaryArg(it)
		if l, ok := productFieldLabels[code]; ok {
			code = t.T(l)
		}
		labels = append(labels, code)
	}
	return strings.Join(labels, ", ")
}

// auditSummaryArg formats one argument as decoded from JSONB.
func auditSummaryArg(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return auditValue(ruTr, x)
	}
}

package catalog

import (
	"errors"
	"strings"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/i18n"
)

// importMsg is one admin-facing problem in the import report, kept as a
// translation key until the report is built so it can be rendered in the
// admin's language (ImportOptions.Lang). Texts live in
// locales/errors.<lang>.yaml under "import.*".
type importMsg struct {
	key    string            // "import.<name>"
	params map[string]string // {name} placeholder values
	// reason, when set, is rendered into the {reason} placeholder — a
	// nested message, e.g. "цена варианта: {reason}".
	reason *importMsg
	// appErr, when set, is the message instead of key: an AppError raised
	// while importing (e.g. by ImportStore.ResolveCategory).
	appErr *apperr.AppError
}

func newImportMsg(key string, params map[string]string) importMsg {
	return importMsg{key: key, params: params}
}

// withReason returns m with inner rendered into its {reason} placeholder.
func (m importMsg) withReason(inner importMsg) importMsg {
	m.reason = &inner
	return m
}

// render returns m's text in lang.
func (m importMsg) render(lang string) string {
	if m.appErr != nil {
		return apperr.Localize(lang, m.appErr)
	}
	params := m.params
	if m.reason != nil {
		params = make(map[string]string, len(m.params)+1)
		for k, v := range m.params {
			params[k] = v
		}
		params["reason"] = m.reason.render(lang)
	}
	return apperr.Translate(lang, m.key, params)
}

// Error makes importMsg usable as the error of the cell parsers
// (parsePrice, parseQuantity); it is the Russian text.
func (m importMsg) Error() string { return m.render(i18n.LangRU) }

// asImportMsg turns a cell-parser error back into its importMsg.
func asImportMsg(err error) importMsg {
	var m importMsg
	if errors.As(err, &m) {
		return m
	}
	return importMsg{appErr: apperr.Internal(err)}
}

// renderImportMsgs joins msgs ("; ") in lang.
func renderImportMsgs(lang string, msgs []importMsg) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = m.render(lang)
	}
	return strings.Join(parts, "; ")
}

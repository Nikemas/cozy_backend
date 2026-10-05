package audit

// Translatable summaries. Entry.Summary stays a ready Russian line (old
// rows, search, backwards compatibility); alongside it an entry can carry
// a message — a locale key of locales/admin.*.yaml plus named arguments —
// stored in details as msg_key / msg_args / msg_via. The journal page
// renders the message in the viewer's language and falls back to Summary
// when there is no message or no translation.
//
// Message templates use named placeholders, e.g. "Товар «{name}»
// активирован". The "fields" argument is a list of product field codes
// (see admin's productFieldLabels), translated at render time.

// Details keys holding the message.
const (
	DetailMsgKey  = "msg_key"
	DetailMsgArgs = "msg_args"
	DetailMsgVia  = "msg_via"
)

// Message sources appended to the summary ("(API)", "(массово)").
const (
	ViaAPI  = "api"
	ViaBulk = "bulk"
)

// ViaKeyPrefix + via is the locale key of a source suffix.
const ViaKeyPrefix = "admin.audit.summary.via."

// Summary message keys.
const (
	MsgProductCreated        = "admin.audit.summary.product_created"
	MsgProductUpdated        = "admin.audit.summary.product_updated"
	MsgProductUpdatedFields  = "admin.audit.summary.product_updated_fields"
	MsgProductActivated      = "admin.audit.summary.product_activated"
	MsgProductDeactivated    = "admin.audit.summary.product_deactivated"
	MsgProductDeleted        = "admin.audit.summary.product_deleted"
	MsgProductCategory       = "admin.audit.summary.product_category"
	MsgProductImages         = "admin.audit.summary.product_images"
	MsgVariantChanged        = "admin.audit.summary.variant_changed"
	MsgVariantAdded          = "admin.audit.summary.variant_added"
	MsgVariantRemoved        = "admin.audit.summary.variant_removed"
	MsgVariantCreated        = "admin.audit.summary.variant_created"
	MsgVariantUpdated        = "admin.audit.summary.variant_updated"
	MsgVariantDeleted        = "admin.audit.summary.variant_deleted"
	MsgStockChanged          = "admin.audit.summary.stock_changed"
	MsgCategoryCreated       = "admin.audit.summary.category_created"
	MsgCategoryUpdated       = "admin.audit.summary.category_updated"
	MsgCategoryDeleted       = "admin.audit.summary.category_deleted"
	MsgCategoryDeletedNoName = "admin.audit.summary.category_deleted_unnamed"
	MsgPointCreated          = "admin.audit.summary.point_created"
	MsgPointUpdated          = "admin.audit.summary.point_updated"
	MsgPointEnabled          = "admin.audit.summary.point_enabled"
	MsgPointDisabled         = "admin.audit.summary.point_disabled"
	MsgStaffCreated          = "admin.audit.summary.staff_created"
	MsgStaffEnabled          = "admin.audit.summary.staff_enabled"
	MsgStaffDisabled         = "admin.audit.summary.staff_disabled"
	MsgStaffPasswordReset    = "admin.audit.summary.staff_password_reset"
)

// SummaryKeys lists every message key and source-suffix key, for tests
// that check both locales translate them.
var SummaryKeys = []string{
	MsgProductCreated, MsgProductUpdated, MsgProductUpdatedFields, MsgProductActivated,
	MsgProductDeactivated, MsgProductDeleted, MsgProductCategory, MsgProductImages,
	MsgVariantChanged, MsgVariantAdded, MsgVariantRemoved, MsgVariantCreated,
	MsgVariantUpdated, MsgVariantDeleted, MsgStockChanged,
	MsgCategoryCreated, MsgCategoryUpdated, MsgCategoryDeleted, MsgCategoryDeletedNoName,
	MsgPointCreated, MsgPointUpdated, MsgPointEnabled, MsgPointDisabled,
	MsgStaffCreated, MsgStaffEnabled, MsgStaffDisabled, MsgStaffPasswordReset,
	ViaKeyPrefix + ViaAPI, ViaKeyPrefix + ViaBulk,
}

// Args is a message's named arguments.
type Args map[string]any

// StoredDetails returns what is written to the details column: e.Details
// plus the message keys, as a new map (e.Details is never modified).
func (e Entry) StoredDetails() map[string]any {
	if e.MsgKey == "" {
		return e.Details
	}
	out := make(map[string]any, len(e.Details)+3)
	for k, v := range e.Details {
		out[k] = v
	}
	out[DetailMsgKey] = e.MsgKey
	if len(e.MsgArgs) > 0 {
		out[DetailMsgArgs] = map[string]any(e.MsgArgs)
	}
	if e.MsgVia != "" {
		out[DetailMsgVia] = e.MsgVia
	}
	return out
}

// StockArgs are the arguments of MsgStockChanged.
func StockArgs(product, size, color, point string, from, to int) Args {
	return Args{"product": product, "size": size, "color": color, "point": point, "from": from, "to": to}
}

// CategoryMessage is the message of a category create/update/delete; a
// delete whose name is unknown uses the unnamed variant.
func CategoryMessage(action, name string) (string, Args) {
	switch action {
	case ActionCategoryCreate:
		return MsgCategoryCreated, Args{"name": name}
	case ActionCategoryUpdate:
		return MsgCategoryUpdated, Args{"name": name}
	case ActionCategoryDelete:
		if name == "" {
			return MsgCategoryDeletedNoName, nil
		}
		return MsgCategoryDeleted, Args{"name": name}
	}
	return "", nil
}

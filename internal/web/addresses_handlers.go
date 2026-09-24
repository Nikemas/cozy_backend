package web

import (
	"context"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

// AddressCard is one row in the addresses list.
type AddressCard struct {
	ID          string
	Label       string
	AddressText string
	IsDefault   bool
}

// AddressFormData backs the inline create/edit form. ID is "" for a new
// address.
type AddressFormData struct {
	ID          string
	Label       string
	AddressText string
	IsDefault   bool
	Error       string
}

// AddressesData backs both addresses.gohtml's full page and the
// _address_list partial every mutation re-renders. Form is non-nil only
// while the create/edit form should be shown inline.
type AddressesData struct {
	Items []AddressCard
	Form  *AddressFormData
}

// addressesScreen renders the full addresses page — only reachable by a
// logged-in customer (delivery addresses are meaningless without one);
// an unauthenticated visitor is sent to /profile to log in first, same
// as cart/checkout per the plan's "no guest state" decision for
// account-scoped screens.
func (h *handlers) addressesScreen(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "addresses")
	if !data.Authed {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	items, err := h.loadAddressCards(r.Context(), data.CustomerID)
	if err != nil {
		return err
	}
	data.Data = AddressesData{Items: items}
	return h.render.Render(w, "addresses", data)
}

func (h *handlers) loadAddressCards(ctx context.Context, customerID string) ([]AddressCard, error) {
	list, err := h.addressRepo.List(ctx, customerID)
	if err != nil {
		return nil, err
	}
	cards := make([]AddressCard, 0, len(list))
	for _, a := range list {
		label := ""
		if a.Label != nil {
			label = *a.Label
		}
		cards = append(cards, AddressCard{ID: a.ID, Label: label, AddressText: a.AddressText, IsDefault: a.IsDefault})
	}
	return cards, nil
}

// renderAddressPanel re-renders the whole #address-panel fragment (list +
// optional inline form) — every HTMX address action swaps this same
// fragment back in, so the list is always consistent with the latest
// write without a full page reload.
func (h *handlers) renderAddressPanel(w http.ResponseWriter, r *http.Request, customerID string, form *AddressFormData) error {
	items, err := h.loadAddressCards(r.Context(), customerID)
	if err != nil {
		return err
	}
	page := PageData{Lang: h.resolveLang(r), Screen: "addresses", Authed: true, CustomerID: customerID}
	page.Data = AddressesData{Items: items, Form: form}
	return h.render.RenderPartial(w, "addresses", "_address_list", page)
}

func requireCustomerID(r *http.Request) (string, error) {
	customerID := CustomerID(r)
	if customerID == "" {
		return "", apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	return customerID, nil
}

// addressNewForm shows an empty create form inline (HTMX: hx-get from
// the "add address" button).
func (h *handlers) addressNewForm(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	return h.renderAddressPanel(w, r, customerID, &AddressFormData{})
}

// addressEditForm shows an existing address's create form pre-filled
// (HTMX: hx-get from a card's "изменить" button).
func (h *handlers) addressEditForm(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	a, err := h.addressRepo.GetByID(r.Context(), customerID, r.PathValue("id"))
	if err != nil {
		return err
	}
	label := ""
	if a.Label != nil {
		label = *a.Label
	}
	return h.renderAddressPanel(w, r, customerID, &AddressFormData{
		ID: a.ID, Label: label, AddressText: a.AddressText, IsDefault: a.IsDefault,
	})
}

// addressCancelForm hides the inline form without saving (HTMX: hx-get
// from the form's "отмена" button).
func (h *handlers) addressCancelForm(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	return h.renderAddressPanel(w, r, customerID, nil)
}

// addressCreate handles the create form's submit.
func (h *handlers) addressCreate(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}

	label := r.FormValue("label")
	addressText := r.FormValue("address_text")
	isDefault := r.FormValue("is_default") == "on"

	in := storefront.AddressInput{Label: &label, AddressText: addressText, IsDefault: isDefault}
	if _, err := h.addressRepo.Create(r.Context(), customerID, in); err != nil {
		return h.renderAddressPanel(w, r, customerID, &AddressFormData{
			Label: label, AddressText: addressText, IsDefault: isDefault, Error: h.errText(h.resolveLang(r), err),
		})
	}

	if err := h.renderAddressPanel(w, r, customerID, nil); err != nil {
		return err
	}
	_, _ = w.Write([]byte(toastOOB(h.t(h.resolveLang(r), "toast.address_saved"))))
	return nil
}

// addressUpdate handles the edit form's submit.
func (h *handlers) addressUpdate(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		return apperr.BadRequest("bad_request", "некорректная форма")
	}

	label := r.FormValue("label")
	addressText := r.FormValue("address_text")
	isDefault := r.FormValue("is_default") == "on"

	in := storefront.AddressInput{Label: &label, AddressText: addressText, IsDefault: isDefault}
	if _, err := h.addressRepo.Update(r.Context(), customerID, id, in); err != nil {
		return h.renderAddressPanel(w, r, customerID, &AddressFormData{
			ID: id, Label: label, AddressText: addressText, IsDefault: isDefault, Error: h.errText(h.resolveLang(r), err),
		})
	}

	if err := h.renderAddressPanel(w, r, customerID, nil); err != nil {
		return err
	}
	_, _ = w.Write([]byte(toastOOB(h.t(h.resolveLang(r), "toast.address_saved"))))
	return nil
}

// addressDelete removes an address (HTMX: hx-delete from a card's
// "удалить" button).
func (h *handlers) addressDelete(w http.ResponseWriter, r *http.Request) error {
	customerID, err := requireCustomerID(r)
	if err != nil {
		return err
	}
	if err := h.addressRepo.Delete(r.Context(), customerID, r.PathValue("id")); err != nil {
		return err
	}
	if err := h.renderAddressPanel(w, r, customerID, nil); err != nil {
		return err
	}
	_, _ = w.Write([]byte(toastOOB(h.t(h.resolveLang(r), "toast.address_deleted"))))
	return nil
}

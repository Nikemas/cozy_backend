package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/media"
)

// FavoriteCard is one tile in fav.gohtml's grid.
type FavoriteCard struct {
	ProductID string
	Brand     string
	Name      string
	Price     string
	PhotoURL  string // thumbnail; "" → shoe icon
	DetailURL string
}

// FavData backs fav.gohtml's content.
type FavData struct {
	Items []FavoriteCard
}

// favorites renders the fav screen. Cart/orders may not exist yet if
// Task 3 hasn't merged (see favAddToCart below), but favorites themselves
// are this task's own domain, so a real read failure here is a genuine
// 500 rather than something to paper over.
func (h *handlers) favorites(w http.ResponseWriter, r *http.Request) error {
	data := h.base(r, "fav")
	if !data.Authed {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return nil
	}

	items, err := h.loadFavoriteCards(r.Context(), data.CustomerID, data.Lang)
	if err != nil {
		return err
	}
	data.Data = FavData{Items: items}
	return h.render.Render(w, "fav", data)
}

// loadFavoriteCards resolves a customer's favorited product IDs
// (internal/storefront, this task's own domain) into display-ready cards
// via one internal/catalog.ProductRepo.GetActiveByIDs query (Task 2's
// domain — read-only here, per the plan's package ownership; formerly one
// GetByID per favorite). A product that's since gone inactive/deleted is
// skipped rather than 500ing the whole page.
func (h *handlers) loadFavoriteCards(ctx context.Context, customerID, lang string) ([]FavoriteCard, error) {
	ids, err := h.favoriteRepo.ListProductIDs(ctx, customerID)
	if err != nil {
		return nil, err
	}
	byID, err := h.products.GetActiveByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	images, err := h.images.PrimaryForProducts(ctx, ids)
	if err != nil {
		return nil, err
	}

	cards := make([]FavoriteCard, 0, len(ids))
	for _, id := range ids {
		p, ok := byID[id]
		if !ok {
			continue
		}
		brand := ""
		if p.Brand != nil {
			brand = *p.Brand
		}
		name := p.NameRu
		if lang == "ky" {
			name = p.NameKy
		}
		card := FavoriteCard{
			ProductID: p.ID,
			Brand:     brand,
			Name:      name,
			Price:     formatAmount(p.BasePrice, h.t(lang, "common.currency")),
			DetailURL: ProductPath(p.ID, p.NameRu),
		}
		if img, ok := images[p.ID]; ok {
			card.PhotoURL = h.photoURL(media.ThumbKey(img.ObjectKey))
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func (h *handlers) renderFavGrid(w http.ResponseWriter, r *http.Request, customerID string) error {
	lang := h.resolveLang(r)
	items, err := h.loadFavoriteCards(r.Context(), customerID, lang)
	if err != nil {
		return err
	}
	page := PageData{Lang: lang, Screen: "fav", Authed: true, CustomerID: customerID}
	page.Data = FavData{Items: items}
	return h.render.RenderPartial(w, "fav", "_fav_grid", page)
}

// favAdd favorites a product (HTMX: hx-post from the shop grid's and the
// product page's heart button — see shop.gohtml/product.gohtml). Both
// Task 2 and Task 4 had merged before either wired this route: Task 2's
// heart buttons shipped decorative (this task's domain didn't exist yet
// in its worktree), and this task only wired removal from the fav
// screen. Fixing it here rather than leaving two dead heart icons on the
// site.
func (h *handlers) favAdd(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	productID := r.PathValue("id")

	if err := h.favoriteRepo.Add(r.Context(), customerID, productID); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(toastOOB(h.t(h.resolveLang(r), "toast.added_to_favorites"))))
	return nil
}

// favRemove un-favorites a product (HTMX: hx-delete from fav.gohtml's
// "убрать из избранного" button) and re-renders the grid in place.
func (h *handlers) favRemove(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	productID := r.PathValue("id")

	if err := h.favoriteRepo.Remove(r.Context(), customerID, productID); err != nil {
		return err
	}
	if err := h.renderFavGrid(w, r, customerID); err != nil {
		return err
	}
	_, _ = w.Write([]byte(toastOOB(h.t(h.resolveLang(r), "toast.removed_from_favorites"))))
	return nil
}

// favAddToCart adds a favorited product's first variant to the cart
// (HTMX: hx-post from fav.gohtml's "в корзину" button). internal/orders
// is Task 3's domain — this only calls the frozen CartRepo.Add contract.
// If it's still the 501 stub (Task 3 not merged into this worktree), that
// surfaces as a "coming soon" toast rather than a broken page, per the
// plan's coordination note for this exact situation.
func (h *handlers) favAddToCart(w http.ResponseWriter, r *http.Request) error {
	customerID := CustomerID(r)
	if customerID == "" {
		return apperr.Unauthorized("unauthorized", "войдите в аккаунт")
	}
	productID := r.PathValue("id")
	lang := h.resolveLang(r)

	variants, err := h.variants.ListByProduct(r.Context(), productID)
	if err != nil {
		return err
	}
	if len(variants) == 0 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(toastOOB(h.t(lang, "toast.no_variants"))))
		return nil
	}

	if err := h.cartRepo.Add(r.Context(), customerID, variants[0].ID, 1); err != nil {
		msg := h.errText(lang, err)
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.Status == http.StatusNotImplemented {
			msg = h.t(lang, "toast.cart_soon")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(toastOOB(msg)))
		return nil
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(toastOOB(h.t(lang, "toast.added_to_cart"))))
	return nil
}

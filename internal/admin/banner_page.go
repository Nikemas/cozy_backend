// banner_page.go is the "Баннер на главной" screen (feat/home-banner): one
// form editing the promo banner at the top of the site's home page and
// the app's home screen (internal/banner) — on/off, RU/KY texts, the
// category the button leads to, colors, and two pictures uploaded through
// internal/media's banner pipeline. Owner + manager (routes.go), the same
// roles as Категории and Рассылки.
package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/audit"
	"github.com/Nikemas/cozy_backend/internal/banner"
	"github.com/Nikemas/cozy_backend/internal/catalog"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/reports"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

const (
	// bannerFormMaxBytes caps the whole multipart body: two pictures at
	// the media limit each plus the text fields.
	bannerFormMaxBytes = 2*media.MaxUploadFileBytes + 1<<20
	// bannerFormMemory is how much of the body ParseMultipartForm keeps in
	// memory before spilling file parts to temp files.
	bannerFormMemory = 8 << 20
)

// bannerStore is the subset of *banner.Store the page uses.
type bannerStore interface {
	Get(ctx context.Context) (*banner.Banner, error)
	Update(ctx context.Context, in banner.Input) (*banner.Banner, error)
}

// bannerImageStore uploads banner pictures; *media.Client in production.
type bannerImageStore interface {
	StoreBannerImage(ctx context.Context, data []byte, kind media.BannerImageKind) (string, error)
	RemoveObject(ctx context.Context, objectKey string) error
}

type bannerPages struct {
	h            *handlers
	store        bannerStore
	images       bannerImageStore
	categoryTree func(ctx context.Context) ([]*catalog.Category, error)
	objectURL    func(objectKey string) string
}

// registerBannerRoutes mounts the banner screen behind gate.
func registerBannerRoutes(mux *http.ServeMux, p *bannerPages, gate func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /admin/banner", gate(p.page))
	mux.HandleFunc("POST /admin/banner", gate(p.save))
}

// bannerForm is the form's state: the stored banner on GET, the submitted
// values after a failed POST (sticky). ImageURL/BgImageURL are the
// currently stored pictures.
type bannerForm struct {
	Enabled              bool
	EyebrowRU, EyebrowKY string
	TitleRU, TitleKY     string
	ButtonRU, ButtonKY   string
	CategoryID           string
	BgColor, TextColor   string
	EyebrowColor         string
	ImageURL, BgImageURL string
	UpdatedAt            string
}

type bannerPageData struct {
	Form       bannerForm
	Categories []categoryParentOption
	Error      string
	MaxEyebrow int
	MaxTitle   int
	MaxButton  int
}

func newBannerForm(b *banner.Banner, objectURL func(string) string) bannerForm {
	f := bannerForm{
		Enabled:   b.Enabled,
		EyebrowRU: b.EyebrowRU, EyebrowKY: b.EyebrowKY,
		TitleRU: b.TitleRU, TitleKY: b.TitleKY,
		ButtonRU: b.ButtonRU, ButtonKY: b.ButtonKY,
		CategoryID: deref(b.LinkCategoryID),
		BgColor:    b.BgColor, TextColor: b.TextColor, EyebrowColor: b.EyebrowColor,
		ImageURL:   objectURL(deref(b.ImageKey)),
		BgImageURL: objectURL(deref(b.BgImageKey)),
	}
	if !b.UpdatedAt.IsZero() {
		f.UpdatedAt = b.UpdatedAt.In(reports.Location).Format("02.01.2006 15:04")
	}
	return f
}

func (p *bannerPages) page(w http.ResponseWriter, r *http.Request) {
	b, err := p.store.Get(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: loading banner failed", "err", err)
		http.Error(w, p.h.tr(r).T("admin.banner.load_failed"), http.StatusInternalServerError)
		return
	}
	toast := ""
	if r.URL.Query().Get("saved") == "1" {
		toast = p.h.tr(r).T("admin.banner.saved")
	}
	p.render(w, r, newBannerForm(b, p.objectURL), "", toast)
}

func (p *bannerPages) render(w http.ResponseWriter, r *http.Request, form bannerForm, errMsg, toast string) {
	st, _ := staff.FromContext(r.Context())
	tree, err := p.categoryTree(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: loading categories failed", "err", err)
		http.Error(w, p.h.tr(r).T("admin.categories.load_failed"), http.StatusInternalServerError)
		return
	}
	data := p.h.shellPageData("banner", "admin.nav.banner", st)
	data.Toast = toast
	data.Data = bannerPageData{
		Form: form, Categories: flattenCategoryParentOptions(tree, 0), Error: errMsg,
		MaxEyebrow: banner.MaxEyebrowLen, MaxTitle: banner.MaxTitleLen, MaxButton: banner.MaxButtonLen,
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	if err := p.h.render.Render(w, "banner", data); err != nil {
		slog.ErrorContext(r.Context(), "admin: rendering banner failed", "err", err)
	}
}

// localizedErrMessage is the admin-facing text of a save error: an apperr
// in the page language with its variant and params (errors.<lang>.yaml),
// anything else a generic line (details only in the log).
func localizedErrMessage(t tr, err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return apperr.Localize(t.Lang(), ae)
	}
	return t.T("admin.err.generic")
}

// save handles POST /admin/banner (multipart: the text fields plus the
// optional "image" / "bg_image" files). The texts are validated before
// any picture is uploaded; a picture uploaded for a save that then fails
// is removed again. Replaced pictures are kept in the bucket, like
// replaced product photos — a page or app response cached a minute ago
// may still point at them.
func (p *bannerPages) save(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t := p.h.tr(r)
	r.Body = http.MaxBytesReader(w, r.Body, bannerFormMaxBytes)
	if err := r.ParseMultipartForm(bannerFormMemory); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		p.rerenderStored(w, r, uploadParseError(t, err))
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	current, err := p.store.Get(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "admin: loading banner failed", "err", err)
		http.Error(w, t.T("admin.banner.load_failed"), http.StatusInternalServerError)
		return
	}

	in := bannerInputFromForm(r, current).Normalized()
	form := stickyBannerForm(in, current, p.objectURL)
	if err := in.Validate(); err != nil {
		p.render(w, r, form, localizedErrMessage(t, err), "")
		return
	}

	uploads, err := p.uploadPictures(ctx, r)
	if err != nil {
		p.removeObjects(ctx, uploads.stored)
		if !isAppErr(err) {
			slog.ErrorContext(ctx, "admin: uploading banner picture failed", "err", err)
		}
		p.render(w, r, form, localizedErrMessage(t, err), "")
		return
	}

	saved, err := p.store.Update(ctx, uploads.apply(in))
	if err != nil {
		p.removeObjects(ctx, uploads.stored)
		if !isAppErr(err) {
			slog.ErrorContext(ctx, "admin: saving banner failed", "err", err)
		}
		p.render(w, r, form, localizedErrMessage(t, err), "")
		return
	}
	p.h.audit.Record(ctx, bannerAuditEntry(current, saved))

	http.Redirect(w, r, "/admin/banner?saved=1", http.StatusSeeOther)
}

func isAppErr(err error) bool {
	var ae *apperr.AppError
	return errors.As(err, &ae)
}

// rerenderStored shows the stored banner with errMsg — for a body that
// couldn't be parsed at all, so there is nothing to keep sticky.
func (p *bannerPages) rerenderStored(w http.ResponseWriter, r *http.Request, errMsg string) {
	b, err := p.store.Get(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "admin: loading banner failed", "err", err)
		http.Error(w, p.h.tr(r).T("admin.banner.load_failed"), http.StatusInternalServerError)
		return
	}
	p.render(w, r, newBannerForm(b, p.objectURL), errMsg, "")
}

func uploadParseError(t tr, err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return localizedErrMessage(t, apperr.BadRequest("file_too_large", "файл больше 10 МБ"))
	}
	return t.T("admin.err.form")
}

// bannerInputFromForm reads the text fields. The pictures start as the
// current ones, minus those whose "remove" box is checked; uploadPictures
// then swaps in new uploads.
func bannerInputFromForm(r *http.Request, current *banner.Banner) banner.Input {
	categoryID := r.FormValue("link_category_id")
	in := banner.Input{
		Enabled:   r.FormValue("enabled") == "1",
		EyebrowRU: r.FormValue("eyebrow_ru"), EyebrowKY: r.FormValue("eyebrow_ky"),
		TitleRU: r.FormValue("title_ru"), TitleKY: r.FormValue("title_ky"),
		ButtonRU: r.FormValue("button_ru"), ButtonKY: r.FormValue("button_ky"),
		LinkCategoryID: &categoryID,
		BgColor:        r.FormValue("bg_color"),
		TextColor:      r.FormValue("text_color"),
		EyebrowColor:   r.FormValue("eyebrow_color"),
		ImageKey:       current.ImageKey,
		BgImageKey:     current.BgImageKey,
	}
	if r.FormValue("remove_image") == "1" {
		in.ImageKey = nil
	}
	if r.FormValue("remove_bg_image") == "1" {
		in.BgImageKey = nil
	}
	return in
}

// stickyBannerForm re-renders the submitted values after a failed save,
// with the stored pictures (nothing new has been stored yet).
func stickyBannerForm(in banner.Input, current *banner.Banner, objectURL func(string) string) bannerForm {
	f := newBannerForm(current, objectURL)
	f.Enabled = in.Enabled
	f.EyebrowRU, f.EyebrowKY = in.EyebrowRU, in.EyebrowKY
	f.TitleRU, f.TitleKY = in.TitleRU, in.TitleKY
	f.ButtonRU, f.ButtonKY = in.ButtonRU, in.ButtonKY
	f.CategoryID = deref(in.LinkCategoryID)
	f.BgColor, f.TextColor, f.EyebrowColor = in.BgColor, in.TextColor, in.EyebrowColor
	return f
}

// pictureUploads are the pictures stored for one save: the new keys
// ("" = no new file in that field) and every key stored, for cleanup.
type pictureUploads struct {
	image, bgImage string
	stored         []string
}

// apply returns a copy of in pointing at the newly uploaded pictures.
func (u pictureUploads) apply(in banner.Input) banner.Input {
	out := in
	if u.image != "" {
		key := u.image
		out.ImageKey = &key
	}
	if u.bgImage != "" {
		key := u.bgImage
		out.BgImageKey = &key
	}
	return out
}

// uploadPictures stores the "image" / "bg_image" files, if any. On error
// the result still lists what was stored, so the caller can remove it.
func (p *bannerPages) uploadPictures(ctx context.Context, r *http.Request) (pictureUploads, error) {
	var u pictureUploads
	image, err := p.uploadPicture(ctx, r, "image", media.BannerPicture)
	if err != nil {
		return u, err
	}
	if image != "" {
		u = pictureUploads{image: image, stored: []string{image}}
	}
	bg, err := p.uploadPicture(ctx, r, "bg_image", media.BannerBackground)
	if err != nil {
		return u, err
	}
	if bg != "" {
		u = pictureUploads{image: u.image, bgImage: bg, stored: append(append([]string{}, u.stored...), bg)}
	}
	return u, nil
}

// uploadPicture stores the named file as kind; "" when no file was chosen.
func (p *bannerPages) uploadPicture(ctx context.Context, r *http.Request, field string, kind media.BannerImageKind) (string, error) {
	data, err := readUploadFile(r, field)
	if err != nil || data == nil {
		return "", err
	}
	return p.images.StoreBannerImage(ctx, data, kind)
}

// readUploadFile returns the bytes of the named file part, or nil when
// none was chosen. Files over the media limit are rejected here already.
func readUploadFile(r *http.Request, field string) ([]byte, error) {
	if r.MultipartForm == nil {
		return nil, nil
	}
	file, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", field, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, media.MaxUploadFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", field, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) > media.MaxUploadFileBytes {
		return nil, apperr.BadRequest("file_too_large", "файл больше 10 МБ")
	}
	return data, nil
}

// removeObjects deletes freshly uploaded pictures of a failed save. Best
// effort: a leftover object only costs storage.
func (p *bannerPages) removeObjects(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := p.images.RemoveObject(context.WithoutCancel(ctx), key); err != nil {
			slog.ErrorContext(ctx, "admin: removing orphaned banner image failed", "key", key, "err", err)
		}
	}
}

// bannerAuditEntry journals a banner save with the fields that changed.
func bannerAuditEntry(before, after *banner.Banner) audit.Entry {
	d := map[string]any{}
	add := func(field string, from, to any) {
		if from != to {
			d[field] = audit.Change{From: from, To: to}
		}
	}
	add("enabled", before.Enabled, after.Enabled)
	add("banner_eyebrow_ru", before.EyebrowRU, after.EyebrowRU)
	add("banner_eyebrow_ky", before.EyebrowKY, after.EyebrowKY)
	add("banner_title_ru", before.TitleRU, after.TitleRU)
	add("banner_title_ky", before.TitleKY, after.TitleKY)
	add("banner_button_ru", before.ButtonRU, after.ButtonRU)
	add("banner_button_ky", before.ButtonKY, after.ButtonKY)
	add("banner_link", deref(before.LinkCategorySlug), deref(after.LinkCategorySlug))
	add("banner_bg_color", before.BgColor, after.BgColor)
	add("banner_text_color", before.TextColor, after.TextColor)
	add("banner_eyebrow_color", before.EyebrowColor, after.EyebrowColor)
	add("banner_image", deref(before.ImageKey), deref(after.ImageKey))
	add("banner_bg_image", deref(before.BgImageKey), deref(after.BgImageKey))
	return audit.Entry{
		Action: audit.ActionBannerUpdate, EntityType: audit.EntityBanner, EntityID: "home",
		Summary: russianAuditSummary(audit.MsgBannerUpdated, nil, ""),
		MsgKey:  audit.MsgBannerUpdated,
		Details: d,
	}
}

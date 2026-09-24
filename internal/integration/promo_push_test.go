//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/broadcasts"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
	"github.com/Nikemas/cozy_backend/internal/notifications"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/push"
	"github.com/Nikemas/cozy_backend/internal/storefront"
)

func TestCustomerLangAndPromoPush(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 1, 1)
	repo := storefront.NewCustomerRepo(testDB)

	c, err := repo.GetByID(ctx, f.CustomerID)
	if err != nil || c.Lang != "ru" || !c.PromoPush {
		t.Fatalf("defaults = %+v, %v; want lang ru, promo_push true", c, err)
	}

	lang, off := "ky", false
	c, err = repo.UpdateProfile(ctx, f.CustomerID, storefront.ProfileUpdate{Lang: &lang, PromoPush: &off})
	if err != nil || c.Lang != "ky" || c.PromoPush || c.Name == nil || *c.Name != "Тест" {
		t.Fatalf("after partial update = %+v, %v (name must be untouched)", c, err)
	}

	bad := "en"
	if _, err := repo.UpdateProfile(ctx, f.CustomerID, storefront.ProfileUpdate{Lang: &bad}); err == nil {
		t.Error("lang=en accepted")
	}

	if err := repo.SetLang(ctx, f.CustomerID, "ru"); err != nil {
		t.Fatal(err)
	}
	contact, err := notifications.NewSQLContactLookup(testDB).CustomerContact(ctx, f.CustomerID)
	if err != nil || contact.Lang != "ru" || !strings.HasPrefix(contact.Phone, "+996") {
		t.Fatalf("contact = %+v, %v", contact, err)
	}

	// A deleted (anonymized) customer has no phone for the SMS fallback.
	if _, err := testDB.ExecContext(ctx, `UPDATE customers SET deleted_at = now() WHERE id = $1`, f.CustomerID); err != nil {
		t.Fatal(err)
	}
	contact, err = notifications.NewSQLContactLookup(testDB).CustomerContact(ctx, f.CustomerID)
	if err != nil || contact.Phone != "" {
		t.Fatalf("deleted customer contact = %+v, %v; want no phone", contact, err)
	}
}

// TestFavoritesMatchProductDetail logs a customer in through the real OTP
// flow (mock SMS) and checks that every GET /api/v1/favorites item is
// byte-for-byte the GET /api/v1/products/{id} object.
func TestFavoritesMatchProductDetail(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 4, 0)
	if _, err := testDB.ExecContext(ctx,
		`INSERT INTO product_images (product_id, object_key, sort_order) VALUES ($1, 'products/it-full.jpg', 0)`, f.ProductID); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{MinIOEndpoint: "media.test", MinIOBucket: "cozy-media"}
	authSvc := auth.NewService(testDB, notify.NewMockClient(), []byte("0123456789abcdef0123456789abcdef"), config.AuthLimits{
		OTPPerIPPerHour: 100, OTPPerDay: 1000, OTPVerifyMaxAttempts: 5, OTPVerifyFailsPerIPPerHour: 100, RefreshPerIPPerMinute: 100,
	})
	phone := uniquePhone()
	if err := authSvc.RequestOTP(ctx, phone); err != nil {
		t.Fatal(err)
	}
	access, _, customer, err := authSvc.VerifyOTP(ctx, phone, "0000")
	if err != nil {
		t.Fatal(err)
	}
	if customer.Lang != "ru" || !customer.PromoPush {
		t.Errorf("new customer = %+v, want lang ru and promo_push on", customer)
	}
	if err := storefront.NewFavoriteRepo(testDB).Add(ctx, customer.ID, f.ProductID); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	httpapi.RegisterCatalogRoutes(mux, testDB, cfg)
	httpapi.RegisterCustomerRoutes(mux, testDB, authSvc, cfg)
	get := func(path string) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+access)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}

	var favs struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(get("/api/v1/favorites"), &favs); err != nil || len(favs.Items) != 1 {
		t.Fatalf("favorites = %+v, %v", favs, err)
	}
	detail := strings.TrimSpace(string(get("/api/v1/products/" + f.ProductID)))
	if string(favs.Items[0]) != detail {
		t.Fatalf("favorite item differs from product detail:\n fav: %s\n pdp: %s", favs.Items[0], detail)
	}
	for _, want := range []string{`"variants":[`, `"stock":[{"point_id":"` + f.PointA + `","quantity":4}]`, `products/it-full.jpg`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %s: %s", want, detail)
		}
	}

	// Profile exposes the new fields; PUT is partial.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/customer", strings.NewReader(`{"promo_push":false}`))
	req.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"promo_push":false`) || !strings.Contains(w.Body.String(), `"lang":"ru"`) {
		t.Fatalf("PUT /customer: %d %s", w.Code, w.Body.String())
	}
}

func TestOrderItemsCarryProductAndPhoto(t *testing.T) {
	t.Parallel()
	ctx := ctxT(t)
	f := newFixture(t, 5, 5)
	// A black-tagged photo must win over the general one for a black variant.
	if _, err := testDB.ExecContext(ctx, `
		INSERT INTO product_images (product_id, object_key, sort_order, color)
		VALUES ($1, 'products/general.jpg', 0, NULL), ($1, 'products/black.jpg', 1, 'black')`, f.ProductID); err != nil {
		t.Fatal(err)
	}
	orders.SetItemPhotoURLs(func(key string) (string, string) { return "https://m/" + key, "https://m/t/" + key })

	svc := orders.NewService(testDB)
	o, err := svc.CreateOrder(ctx, f.CustomerID, []orders.OrderItemInput{{VariantID: f.VariantA, Quantity: 1}}, nil, &f.PointA)
	if err != nil {
		t.Fatal(err)
	}
	check := func(label string, o *orders.Order) {
		t.Helper()
		it := o.Items[0]
		if it.ProductID == nil || *it.ProductID != f.ProductID ||
			it.PhotoURL == nil || *it.PhotoURL != "https://m/products/black.jpg" ||
			it.ThumbURL == nil || *it.ThumbURL != "https://m/t/products/black.jpg" {
			t.Errorf("%s: item = %+v", label, it)
		}
	}
	check("create", o)
	got, err := svc.GetOrder(ctx, f.CustomerID, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("get", got)
}

type countingPush struct {
	sent map[string]push.Message
}

func (p *countingPush) Send(_ context.Context, token string, msg push.Message) error {
	if strings.HasPrefix(token, "dead-") {
		return push.ErrInvalidToken
	}
	p.sent[token] = msg
	return nil
}

func TestBroadcastWorkerEndToEnd(t *testing.T) {
	// Not parallel: the audience is every promo_push device in the shared
	// database, so other tests' customers are cleaned of devices first.
	ctx := ctxT(t)
	if _, err := testDB.ExecContext(ctx, `DELETE FROM device_tokens`); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 1, 1)
	optedOut, ky := newCustomer(t), newCustomer(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := testDB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`UPDATE customers SET promo_push = false WHERE id = $1`, optedOut)
	mustExec(`UPDATE customers SET lang = 'ky' WHERE id = $1`, ky)
	mustExec(`INSERT INTO device_tokens (customer_id, fcm_token, platform) VALUES
		($1, 'ru-1', 'android'), ($1, 'dead-1', 'ios'), ($2, 'optout-1', 'android'), ($3, 'ky-1', 'ios')`,
		f.CustomerID, optedOut, ky)

	repo := broadcasts.NewRepo(testDB)
	in := broadcasts.Input{TitleRU: "Скидки", BodyRU: "−20%", TitleKY: "Арзандатуу",
		LinkType: broadcasts.LinkProduct, LinkID: f.ProductID}
	id, created, err := repo.Create(ctx, in, "it-token-"+f.ProductID[:8], broadcasts.Author{Name: "Тест"})
	if err != nil || !created {
		t.Fatalf("Create = %q, %v, %v", id, created, err)
	}
	again, created, err := repo.Create(ctx, in, "it-token-"+f.ProductID[:8], broadcasts.Author{Name: "Тест"})
	if err != nil || created || again != id {
		t.Fatalf("resubmit = %q, %v, %v; want same id, not created", again, created, err)
	}

	p := &countingPush{sent: map[string]push.Message{}}
	wctx, stop := context.WithCancel(context.Background())
	done := broadcasts.NewWorker(repo, p, broadcasts.WorkerConfig{BatchSize: 2, PollInterval: 50 * time.Millisecond}).Start(wctx)
	deadline := time.Now().Add(10 * time.Second)
	var b *broadcasts.Broadcast
	for {
		b, err = repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status == broadcasts.StatusDone || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	<-done

	if b.Status != broadcasts.StatusDone || b.Targets != 3 || b.Sent != 2 || b.InvalidRemoved != 1 || b.Failed != 0 {
		t.Fatalf("broadcast = status %s targets %d sent %d invalid %d failed %d; want done 3/2/1/0",
			b.Status, b.Targets, b.Sent, b.InvalidRemoved, b.Failed)
	}
	if _, ok := p.sent["optout-1"]; ok {
		t.Error("customer with promo_push=false got the promo")
	}
	if p.sent["ky-1"].Title != "Арзандатуу" || p.sent["ru-1"].Title != "Скидки" {
		t.Errorf("titles: ky %q ru %q", p.sent["ky-1"].Title, p.sent["ru-1"].Title)
	}
	if got := p.sent["ru-1"].Data; got["type"] != "promo" || got["link"] != "/product/"+f.ProductID {
		t.Errorf("data = %v", got)
	}
	var left int
	if err := testDB.QueryRowContext(ctx, `SELECT count(*) FROM device_tokens WHERE fcm_token = 'dead-1'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("invalid token not deleted (%d left, %v)", left, err)
	}
	if b.LinkLabel == "" {
		t.Error("link label (product name) not stored")
	}
}

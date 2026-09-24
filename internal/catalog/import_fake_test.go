package catalog

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// memStore is an in-memory ImportStore that mimics the SQL one closely
// enough for the importer's logic: the same uniqueness rules (SKU,
// product+size+color, model code) as the schema, per-model rollback, and a
// dry run that discards everything.
type memStore struct {
	cats         map[string]string // lower(slug or name) -> id
	points       map[string]bool   // id -> active
	defaultPoint string

	st memState

	// failCreateVariantAfter > 0 makes the Nth CreateVariant call fail with
	// a generic database error (to prove per-model rollback).
	failCreateVariantAfter int
	createVariantCalls     int
}

type memProduct struct {
	ImportProduct
	id string
}

type memVariant struct {
	ImportVariant
	id, productID string
}

type memState struct {
	seq      int
	products map[string]memProduct
	variants map[string]memVariant
	stock    map[[2]string]int
}

func (s memState) clone() memState {
	return memState{seq: s.seq, products: maps.Clone(s.products), variants: maps.Clone(s.variants), stock: maps.Clone(s.stock)}
}

const (
	testCatSneakers = "11111111-1111-1111-1111-111111111111"
	testCatBoots    = "22222222-2222-2222-2222-222222222222"
	testPointA      = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testPointB      = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	testPointOff    = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func newMemStore() *memStore {
	return &memStore{
		cats: map[string]string{
			"sneakers": testCatSneakers, "кроссовки": testCatSneakers, testCatSneakers: testCatSneakers,
			"boots": testCatBoots, "ботинки": testCatBoots,
		},
		points:       map[string]bool{testPointA: true, testPointB: true, testPointOff: false},
		defaultPoint: testPointA,
		st:           memState{products: map[string]memProduct{}, variants: map[string]memVariant{}, stock: map[[2]string]int{}},
	}
}

func (s *memStore) ResolveCategory(_ context.Context, raw string) (string, error) {
	if id, ok := s.cats[strings.ToLower(raw)]; ok {
		return id, nil
	}
	return "", apperr.BadRequest("category_not_found", fmt.Sprintf("категория %q не найдена", raw))
}

func (s *memStore) ActivePoint(_ context.Context, id string) (bool, error) { return s.points[id], nil }

func (s *memStore) DefaultPoint(context.Context) (string, error) { return s.defaultPoint, nil }

func (s *memStore) Begin(_ context.Context, dryRun bool) (ImportSession, error) {
	sess := &memSession{s: s}
	if dryRun {
		snap := s.st.clone()
		sess.restore = &snap
	}
	return sess, nil
}

type memSession struct {
	s       *memStore
	restore *memState
}

func (m *memSession) Model(_ context.Context, fn func(ImportTx) error) error {
	snap := m.s.st.clone()
	if err := fn(memTx{m.s}); err != nil {
		m.s.st = snap
		return err
	}
	return nil
}

func (m *memSession) Close() error {
	if m.restore != nil {
		m.s.st = *m.restore
	}
	return nil
}

type memTx struct{ s *memStore }

var errUnique = &pgconn.PgError{Code: pgUniqueViolation}

func (t memTx) nextID(prefix string) string {
	t.s.st.seq++
	return fmt.Sprintf("%s-%d", prefix, t.s.st.seq)
}

func (t memTx) ProductByModelCode(_ context.Context, code string) (string, error) {
	for _, p := range t.s.st.products {
		if p.ModelCode != "" && strings.EqualFold(p.ModelCode, code) {
			return p.id, nil
		}
	}
	return "", nil
}

func (t memTx) ProductsByName(_ context.Context, brand, nameRu, categoryID, modelCode string) ([]string, error) {
	var ids []string
	for _, p := range t.s.st.products {
		if p.CategoryID == categoryID && strings.EqualFold(p.NameRu, nameRu) && strings.EqualFold(p.Brand, brand) &&
			(modelCode == "" || p.ModelCode == "") {
			ids = append(ids, p.id)
		}
	}
	return ids, nil
}

func (t memTx) checkModelCode(id, code string) error {
	if code == "" {
		return nil
	}
	if other, _ := t.ProductByModelCode(context.Background(), code); other != "" && other != id {
		return errUnique
	}
	return nil
}

func (t memTx) CreateProduct(_ context.Context, p ImportProduct) (string, error) {
	if err := t.checkModelCode("", p.ModelCode); err != nil {
		return "", err
	}
	if p.NameKy == "" {
		p.NameKy = p.NameRu
	}
	id := t.nextID("product")
	t.s.st.products[id] = memProduct{ImportProduct: p, id: id}
	return id, nil
}

func (t memTx) UpdateProduct(_ context.Context, id string, p ImportProduct) error {
	cur, ok := t.s.st.products[id]
	if !ok {
		return errors.New("no such product")
	}
	if err := t.checkModelCode(id, p.ModelCode); err != nil {
		return err
	}
	keep := func(newV, old string) string {
		if newV == "" {
			return old
		}
		return newV
	}
	cur.CategoryID, cur.NameRu, cur.BasePrice = p.CategoryID, p.NameRu, p.BasePrice
	cur.NameKy = keep(p.NameKy, cur.NameKy)
	cur.Brand = keep(p.Brand, cur.Brand)
	cur.DescriptionRu = keep(p.DescriptionRu, cur.DescriptionRu)
	cur.DescriptionKy = keep(p.DescriptionKy, cur.DescriptionKy)
	cur.ModelCode = keep(p.ModelCode, cur.ModelCode)
	t.s.st.products[id] = cur
	return nil
}

func (t memTx) VariantBySKU(_ context.Context, sku string) (*ImportVariantRef, error) {
	for _, v := range t.s.st.variants {
		if v.SKU == sku {
			return &ImportVariantRef{ID: v.id, ProductID: v.productID, ProductModelCode: t.s.st.products[v.productID].ModelCode}, nil
		}
	}
	return nil, nil
}

func (t memTx) VariantBySizeColor(_ context.Context, productID, size, color string) (*ImportVariantRef, error) {
	for _, v := range t.s.st.variants {
		if v.productID == productID && strings.EqualFold(v.Size, size) && strings.EqualFold(v.Color, color) {
			return &ImportVariantRef{ID: v.id, ProductID: v.productID}, nil
		}
	}
	return nil, nil
}

func (t memTx) checkVariant(id, productID string, v ImportVariant) error {
	for _, o := range t.s.st.variants {
		if o.id == id {
			continue
		}
		if v.SKU != "" && o.SKU == v.SKU {
			return errUnique
		}
		if o.productID == productID && o.Size == v.Size && o.Color == v.Color {
			return errUnique
		}
	}
	return nil
}

func (t memTx) CreateVariant(_ context.Context, productID string, v ImportVariant) (string, error) {
	t.s.createVariantCalls++
	if t.s.failCreateVariantAfter > 0 && t.s.createVariantCalls >= t.s.failCreateVariantAfter {
		return "", errors.New("connection reset")
	}
	if err := t.checkVariant("", productID, v); err != nil {
		return "", err
	}
	id := t.nextID("variant")
	t.s.st.variants[id] = memVariant{ImportVariant: v, id: id, productID: productID}
	return id, nil
}

func (t memTx) UpdateVariant(_ context.Context, id string, v ImportVariant) error {
	cur, ok := t.s.st.variants[id]
	if !ok {
		return errors.New("no such variant")
	}
	if v.SKU == "" {
		v.SKU = cur.SKU
	}
	if err := t.checkVariant(id, cur.productID, v); err != nil {
		return err
	}
	cur.ImportVariant = v
	t.s.st.variants[id] = cur
	return nil
}

func (t memTx) SetStock(_ context.Context, variantID, pointID string, qty int) error {
	if _, ok := t.s.points[pointID]; !ok {
		return &pgconn.PgError{Code: pgForeignKeyViolation}
	}
	t.s.st.stock[[2]string{variantID, pointID}] = qty
	return nil
}

// --- helpers for assertions ---

func (s *memStore) productByName(name string) (memProduct, bool) {
	for _, p := range s.st.products {
		if p.NameRu == name {
			return p, true
		}
	}
	return memProduct{}, false
}

func (s *memStore) variantBySKU(sku string) (memVariant, bool) {
	for _, v := range s.st.variants {
		if v.SKU == sku {
			return v, true
		}
	}
	return memVariant{}, false
}

func (s *memStore) variantsOf(productID string) []memVariant {
	var out []memVariant
	for _, v := range s.st.variants {
		if v.productID == productID {
			out = append(out, v)
		}
	}
	return out
}

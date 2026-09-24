package catalog

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

// TestParseXLSXRowsRefusesOversizedUnzip: a workbook that decompresses
// past xlsxUnzipSizeLimit is rejected as invalid instead of being inflated
// into memory (zip-bomb guard).
func TestParseXLSXRowsRefusesOversizedUnzip(t *testing.T) {
	rows := [][]string{{"name", "sku"}}
	for i := 0; i < 200; i++ {
		rows = append(rows, []string{strings.Repeat("x", 200), "SKU"})
	}
	data := buildXLSX(t, rows)

	if _, err := parseXLSXRows(bytes.NewReader(data)); err != nil {
		t.Fatalf("within default limits: %v", err)
	}

	oldTotal, oldXML := xlsxUnzipSizeLimit, xlsxUnzipXMLSizeLimit
	defer func() { xlsxUnzipSizeLimit, xlsxUnzipXMLSizeLimit = oldTotal, oldXML }()
	xlsxUnzipSizeLimit, xlsxUnzipXMLSizeLimit = 4<<10, 4<<10

	_, err := parseXLSXRows(bytes.NewReader(data))
	if err == nil {
		t.Fatal("want an error for a workbook over the unzip limit")
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "invalid_xlsx" {
		t.Fatalf("err = %v, want invalid_xlsx", err)
	}
}

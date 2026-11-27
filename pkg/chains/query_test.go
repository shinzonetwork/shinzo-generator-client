package chains

import (
	"os"
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/constants"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/logger"
	"github.com/stretchr/testify/assert"
)

// TestMain initializes the console-only logger so the shared query helpers
// can warn through logger.Sugar without a nil logger in the pure-function
// tests, and so DefraDB-backed tests never write a logs directory into the
// source tree.
func TestMain(m *testing.M) {
	logger.InitConsoleOnly(true)
	os.Exit(m.Run())
}

// TestParseBlockNumberRow pins the accepted number encodings: the JSON wire
// form arrives as float64, while int64 and int cover direct construction;
// everything else (nil, missing, string) must report unparseable.
func TestParseBlockNumberRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		row   map[string]any
		want  int64
		valid bool
	}{
		{"float64", map[string]any{constants.NumberFieldName: float64(100)}, 100, true},
		{"int64", map[string]any{constants.NumberFieldName: int64(101)}, 101, true},
		{"int", map[string]any{constants.NumberFieldName: int(102)}, 102, true},
		{"nil", map[string]any{constants.NumberFieldName: nil}, 0, false},
		{"missing", map[string]any{}, 0, false},
		{"string", map[string]any{constants.NumberFieldName: "100"}, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseBlockNumberRow(tc.row, constants.NumberFieldName)
			assert.Equal(t, tc.valid, valid)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestFirstUsableRow exercises the skip-and-summarize behavior directly:
// the first parseable row wins, unparseable rows are summarized into the
// all-corrupt error, and that error wraps the corruption sentinel so
// consumers can distinguish it from a benign not-found.
func TestFirstUsableRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      []any
		want      int64
		wantErr   bool
		errSubstr string
	}{
		{
			name: "first row wins",
			rows: []any{
				map[string]any{constants.NumberFieldName: float64(7), docIDField: "d7"},
			},
			want: 7,
		},
		{
			name: "skips corrupt rows then finds a usable one",
			rows: []any{
				map[string]any{constants.NumberFieldName: nil, docIDField: "d1"},
				"not-a-map",
				map[string]any{constants.NumberFieldName: float64(9), docIDField: "d9"},
			},
			want: 9,
		},
		{
			name: "all rows corrupt",
			rows: []any{
				map[string]any{constants.NumberFieldName: nil, docIDField: "d1"},
				map[string]any{docIDField: "d2"},
			},
			wantErr:   true,
			errSubstr: "all 2 rows corrupt",
		},
		{
			name:      "non-map rows are reported verbatim",
			rows:      []any{"junk"},
			wantErr:   true,
			errSubstr: `row="junk"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := firstUsableRow(tc.rows, constants.NumberFieldName, "TestFirstUsableRow")
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrBlockNumberCorrupt)
				assert.Contains(t, err.Error(), tc.errSubstr)
				assert.Zero(t, got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

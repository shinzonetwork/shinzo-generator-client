package evm

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sdlDataFields returns the data-field names declared on the given type in
// the SDL. Relation lines (marked @relation) are not data fields.
func sdlDataFields(t *testing.T, sdl, typeName string) []string {
	t.Helper()
	var fields []string
	scanner := bufio.NewScanner(strings.NewReader(sdl))
	inType := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "type "+typeName+" ") {
			inType = true
			continue
		}
		if !inType {
			continue
		}
		if strings.HasPrefix(line, "}") {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "@relation") {
			continue
		}
		name, _, _ := strings.Cut(line, ":")
		fields = append(fields, strings.TrimSpace(name))
	}
	require.NotEmpty(t, fields, "type %s not found in SDL", typeName)
	sort.Strings(fields)
	return fields
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestPolygonSDL_MatchesGolden pins the full Polygon SDL to the reviewed
// reference (identical to `build_schema -prefix Polygon__Mainnet` output;
// cmd delegates to GetSchemaForChain, pinned by the cmd's own tests).
func TestPolygonSDL_MatchesGolden(t *testing.T) {
	t.Parallel()
	c := NewConverter(polygonConfig())
	sdl, err := c.GetSchema()
	require.NoError(t, err)

	golden, err := os.ReadFile("testdata/polygon_schema_reference.graphql")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(golden)), strings.TrimSpace(sdl))
}

// TestPolygonSDL_FieldAlignment checks that every data field in the Polygon
// SDL has a converter-emitted doc key and vice versa: no schema field nothing
// writes, no doc field the schema doesn't declare.
func TestPolygonSDL_FieldAlignment(t *testing.T) {
	t.Parallel()
	c := NewConverter(polygonConfig())
	sdl, err := c.GetSchema()
	require.NoError(t, err)

	// Harvest converter doc keys from the builders themselves.
	blockDoc := c.buildBlockDataFn(fakeBlock(1), 1)
	txDoc := c.buildTransactionDataFn(&Transaction{BlockNumber: "1"})
	logDoc := c.buildLogData(&Log{BlockNumber: "0x1"})
	aleDoc := c.buildALEData(&AccessListEntry{}, 1)

	for _, tc := range []struct {
		typeName string
		doc      map[string]any
	}{
		{"Polygon__Mainnet__Block", blockDoc},
		{"Polygon__Mainnet__Transaction", txDoc},
		{"Polygon__Mainnet__Log", logDoc},
		{"Polygon__Mainnet__AccessListEntry", aleDoc},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			assert.Equal(t, sdlDataFields(t, sdl, tc.typeName), sortedKeys(tc.doc))
		})
	}
}

package chains_test

import (
	"testing"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/stretchr/testify/assert"
)

func TestMergeSDL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []chains.CollectionFile
		want  string
	}{
		{
			name:  "nil slice yields empty document",
			files: nil,
			want:  "",
		},
		{
			name:  "empty slice yields empty document",
			files: []chains.CollectionFile{},
			want:  "",
		},
		{
			name:  "single pair yields its SDL unchanged",
			files: []chains.CollectionFile{{Name: "alpha", SDL: "type Alpha"}},
			want:  "type Alpha",
		},
		{
			name: "pairs join in order with blank lines",
			files: []chains.CollectionFile{
				{Name: "alpha", SDL: "type Alpha"},
				{Name: "beta", SDL: "type Beta"},
				{Name: "gamma", SDL: "type Gamma"},
			},
			want: "type Alpha\n\ntype Beta\n\ntype Gamma",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, chains.MergeSDL(tt.files))
		})
	}
}

package migration

import (
	"context"
	"fmt"
	"math/big"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// Validator handles data validation between providers and RPC
type Validator struct {
	rpcClient *ethclient.Client
	providers map[string]DataProvider
}

// ValidationReport contains the results of validation
type ValidationReport struct {
	BlocksChecked     int
	BlocksMatched     int
	BlocksMismatched  int
	Mismatches        []Mismatch
	ProviderCoverage  map[string]int
	RecommendedSource string
}

// Mismatch represents a data mismatch between sources
type Mismatch struct {
	BlockNumber int64
	Field       string
	Source1     string
	Source2     string
	Value1      interface{}
	Value2      interface{}
	Severity    string // "critical", "warning", "info"
}

// NewValidator creates a new validator
func NewValidator(rpcURL string) (*Validator, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC: %w", err)
	}

	return &Validator{
		rpcClient: client,
		providers: make(map[string]DataProvider),
	}, nil
}

// AddProvider adds a data provider for comparison
func (v *Validator) AddProvider(name string, provider DataProvider) {
	v.providers[name] = provider
}

// ValidateBlockRange validates a range of blocks across all providers
func (v *Validator) ValidateBlockRange(ctx context.Context, startBlock, endBlock int64, sampleSize int) (*ValidationReport, error) {
	report := &ValidationReport{
		ProviderCoverage: make(map[string]int),
	}

	// Generate sample block numbers
	blockRange := endBlock - startBlock + 1
	if int64(sampleSize) > blockRange {
		sampleSize = int(blockRange)
	}

	sampleBlocks := v.generateSampleBlocks(startBlock, endBlock, sampleSize)
	report.BlocksChecked = len(sampleBlocks)

	logger.Sugar.Infof("Validating %d sample blocks from range %d-%d", sampleSize, startBlock, endBlock)

	for i, blockNum := range sampleBlocks {
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		default:
		}

		mismatches, coverage := v.validateBlock(ctx, blockNum)
		
		// Update coverage stats
		for provider, count := range coverage {
			report.ProviderCoverage[provider] += count
		}

		if len(mismatches) == 0 {
			report.BlocksMatched++
		} else {
			report.BlocksMismatched++
			report.Mismatches = append(report.Mismatches, mismatches...)
		}

		if (i+1)%10 == 0 {
			logger.Sugar.Infof("Progress: %d/%d blocks validated", i+1, sampleSize)
		}
	}

	// Determine recommended source
	report.RecommendedSource = v.determineRecommendedSource(report)

	return report, nil
}

// validateBlock validates a single block across all sources
func (v *Validator) validateBlock(ctx context.Context, blockNum int64) ([]Mismatch, map[string]int) {
	var mismatches []Mismatch
	coverage := make(map[string]int)

	// Fetch from RPC (ground truth)
	rpcBlock, err := v.rpcClient.BlockByNumber(ctx, big.NewInt(blockNum))
	if err != nil {
		logger.Sugar.Warnf("Failed to fetch block %d from RPC: %v", blockNum, err)
		return mismatches, coverage
	}

	rpcHash := rpcBlock.Hash().Hex()
	rpcTxCount := len(rpcBlock.Transactions())

	// Compare with each provider
	for name, provider := range v.providers {
		blocks, err := provider.ReadBlocks(ctx, blockNum, blockNum)
		if err != nil {
			logger.Sugar.Warnf("Failed to fetch block %d from %s: %v", blockNum, name, err)
			continue
		}

		if len(blocks) == 0 {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "block",
				Source1:     "rpc",
				Source2:     name,
				Value1:      rpcHash,
				Value2:      "missing",
				Severity:    "critical",
			})
			continue
		}

		coverage[name]++
		block := blocks[0]

		// Compare hash
		if block.Hash != rpcHash {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "hash",
				Source1:     "rpc",
				Source2:     name,
				Value1:      rpcHash,
				Value2:      block.Hash,
				Severity:    "critical",
			})
		}

		// Compare transaction count
		txs, err := provider.ReadTransactions(ctx, blockNum, blockNum)
		if err == nil {
			if len(txs) != rpcTxCount {
				mismatches = append(mismatches, Mismatch{
					BlockNumber: blockNum,
					Field:       "transaction_count",
					Source1:     "rpc",
					Source2:     name,
					Value1:      rpcTxCount,
					Value2:      len(txs),
					Severity:    "warning",
				})
			}
		}
	}

	return mismatches, coverage
}

// generateSampleBlocks generates a random sample of block numbers
func (v *Validator) generateSampleBlocks(startBlock, endBlock int64, sampleSize int) []int64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	blockRange := endBlock - startBlock + 1
	if int64(sampleSize) >= blockRange {
		// Return all blocks in range
		blocks := make([]int64, blockRange)
		for i := int64(0); i < blockRange; i++ {
			blocks[i] = startBlock + i
		}
		return blocks
	}

	// Generate unique random samples
	selected := make(map[int64]bool)
	blocks := make([]int64, 0, sampleSize)

	for len(blocks) < sampleSize {
		blockNum := startBlock + r.Int63n(blockRange)
		if !selected[blockNum] {
			selected[blockNum] = true
			blocks = append(blocks, blockNum)
		}
	}

	return blocks
}

// determineRecommendedSource determines the best data source based on validation results
func (v *Validator) determineRecommendedSource(report *ValidationReport) string {
	if len(v.providers) == 0 {
		return "rpc"
	}

	// Find provider with highest coverage and fewest mismatches
	type providerScore struct {
		name       string
		coverage   int
		mismatches int
	}

	scores := make(map[string]*providerScore)
	for name := range v.providers {
		scores[name] = &providerScore{name: name}
	}

	for name, count := range report.ProviderCoverage {
		if s, ok := scores[name]; ok {
			s.coverage = count
		}
	}

	for _, m := range report.Mismatches {
		if m.Severity == "critical" {
			if s, ok := scores[m.Source2]; ok {
				s.mismatches++
			}
		}
	}

	// Score providers
	var best string
	var bestScore float64 = -1

	for name, s := range scores {
		if s.coverage == 0 {
			continue
		}
		// Score = coverage / (1 + mismatches)
		score := float64(s.coverage) / float64(1+s.mismatches)
		if score > bestScore {
			bestScore = score
			best = name
		}
	}

	if best == "" {
		return "rpc"
	}
	return best
}

// CompareProviders compares data between two providers for a block range
func (v *Validator) CompareProviders(ctx context.Context, provider1, provider2 string, startBlock, endBlock int64) ([]Mismatch, error) {
	p1, ok := v.providers[provider1]
	if !ok {
		return nil, fmt.Errorf("provider %s not found", provider1)
	}

	p2, ok := v.providers[provider2]
	if !ok {
		return nil, fmt.Errorf("provider %s not found", provider2)
	}

	var mismatches []Mismatch

	// Fetch blocks from both providers
	blocks1, err := p1.ReadBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to read from %s: %w", provider1, err)
	}

	blocks2, err := p2.ReadBlocks(ctx, startBlock, endBlock)
	if err != nil {
		return nil, fmt.Errorf("failed to read from %s: %w", provider2, err)
	}

	// Create maps for comparison
	map1 := make(map[int64]*types.Block)
	for _, b := range blocks1 {
		num := mustParseBlockNumber(b.Number)
		map1[num] = b
	}

	map2 := make(map[int64]*types.Block)
	for _, b := range blocks2 {
		num := mustParseBlockNumber(b.Number)
		map2[num] = b
	}

	// Compare each block
	allBlocks := make(map[int64]bool)
	for k := range map1 {
		allBlocks[k] = true
	}
	for k := range map2 {
		allBlocks[k] = true
	}

	for blockNum := range allBlocks {
		b1, has1 := map1[blockNum]
		b2, has2 := map2[blockNum]

		if !has1 && has2 {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "block",
				Source1:     provider1,
				Source2:     provider2,
				Value1:      "missing",
				Value2:      b2.Hash,
				Severity:    "critical",
			})
			continue
		}

		if has1 && !has2 {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "block",
				Source1:     provider1,
				Source2:     provider2,
				Value1:      b1.Hash,
				Value2:      "missing",
				Severity:    "critical",
			})
			continue
		}

		// Compare fields
		if b1.Hash != b2.Hash {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "hash",
				Source1:     provider1,
				Source2:     provider2,
				Value1:      b1.Hash,
				Value2:      b2.Hash,
				Severity:    "critical",
			})
		}

		if b1.ParentHash != b2.ParentHash {
			mismatches = append(mismatches, Mismatch{
				BlockNumber: blockNum,
				Field:       "parent_hash",
				Source1:     provider1,
				Source2:     provider2,
				Value1:      b1.ParentHash,
				Value2:      b2.ParentHash,
				Severity:    "critical",
			})
		}
	}

	return mismatches, nil
}

// PrintReport prints a formatted validation report
func (v *Validator) PrintReport(report *ValidationReport) {
	fmt.Println("\n" + repeatStr("=", 60))
	fmt.Println("VALIDATION REPORT")
	fmt.Println(repeatStr("=", 60))
	fmt.Printf("Blocks Checked:    %d\n", report.BlocksChecked)
	fmt.Printf("Blocks Matched:    %d (%.1f%%)\n", report.BlocksMatched,
		float64(report.BlocksMatched)/float64(report.BlocksChecked)*100)
	fmt.Printf("Blocks Mismatched: %d (%.1f%%)\n", report.BlocksMismatched,
		float64(report.BlocksMismatched)/float64(report.BlocksChecked)*100)
	fmt.Printf("Recommended Source: %s\n", report.RecommendedSource)

	if len(report.ProviderCoverage) > 0 {
		fmt.Println("\nProvider Coverage:")
		for provider, count := range report.ProviderCoverage {
			fmt.Printf("  %s: %d blocks\n", provider, count)
		}
	}

	if len(report.Mismatches) > 0 {
		fmt.Println("\nMismatches (first 20):")
		for i, m := range report.Mismatches {
			if i >= 20 {
				fmt.Printf("  ... and %d more\n", len(report.Mismatches)-20)
				break
			}
			fmt.Printf("  [%s] Block %d, Field: %s\n", m.Severity, m.BlockNumber, m.Field)
			fmt.Printf("       %s: %v\n", m.Source1, m.Value1)
			fmt.Printf("       %s: %v\n", m.Source2, m.Value2)
		}
	}

	fmt.Println(repeatStr("=", 60))
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

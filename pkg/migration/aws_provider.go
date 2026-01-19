package migration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/logger"
	"github.com/shinzonetwork/shinzo-indexer-client/pkg/types"
)

// AWSProvider handles reading data from AWS Public Blockchain dataset
type AWSProvider struct {
	bucket     string
	prefix     string
	outputDir  string
	httpClient *http.Client
	cache      map[string]bool
	cacheMu    sync.RWMutex
}

// AWSBlock represents a block record from AWS parquet files
// Using parquet-go compatible tags
type AWSBlock struct {
	Number           int64   `parquet:"number"`
	Hash             string  `parquet:"hash"`
	ParentHash       string  `parquet:"parent_hash"`
	Nonce            string  `parquet:"nonce"`
	Sha3Uncles       string  `parquet:"sha3_uncles"`
	LogsBloom        string  `parquet:"logs_bloom"`
	TransactionsRoot string  `parquet:"transactions_root"`
	StateRoot        string  `parquet:"state_root"`
	ReceiptsRoot     string  `parquet:"receipts_root"`
	Miner            string  `parquet:"miner"`
	Difficulty       float64 `parquet:"difficulty"`
	TotalDifficulty  float64 `parquet:"total_difficulty"`
	Size             int64   `parquet:"size"`
	ExtraData        string  `parquet:"extra_data"`
	GasLimit         int64   `parquet:"gas_limit"`
	GasUsed          int64   `parquet:"gas_used"`
	Timestamp        int64   `parquet:"timestamp"`
	TransactionCount int64   `parquet:"transaction_count"`
	BaseFeePerGas    *int64  `parquet:"base_fee_per_gas,optional"`
}

// AWSTransaction represents a transaction record from AWS parquet files
type AWSTransaction struct {
	Hash                     string   `parquet:"hash"`
	Nonce                    int64    `parquet:"nonce"`
	TransactionIndex         int64    `parquet:"transaction_index"`
	FromAddress              string   `parquet:"from_address"`
	ToAddress                *string  `parquet:"to_address,optional"`
	Value                    float64  `parquet:"value"`
	Gas                      int64    `parquet:"gas"`
	GasPrice                 int64    `parquet:"gas_price"`
	Input                    string   `parquet:"input"`
	BlockTimestamp           int64    `parquet:"block_timestamp"`
	BlockNumber              int64    `parquet:"block_number"`
	BlockHash                string   `parquet:"block_hash"`
	MaxFeePerGas             *int64   `parquet:"max_fee_per_gas,optional"`
	MaxPriorityFeePerGas     *int64   `parquet:"max_priority_fee_per_gas,optional"`
	TransactionType          int64    `parquet:"transaction_type"`
	ReceiptCumulativeGasUsed int64    `parquet:"receipt_cumulative_gas_used"`
	ReceiptGasUsed           int64    `parquet:"receipt_gas_used"`
	ReceiptContractAddress   *string  `parquet:"receipt_contract_address,optional"`
	ReceiptStatus            int64    `parquet:"receipt_status"`
	ReceiptEffectiveGasPrice int64    `parquet:"receipt_effective_gas_price"`
}

// AWSLog represents a log record from AWS parquet files
type AWSLog struct {
	LogIndex         int64  `parquet:"log_index"`
	TransactionHash  string `parquet:"transaction_hash"`
	TransactionIndex int64  `parquet:"transaction_index"`
	Address          string `parquet:"address"`
	Data             string `parquet:"data"`
	Topics           string `parquet:"topics"` // JSON array as string
	BlockTimestamp   int64  `parquet:"block_timestamp"`
	BlockNumber      int64  `parquet:"block_number"`
	BlockHash        string `parquet:"block_hash"`
}

// NewAWSProvider creates a new AWS data provider
func NewAWSProvider(bucket, prefix, outputDir string) *AWSProvider {
	return &AWSProvider{
		bucket:    bucket,
		prefix:    prefix,
		outputDir: outputDir,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		cache: make(map[string]bool),
	}
}

// GetBlockRange returns the available block range from the provider
func (p *AWSProvider) GetBlockRange(ctx context.Context) (int64, int64, error) {
	return 0, 21000000, nil
}

// DownloadDatePartition downloads parquet files for a specific date
func (p *AWSProvider) DownloadDatePartition(ctx context.Context, date string, dataType string) (string, error) {
	baseURL := fmt.Sprintf("https://%s.s3.us-east-2.amazonaws.com/%s/%s/date=%s/",
		p.bucket, p.prefix, dataType, date)

	localDir := filepath.Join(p.outputDir, dataType, fmt.Sprintf("date=%s", date))
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	p.cacheMu.RLock()
	if p.cache[localDir] {
		p.cacheMu.RUnlock()
		return localDir, nil
	}
	p.cacheMu.RUnlock()

	logger.Sugar.Infof("Downloading %s data for date %s...", dataType, date)

	files, err := p.listS3Directory(ctx, dataType, date)
	if err != nil {
		logger.Sugar.Warnf("Could not list S3 directory: %v", err)
		return "", fmt.Errorf("failed to list S3 directory: %w", err)
	}

	downloadedAny := false
	for _, fileName := range files {
		if !strings.HasSuffix(fileName, ".parquet") {
			continue
		}

		fileURL := baseURL + fileName
		localPath := filepath.Join(localDir, fileName)

		if err := p.downloadFile(ctx, fileURL, localPath); err != nil {
			logger.Sugar.Debugf("Failed to download %s: %v", fileName, err)
			continue
		}
		downloadedAny = true
		logger.Sugar.Infof("Downloaded: %s", fileName)
	}

	if !downloadedAny {
		return "", fmt.Errorf("failed to download %s: no files found for date %s", dataType, date)
	}

	p.cacheMu.Lock()
	p.cache[localDir] = true
	p.cacheMu.Unlock()

	return localDir, nil
}

// listS3Directory lists files in an S3 directory using the REST API
func (p *AWSProvider) listS3Directory(ctx context.Context, dataType, date string) ([]string, error) {
	prefix := fmt.Sprintf("%s/%s/date=%s/", p.prefix, dataType, date)
	listURL := fmt.Sprintf("https://%s.s3.us-east-2.amazonaws.com/?list-type=2&prefix=%s&max-keys=100",
		p.bucket, prefix)

	req, err := http.NewRequestWithContext(ctx, "GET", listURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d listing S3", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var files []string
	keyPattern := regexp.MustCompile(`<Key>([^<]+)</Key>`)
	matches := keyPattern.FindAllStringSubmatch(string(body), -1)
	for _, match := range matches {
		if len(match) > 1 {
			key := match[1]
			parts := strings.Split(key, "/")
			if len(parts) > 0 {
				fileName := parts[len(parts)-1]
				if fileName != "" {
					files = append(files, fileName)
				}
			}
		}
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in listing")
	}

	return files, nil
}

// downloadFile downloads a file from URL to local path
func (p *AWSProvider) downloadFile(ctx context.Context, url, localPath string) error {
	if _, err := os.Stat(localPath); err == nil {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// ReadBlocks reads blocks from parquet files for a block range
func (p *AWSProvider) ReadBlocks(ctx context.Context, startBlock, endBlock int64) ([]*types.Block, error) {
	dates := p.getDateRangeForBlocks(startBlock, endBlock)

	var allBlocks []*types.Block

	for _, date := range dates {
		select {
		case <-ctx.Done():
			return allBlocks, ctx.Err()
		default:
		}

		localDir, err := p.DownloadDatePartition(ctx, date, "blocks")
		if err != nil {
			logger.Sugar.Warnf("Failed to download blocks for %s: %v", date, err)
			continue
		}

		blocks, err := p.readBlocksFromDirectory(localDir, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read blocks from %s: %v", localDir, err)
			continue
		}

		allBlocks = append(allBlocks, blocks...)
	}

	sort.Slice(allBlocks, func(i, j int) bool {
		return mustParseBlockNumber(allBlocks[i].Number) < mustParseBlockNumber(allBlocks[j].Number)
	})

	return allBlocks, nil
}

// readBlocksFromDirectory reads blocks from parquet files in a directory
func (p *AWSProvider) readBlocksFromDirectory(dir string, startBlock, endBlock int64) ([]*types.Block, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.parquet"))
	if err != nil {
		return nil, err
	}

	var blocks []*types.Block

	for _, file := range files {
		fileBlocks, err := p.readBlocksFromFile(file, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read %s: %v", file, err)
			continue
		}
		blocks = append(blocks, fileBlocks...)
	}

	return blocks, nil
}

// readBlocksFromFile reads blocks from a single parquet file using parquet-go
func (p *AWSProvider) readBlocksFromFile(filePath string, startBlock, endBlock int64) ([]*types.Block, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	pf, err := parquet.OpenFile(file, stat.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to open parquet file: %w", err)
	}

	// Log schema for debugging
	schema := pf.Schema()
	logger.Sugar.Infof("Parquet schema has %d fields", len(schema.Fields()))
	for i, field := range schema.Fields() {
		if i < 5 { // Log first 5 fields
			logger.Sugar.Debugf("  Field %d: %s", i, field.Name())
		}
	}
	logger.Sugar.Infof("File has %d rows", pf.NumRows())

	var blocks []*types.Block

	// Try using the generic reader
	reader := parquet.NewGenericReader[AWSBlock](pf)
	defer reader.Close()

	// Track min/max block numbers seen
	var minBlock, maxBlock int64 = -1, -1
	totalRead := 0

	// Read in batches
	batch := make([]AWSBlock, 1000)
	for {
		n, err := reader.Read(batch)
		if n > 0 {
			totalRead += n
		}
		if err != nil && err != io.EOF {
			logger.Sugar.Warnf("Error reading parquet: %v", err)
			return nil, fmt.Errorf("failed to read rows: %w", err)
		}
		if n == 0 {
			break
		}

		for i := 0; i < n; i++ {
			ab := &batch[i]
			
			// Track block number range
			if minBlock == -1 || ab.Number < minBlock {
				minBlock = ab.Number
			}
			if ab.Number > maxBlock {
				maxBlock = ab.Number
			}

			// Filter by block range
			if ab.Number < startBlock || (endBlock > 0 && ab.Number > endBlock) {
				continue
			}

			block := convertAWSBlockToBlock(ab)
			blocks = append(blocks, block)
		}

		if err == io.EOF {
			break
		}
	}

	logger.Sugar.Infof("File contains blocks %d to %d, looking for %d to %d", minBlock, maxBlock, startBlock, endBlock)
	logger.Sugar.Infof("Extracted %d blocks matching range", len(blocks))
	return blocks, nil
}

// ReadTransactions reads transactions from parquet files for a block range
func (p *AWSProvider) ReadTransactions(ctx context.Context, startBlock, endBlock int64) ([]*types.Transaction, error) {
	dates := p.getDateRangeForBlocks(startBlock, endBlock)

	var allTxs []*types.Transaction

	for _, date := range dates {
		select {
		case <-ctx.Done():
			return allTxs, ctx.Err()
		default:
		}

		localDir, err := p.DownloadDatePartition(ctx, date, "transactions")
		if err != nil {
			logger.Sugar.Warnf("Failed to download transactions for %s: %v", date, err)
			continue
		}

		txs, err := p.readTransactionsFromDirectory(localDir, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read transactions from %s: %v", localDir, err)
			continue
		}

		allTxs = append(allTxs, txs...)
	}

	return allTxs, nil
}

// readTransactionsFromDirectory reads transactions from parquet files in a directory
func (p *AWSProvider) readTransactionsFromDirectory(dir string, startBlock, endBlock int64) ([]*types.Transaction, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.parquet"))
	if err != nil {
		return nil, err
	}

	var txs []*types.Transaction

	for _, file := range files {
		fileTxs, err := p.readTransactionsFromFile(file, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read %s: %v", file, err)
			continue
		}
		txs = append(txs, fileTxs...)
	}

	return txs, nil
}

// readTransactionsFromFile reads transactions from a single parquet file
func (p *AWSProvider) readTransactionsFromFile(filePath string, startBlock, endBlock int64) ([]*types.Transaction, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	pf, err := parquet.OpenFile(file, stat.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to open parquet file: %w", err)
	}

	var txs []*types.Transaction

	reader := parquet.NewGenericReader[AWSTransaction](pf)
	defer reader.Close()

	batch := make([]AWSTransaction, 1000)
	for {
		n, err := reader.Read(batch)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("failed to read rows: %w", err)
		}
		if n == 0 {
			break
		}

		for i := 0; i < n; i++ {
			at := &batch[i]
			if at.BlockNumber < startBlock || (endBlock > 0 && at.BlockNumber > endBlock) {
				continue
			}

			tx := convertAWSTransactionToTransaction(at)
			txs = append(txs, tx)
		}

		if err == io.EOF {
			break
		}
	}

	return txs, nil
}

// ReadLogs reads logs from parquet files for a block range
func (p *AWSProvider) ReadLogs(ctx context.Context, startBlock, endBlock int64) ([]*types.Log, error) {
	dates := p.getDateRangeForBlocks(startBlock, endBlock)

	var allLogs []*types.Log

	for _, date := range dates {
		select {
		case <-ctx.Done():
			return allLogs, ctx.Err()
		default:
		}

		localDir, err := p.DownloadDatePartition(ctx, date, "logs")
		if err != nil {
			logger.Sugar.Warnf("Failed to download logs for %s: %v", date, err)
			continue
		}

		logs, err := p.readLogsFromDirectory(localDir, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read logs from %s: %v", localDir, err)
			continue
		}

		allLogs = append(allLogs, logs...)
	}

	return allLogs, nil
}

// readLogsFromDirectory reads logs from parquet files in a directory
func (p *AWSProvider) readLogsFromDirectory(dir string, startBlock, endBlock int64) ([]*types.Log, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.parquet"))
	if err != nil {
		return nil, err
	}

	var logs []*types.Log

	for _, file := range files {
		fileLogs, err := p.readLogsFromFile(file, startBlock, endBlock)
		if err != nil {
			logger.Sugar.Warnf("Failed to read %s: %v", file, err)
			continue
		}
		logs = append(logs, fileLogs...)
	}

	return logs, nil
}

// readLogsFromFile reads logs from a single parquet file
func (p *AWSProvider) readLogsFromFile(filePath string, startBlock, endBlock int64) ([]*types.Log, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	pf, err := parquet.OpenFile(file, stat.Size())
	if err != nil {
		return nil, fmt.Errorf("failed to open parquet file: %w", err)
	}

	var logs []*types.Log

	reader := parquet.NewGenericReader[AWSLog](pf)
	defer reader.Close()

	batch := make([]AWSLog, 1000)
	for {
		n, err := reader.Read(batch)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("failed to read rows: %w", err)
		}
		if n == 0 {
			break
		}

		for i := 0; i < n; i++ {
			al := &batch[i]
			if al.BlockNumber < startBlock || (endBlock > 0 && al.BlockNumber > endBlock) {
				continue
			}

			log := convertAWSLogToLog(al)
			logs = append(logs, log)
		}

		if err == io.EOF {
			break
		}
	}

	return logs, nil
}

// getDateRangeForBlocks returns the date range that covers the given block range
func (p *AWSProvider) getDateRangeForBlocks(startBlock, endBlock int64) []string {
	type anchor struct {
		block int64
		date  time.Time
	}

	// Updated anchors based on actual AWS data
	// June 5, 2024 contains blocks ~20,021,902 - 20,029,069 (~7167 blocks/day)
	anchors := []anchor{
		{0, time.Date(2015, 7, 30, 0, 0, 0, 0, time.UTC)},
		{5000000, time.Date(2018, 1, 30, 0, 0, 0, 0, time.UTC)},
		{10000000, time.Date(2020, 5, 4, 0, 0, 0, 0, time.UTC)},
		{15000000, time.Date(2022, 6, 21, 0, 0, 0, 0, time.UTC)},
		{15537394, time.Date(2022, 9, 15, 0, 0, 0, 0, time.UTC)},  // The Merge
		{17000000, time.Date(2023, 4, 10, 0, 0, 0, 0, time.UTC)},
		{18000000, time.Date(2023, 8, 28, 0, 0, 0, 0, time.UTC)},
		{19000000, time.Date(2024, 1, 16, 0, 0, 0, 0, time.UTC)},
		{20000000, time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)},   // Corrected: June 1-2, 2024
		{20025000, time.Date(2024, 6, 5, 0, 0, 0, 0, time.UTC)},   // From actual data
		{21000000, time.Date(2024, 10, 18, 0, 0, 0, 0, time.UTC)}, // Adjusted
		{22000000, time.Date(2025, 3, 7, 0, 0, 0, 0, time.UTC)},   // Adjusted
	}

	estimateDate := func(blockNum int64) time.Time {
		var lower, upper anchor
		lower = anchors[0]
		upper = anchors[len(anchors)-1]

		for i := 0; i < len(anchors)-1; i++ {
			if blockNum >= anchors[i].block && blockNum < anchors[i+1].block {
				lower = anchors[i]
				upper = anchors[i+1]
				break
			}
		}

		blockRange := upper.block - lower.block
		if blockRange == 0 {
			return lower.date
		}

		blockOffset := blockNum - lower.block
		timeRange := upper.date.Sub(lower.date)
		timeOffset := time.Duration(float64(timeRange) * float64(blockOffset) / float64(blockRange))

		return lower.date.Add(timeOffset)
	}

	startDate := estimateDate(startBlock)
	endDate := estimateDate(endBlock)
	if endBlock == 0 || endBlock <= startBlock {
		endDate = startDate.AddDate(0, 0, 1)
	}

	// Add buffer of 2 days on each side to ensure we find the blocks
	startDate = startDate.AddDate(0, 0, -2)
	endDate = endDate.AddDate(0, 0, 2)

	var dates []string
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format("2006-01-02"))
	}

	logger.Sugar.Infof("Block range %d-%d maps to dates %s to %s", startBlock, endBlock, dates[0], dates[len(dates)-1])

	return dates
}

// Conversion functions - struct based

func convertAWSBlockToBlock(ab *AWSBlock) *types.Block {
	baseFee := ""
	if ab.BaseFeePerGas != nil {
		baseFee = fmt.Sprintf("0x%x", *ab.BaseFeePerGas)
	}

	return &types.Block{
		Hash:             ab.Hash,
		Number:           fmt.Sprintf("0x%x", ab.Number),
		Timestamp:        fmt.Sprintf("0x%x", ab.Timestamp),
		ParentHash:       ab.ParentHash,
		Difficulty:       fmt.Sprintf("0x%x", int64(ab.Difficulty)),
		TotalDifficulty:  fmt.Sprintf("0x%x", int64(ab.TotalDifficulty)),
		GasUsed:          fmt.Sprintf("0x%x", ab.GasUsed),
		GasLimit:         fmt.Sprintf("0x%x", ab.GasLimit),
		BaseFeePerGas:    baseFee,
		Nonce:            ab.Nonce,
		Miner:            ab.Miner,
		Size:             fmt.Sprintf("0x%x", ab.Size),
		StateRoot:        ab.StateRoot,
		Sha3Uncles:       ab.Sha3Uncles,
		TransactionsRoot: ab.TransactionsRoot,
		ReceiptsRoot:     ab.ReceiptsRoot,
		LogsBloom:        ab.LogsBloom,
		ExtraData:        ab.ExtraData,
		MixHash:          "",
		Uncles:           []string{},
	}
}

func convertAWSTransactionToTransaction(at *AWSTransaction) *types.Transaction {
	maxFee := ""
	if at.MaxFeePerGas != nil {
		maxFee = fmt.Sprintf("0x%x", *at.MaxFeePerGas)
	}
	maxPriority := ""
	if at.MaxPriorityFeePerGas != nil {
		maxPriority = fmt.Sprintf("0x%x", *at.MaxPriorityFeePerGas)
	}

	toAddr := ""
	if at.ToAddress != nil {
		toAddr = *at.ToAddress
	}

	return &types.Transaction{
		Hash:                 at.Hash,
		BlockHash:            at.BlockHash,
		BlockNumber:          fmt.Sprintf("%d", at.BlockNumber),
		From:                 at.FromAddress,
		To:                   toAddr,
		Value:                fmt.Sprintf("0x%x", int64(at.Value)),
		Gas:                  fmt.Sprintf("0x%x", at.Gas),
		GasPrice:             fmt.Sprintf("0x%x", at.GasPrice),
		MaxFeePerGas:         maxFee,
		MaxPriorityFeePerGas: maxPriority,
		Input:                at.Input,
		Nonce:                fmt.Sprintf("0x%x", at.Nonce),
		TransactionIndex:     int(at.TransactionIndex),
		Type:                 fmt.Sprintf("0x%x", at.TransactionType),
		ChainId:              "0x1",
		V:                    "",
		R:                    "",
		S:                    "",
		Status:               at.ReceiptStatus == 1,
		GasUsed:              fmt.Sprintf("0x%x", at.ReceiptGasUsed),
		CumulativeGasUsed:    fmt.Sprintf("0x%x", at.ReceiptCumulativeGasUsed),
		EffectiveGasPrice:    fmt.Sprintf("0x%x", at.ReceiptEffectiveGasPrice),
		AccessList:           []types.AccessListEntry{},
	}
}

func convertAWSLogToLog(al *AWSLog) *types.Log {
	var topics []string
	if al.Topics != "" {
		topicsStr := strings.Trim(al.Topics, "[]")
		if topicsStr != "" {
			for _, t := range strings.Split(topicsStr, ",") {
				topics = append(topics, strings.Trim(t, "\" "))
			}
		}
	}

	return &types.Log{
		Address:          al.Address,
		Topics:           topics,
		Data:             al.Data,
		BlockNumber:      fmt.Sprintf("0x%x", al.BlockNumber),
		TransactionHash:  al.TransactionHash,
		TransactionIndex: int(al.TransactionIndex),
		BlockHash:        al.BlockHash,
		LogIndex:         int(al.LogIndex),
		Removed:          false,
	}
}

func mustParseBlockNumber(hexStr string) int64 {
	if strings.HasPrefix(hexStr, "0x") {
		var num int64
		fmt.Sscanf(hexStr, "0x%x", &num)
		return num
	}
	var num int64
	fmt.Sscanf(hexStr, "%d", &num)
	return num
}

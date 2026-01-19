#!/bin/bash
# Test Snapshot Providers
# Downloads sample data from various providers and compares them

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Test configuration
TEST_BLOCK_START=${1:-20000000}
TEST_BLOCK_END=${2:-20001000}
OUTPUT_DIR="./test_snapshots"
RPC_URL="${ETH_RPC_URL:-}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║           ETHEREUM SNAPSHOT PROVIDER TEST                  ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo "Test Block Range: $TEST_BLOCK_START - $TEST_BLOCK_END"
echo "Output Directory: $OUTPUT_DIR"
echo ""

# Create output directories
mkdir -p "$OUTPUT_DIR"/{aws,cryo,bigquery,comparison}

# Check for required tools
check_tools() {
    echo -e "${YELLOW}Checking required tools...${NC}"
    
    local missing=()
    
    command -v aws >/dev/null 2>&1 || missing+=("aws-cli")
    command -v jq >/dev/null 2>&1 || missing+=("jq")
    command -v go >/dev/null 2>&1 || missing+=("go")
    
    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}Missing required tools: ${missing[*]}${NC}"
        echo "Please install them and try again."
        exit 1
    fi
    
    echo -e "${GREEN}All required tools found.${NC}"
}

# Test AWS Public Blockchain
test_aws() {
    echo ""
    echo -e "${BLUE}=== Testing AWS Public Blockchain ===${NC}"
    
    # Calculate date from block number (approximate)
    # Post-merge: ~7200 blocks/day
    # Genesis: 2015-07-30
    local DAYS_SINCE_GENESIS=$((TEST_BLOCK_START / 7200))
    local TEST_DATE=$(date -d "2015-07-30 + $DAYS_SINCE_GENESIS days" +%Y-%m-%d 2>/dev/null || \
                      date -v+${DAYS_SINCE_GENESIS}d -j -f "%Y-%m-%d" "2015-07-30" +%Y-%m-%d)
    
    echo "Estimated date for block $TEST_BLOCK_START: $TEST_DATE"
    
    # Download blocks
    echo "Downloading blocks..."
    aws s3 cp "s3://aws-public-blockchain/v1.0/eth/blocks/date=$TEST_DATE/" \
        "$OUTPUT_DIR/aws/blocks/" --recursive --no-sign-request 2>/dev/null || {
        echo -e "${RED}Failed to download blocks from AWS${NC}"
        return 1
    }
    
    # Download transactions
    echo "Downloading transactions..."
    aws s3 cp "s3://aws-public-blockchain/v1.0/eth/transactions/date=$TEST_DATE/" \
        "$OUTPUT_DIR/aws/transactions/" --recursive --no-sign-request 2>/dev/null || {
        echo -e "${RED}Failed to download transactions from AWS${NC}"
        return 1
    }
    
    # Download logs
    echo "Downloading logs..."
    aws s3 cp "s3://aws-public-blockchain/v1.0/eth/logs/date=$TEST_DATE/" \
        "$OUTPUT_DIR/aws/logs/" --recursive --no-sign-request 2>/dev/null || {
        echo -e "${RED}Failed to download logs from AWS${NC}"
        return 1
    }
    
    # Count files
    local BLOCK_FILES=$(find "$OUTPUT_DIR/aws/blocks" -name "*.parquet" 2>/dev/null | wc -l)
    local TX_FILES=$(find "$OUTPUT_DIR/aws/transactions" -name "*.parquet" 2>/dev/null | wc -l)
    local LOG_FILES=$(find "$OUTPUT_DIR/aws/logs" -name "*.parquet" 2>/dev/null | wc -l)
    
    echo -e "${GREEN}AWS Download Complete:${NC}"
    echo "  Block files: $BLOCK_FILES"
    echo "  Transaction files: $TX_FILES"
    echo "  Log files: $LOG_FILES"
    
    return 0
}

# Test with Cryo (if installed)
test_cryo() {
    echo ""
    echo -e "${BLUE}=== Testing Cryo ===${NC}"
    
    if ! command -v cryo >/dev/null 2>&1; then
        echo -e "${YELLOW}Cryo not installed. Skipping.${NC}"
        echo "Install with: cargo install cryo_cli"
        return 1
    fi
    
    if [[ -z "$RPC_URL" ]]; then
        echo -e "${YELLOW}ETH_RPC_URL not set. Skipping Cryo test.${NC}"
        return 1
    fi
    
    echo "Extracting with Cryo..."
    cryo blocks transactions logs \
        --blocks "${TEST_BLOCK_START}:${TEST_BLOCK_END}" \
        --rpc "$RPC_URL" \
        --output-dir "$OUTPUT_DIR/cryo" \
        --requests-per-second 50 || {
        echo -e "${RED}Cryo extraction failed${NC}"
        return 1
    }
    
    local CRYO_FILES=$(find "$OUTPUT_DIR/cryo" -name "*.parquet" 2>/dev/null | wc -l)
    echo -e "${GREEN}Cryo extraction complete: $CRYO_FILES files${NC}"
    
    return 0
}

# Test BigQuery export
test_bigquery() {
    echo ""
    echo -e "${BLUE}=== Testing BigQuery ===${NC}"
    
    if ! command -v bq >/dev/null 2>&1; then
        echo -e "${YELLOW}BigQuery CLI not installed. Skipping.${NC}"
        echo "Install with: gcloud components install bq"
        return 1
    fi
    
    # Check for GCP credentials
    if [[ -z "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]] && ! gcloud auth list 2>&1 | grep -q "ACTIVE"; then
        echo -e "${YELLOW}GCP not authenticated. Skipping BigQuery test.${NC}"
        return 1
    fi
    
    echo "Querying BigQuery..."
    bq query --nouse_legacy_sql --format=csv \
        "SELECT number, hash, parent_hash, timestamp, transaction_count 
         FROM \`bigquery-public-data.crypto_ethereum.blocks\`
         WHERE number BETWEEN $TEST_BLOCK_START AND $TEST_BLOCK_END
         ORDER BY number
         LIMIT 1000" > "$OUTPUT_DIR/bigquery/blocks.csv" 2>/dev/null || {
        echo -e "${RED}BigQuery query failed${NC}"
        return 1
    }
    
    local ROW_COUNT=$(wc -l < "$OUTPUT_DIR/bigquery/blocks.csv")
    echo -e "${GREEN}BigQuery query complete: $((ROW_COUNT - 1)) rows${NC}"
    
    return 0
}

# Compare data from different sources
compare_sources() {
    echo ""
    echo -e "${BLUE}=== Comparing Data Sources ===${NC}"
    
    # Build comparison tool if needed
    if [[ ! -f "$PROJECT_ROOT/bin/snapshot_migrate" ]]; then
        echo "Building migration tool..."
        cd "$PROJECT_ROOT"
        go build -o bin/snapshot_migrate ./cmd/snapshot_migrate
    fi
    
    # Run dry-run migration to parse and compare
    "$PROJECT_ROOT/bin/snapshot_migrate" \
        --provider aws \
        --start "$TEST_BLOCK_START" \
        --end "$TEST_BLOCK_END" \
        --output "$OUTPUT_DIR/aws" \
        --dry-run \
        --config config/config.yaml 2>&1 | tee "$OUTPUT_DIR/comparison/aws_parse.log"
    
    echo -e "${GREEN}Comparison complete. Check $OUTPUT_DIR/comparison/ for results.${NC}"
}

# Validate against RPC
validate_against_rpc() {
    echo ""
    echo -e "${BLUE}=== Validating Against RPC ===${NC}"
    
    if [[ -z "$RPC_URL" ]]; then
        echo -e "${YELLOW}ETH_RPC_URL not set. Skipping RPC validation.${NC}"
        echo "Set ETH_RPC_URL environment variable to enable validation."
        return 1
    fi
    
    "$PROJECT_ROOT/bin/snapshot_migrate" \
        --provider aws \
        --start "$TEST_BLOCK_START" \
        --end "$TEST_BLOCK_END" \
        --output "$OUTPUT_DIR/aws" \
        --dry-run \
        --validate \
        --validate-sample 50 \
        --rpc "$RPC_URL" \
        --config config/config.yaml 2>&1 | tee "$OUTPUT_DIR/comparison/validation.log"
    
    echo -e "${GREEN}Validation complete.${NC}"
}

# Print summary
print_summary() {
    echo ""
    echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║                       SUMMARY                              ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
    
    echo ""
    echo "Test Results:"
    echo ""
    
    # Check each provider
    if [[ -d "$OUTPUT_DIR/aws/blocks" ]] && [[ -n "$(ls -A "$OUTPUT_DIR/aws/blocks" 2>/dev/null)" ]]; then
        echo -e "  AWS:      ${GREEN}✓ Success${NC}"
        echo "            $(find "$OUTPUT_DIR/aws" -name "*.parquet" 2>/dev/null | wc -l) parquet files"
    else
        echo -e "  AWS:      ${RED}✗ Failed or no data${NC}"
    fi
    
    if [[ -d "$OUTPUT_DIR/cryo" ]] && [[ -n "$(ls -A "$OUTPUT_DIR/cryo" 2>/dev/null)" ]]; then
        echo -e "  Cryo:     ${GREEN}✓ Success${NC}"
        echo "            $(find "$OUTPUT_DIR/cryo" -name "*.parquet" 2>/dev/null | wc -l) parquet files"
    else
        echo -e "  Cryo:     ${YELLOW}○ Skipped${NC}"
    fi
    
    if [[ -f "$OUTPUT_DIR/bigquery/blocks.csv" ]]; then
        echo -e "  BigQuery: ${GREEN}✓ Success${NC}"
        echo "            $(wc -l < "$OUTPUT_DIR/bigquery/blocks.csv") rows"
    else
        echo -e "  BigQuery: ${YELLOW}○ Skipped${NC}"
    fi
    
    echo ""
    echo "Output directory: $OUTPUT_DIR"
    echo ""
    echo "Next steps:"
    echo "  1. Review the downloaded data in $OUTPUT_DIR"
    echo "  2. Run full migration with: ./scripts/migrate.sh -s $TEST_BLOCK_START"
    echo "  3. Check comparison logs in $OUTPUT_DIR/comparison/"
}

# Main execution
main() {
    check_tools
    
    test_aws || true
    test_cryo || true
    test_bigquery || true
    
    if [[ -f "$PROJECT_ROOT/cmd/snapshot_migrate/main.go" ]]; then
        compare_sources || true
        validate_against_rpc || true
    fi
    
    print_summary
}

main "$@"

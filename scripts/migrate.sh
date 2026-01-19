#!/bin/bash
# Ethereum Snapshot Migration Tool
# Usage: ./scripts/migrate.sh [options]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Default values
PROVIDER="aws"
START_BLOCK=0
END_BLOCK=0
BATCH_SIZE=1000
WORKERS=4
DRY_RUN=false
VALIDATE=false
VALIDATE_SAMPLE=100
OUTPUT_DIR="./snapshot_data"
CONFIG_FILE="config/config.yaml"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

show_help() {
    cat << EOF
Ethereum Snapshot Migration Tool

Usage: $0 [options]

Options:
    -p, --provider       Data provider (aws, bigquery, cryo) [default: aws]
    -s, --start          Start block number [default: 0]
    -e, --end            End block number [default: latest available]
    -b, --batch          Batch size (blocks per batch) [default: 1000]
    -w, --workers        Number of parallel workers [default: 4]
    -o, --output         Output directory for downloaded data [default: ./snapshot_data]
    -c, --config         Config file path [default: config/config.yaml]
    --dry-run            Download and parse data without importing
    --validate           Validate imported data against RPC
    --validate-sample    Number of blocks to sample for validation [default: 100]
    --resume             Resume from checkpoint
    -h, --help           Show this help message

Examples:
    # Test with dry run (download only, no import)
    $0 --dry-run -s 20000000 -e 20001000

    # Import 10,000 blocks with validation
    $0 -s 20000000 -e 20010000 --validate

    # Full migration with more workers
    $0 -s 0 -e 21000000 -w 8 -b 5000

    # Resume from checkpoint
    $0 --resume
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -p|--provider)
            PROVIDER="$2"
            shift 2
            ;;
        -s|--start)
            START_BLOCK="$2"
            shift 2
            ;;
        -e|--end)
            END_BLOCK="$2"
            shift 2
            ;;
        -b|--batch)
            BATCH_SIZE="$2"
            shift 2
            ;;
        -w|--workers)
            WORKERS="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -c|--config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --validate)
            VALIDATE=true
            shift
            ;;
        --validate-sample)
            VALIDATE_SAMPLE="$2"
            shift 2
            ;;
        --resume)
            RESUME=true
            shift
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            show_help
            exit 1
            ;;
    esac
done

# Check if binary exists
BINARY="$PROJECT_ROOT/bin/snapshot_migrate"
if [[ ! -f "$BINARY" ]]; then
    echo -e "${YELLOW}Binary not found. Building...${NC}"
    cd "$PROJECT_ROOT"
    go build -o bin/snapshot_migrate ./cmd/snapshot_migrate
    echo -e "${GREEN}Build complete.${NC}"
fi

# Build command
CMD="$BINARY"
CMD="$CMD --provider $PROVIDER"
CMD="$CMD --start $START_BLOCK"
CMD="$CMD --end $END_BLOCK"
CMD="$CMD --batch $BATCH_SIZE"
CMD="$CMD --workers $WORKERS"
CMD="$CMD --output $OUTPUT_DIR"
CMD="$CMD --config $CONFIG_FILE"

if [[ "$DRY_RUN" == "true" ]]; then
    CMD="$CMD --dry-run"
fi

if [[ "$VALIDATE" == "true" ]]; then
    CMD="$CMD --validate --validate-sample $VALIDATE_SAMPLE"
fi

if [[ "$RESUME" == "true" ]]; then
    # Load resume block from checkpoint
    CHECKPOINT_FILE="$OUTPUT_DIR/checkpoint.json"
    if [[ -f "$CHECKPOINT_FILE" ]]; then
        RESUME_BLOCK=$(jq -r '.last_block' "$CHECKPOINT_FILE" 2>/dev/null || echo "0")
        if [[ "$RESUME_BLOCK" != "0" && "$RESUME_BLOCK" != "null" ]]; then
            CMD="$CMD --resume $((RESUME_BLOCK + 1))"
            echo -e "${GREEN}Resuming from block $((RESUME_BLOCK + 1))${NC}"
        fi
    else
        echo -e "${YELLOW}No checkpoint found, starting from beginning${NC}"
    fi
fi

# Print configuration
echo ""
echo -e "${GREEN}=== Ethereum Snapshot Migration ===${NC}"
echo "Provider:    $PROVIDER"
echo "Start Block: $START_BLOCK"
echo "End Block:   $END_BLOCK"
echo "Batch Size:  $BATCH_SIZE"
echo "Workers:     $WORKERS"
echo "Output Dir:  $OUTPUT_DIR"
echo "Dry Run:     $DRY_RUN"
echo "Validate:    $VALIDATE"
echo ""

# Confirm before running
if [[ "$DRY_RUN" != "true" ]]; then
    read -p "Start migration? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted."
        exit 0
    fi
fi

# Run migration
echo -e "${GREEN}Starting migration...${NC}"
echo "Command: $CMD"
echo ""

exec $CMD

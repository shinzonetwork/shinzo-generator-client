# Deployment Configuration

This document describes the CI/CD setup for auto-deploying the indexer.

## Architecture

```
Push to main → GitHub Actions (test + build) → Push to GHCR → Watchtower pulls → Container restarts
```

**Watchtower** runs on the VM and automatically pulls new `:latest` images every 5 minutes.

## Required GitHub Secrets

| Secret | Required | Description |
|--------|----------|-------------|
| `GETH_RPC_URL` | Yes | Ethereum JSON-RPC endpoint URL |
| `GETH_WS_URL` | Yes | Ethereum WebSocket endpoint URL |
| `GETH_API_KEY` | Yes | API key for Ethereum node authentication |

## Workflow Behavior

### Triggers
- **Push to `main`**: Runs tests, builds image, pushes to GHCR
- **Manual dispatch**: Trigger build via Actions UI

### Deploy Process
1. Run tests
2. Build Docker image with SHA tag
3. Push to GHCR with `:latest` and `:sha-<commit>` tags
4. Watchtower detects new image within ~5 minutes
5. Watchtower stops old container, pulls new image, starts new container

### Image Tags
- `ghcr.io/shinzonetwork/indexer:latest` - Most recent main build (Watchtower watches this)
- `ghcr.io/shinzonetwork/indexer:sha-<7chars>` - Specific commit for rollback

## VM Setup

### Prerequisites
- Docker installed
- GHCR authentication configured (`docker login ghcr.io`)
- `.env` file with runtime configuration

### Watchtower Setup

```bash
# Login to GHCR (one-time, credentials saved to ~/.docker/config.json)
echo "YOUR_GITHUB_PAT" | docker login ghcr.io -u YOUR_USERNAME --password-stdin

# Start Watchtower
docker run -d \
  --name watchtower \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ~/.docker/config.json:/config.json:ro \
  -e WATCHTOWER_POLL_INTERVAL=300 \
  -e WATCHTOWER_CLEANUP=true \
  -e WATCHTOWER_LABEL_ENABLE=true \
  containrrr/watchtower
```

### Indexer Container

```bash
docker run -d \
  --label com.centurylinklabs.watchtower.enable=true \
  --name shinzo-indexer \
  --restart unless-stopped \
  -p 8080:8080 \
  -p 9171:9171 \
  -v /mnt/defradb-data:/app/.defra \
  -v /mnt/defradb-data/logs:/app/logs \
  --env-file .env \
  --health-cmd="curl -f http://localhost:8080/health || exit 1" \
  --health-interval=30s \
  --health-timeout=10s \
  --health-retries=3 \
  --health-start-period=60s \
  ghcr.io/shinzonetwork/indexer:latest
```

## Troubleshooting

### Check Watchtower logs
```bash
docker logs watchtower --tail 50
```

### Check if Watchtower can pull images
```bash
docker pull ghcr.io/shinzonetwork/indexer:latest
```

### Force immediate update
```bash
docker exec watchtower /watchtower --run-once
```

### Manual rollback
```bash
# Stop current container
docker stop shinzo-indexer && docker rm shinzo-indexer

# Start with specific SHA tag
docker run -d \
  --label com.centurylinklabs.watchtower.enable=true \
  --name shinzo-indexer \
  ... \
  ghcr.io/shinzonetwork/indexer:sha-abc1234
```

### Container not updating
- Verify the container has the Watchtower label: `docker inspect shinzo-indexer | grep watchtower`
- Check GHCR credentials: `cat ~/.docker/config.json`
- Verify Watchtower is running: `docker ps | grep watchtower`

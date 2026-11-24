# Deployment Configuration

How generator images are released and deployed.

## Architecture

```
git tag v<ver>-<chain>-<network> → release.yml (build + verify) → GHCR image per chain → Watchtower pulls → container restarts
```

Each generator runs **one chain**. The image for a chain carries that chain's config, baked in at build time, so `CMD`, compose files and k8s manifests are the same for every chain.

## Releasing

Push a tag of the form `v<version>-<chain>-<network>`:

```bash
git tag v0.7.0-polygon-mainnet && git push origin v0.7.0-polygon-mainnet
```

`release.yml` then:
1. runs `make build` (every binary), then `make evm-build` and checks that the version is injected into `bin/block_poster_evm`;
2. parses `<chain>` and `<network>` from the tag, lower-cased (a malformed tag fails here);
3. builds the image with `--build-arg CHAIN=<chain> --build-arg NETWORK=<network>`;
4. pushes `:<tag>` and the floating `:<chain>-<network>-latest`;
5. creates a GitHub release with `bin/block_poster_evm` attached.

### Config selection (Dockerfile)
| Tag chain / network | Baked config |
|---|---|
| `ethereum` / `mainnet` | `config/config.yaml` |
| `<chain>` / `mainnet` | `config/config_<chain>.yaml` |
| `<chain>` / `<network>` | `config/config_<chain>_<network>.yaml` |

The build **fails** if that file is missing, or if its `chain.adapter` is not `evm`. The image only contains the EVM binary, so a Bitcoin, Tron or cosmos-evm tag cannot produce an image yet. Non-EVM families need their own image path, which is not built yet.

### Published chains
| Tag suffix | Config |
|---|---|
| `ethereum-mainnet` | `config.yaml` |
| `bsc-mainnet` | `config_bsc.yaml` |
| `polygon-mainnet` | `config_polygon.yaml` |
| `polygon-amoy` | `config_polygon_amoy.yaml` |
| `avalanche-mainnet` | `config_avalanche.yaml` |
| `celo-mainnet` | `config_celo.yaml` |
| `kaia-mainnet` | `config_kaia.yaml` |

### Image tags
- `ghcr.io/shinzonetwork/shinzo-generator-client:<chain>-<network>-latest`: newest release for that chain (Watchtower watches this).
- `ghcr.io/shinzonetwork/shinzo-generator-client:v<version>-<chain>-<network>`: a specific release, for pinning and rollback.

### Local image
```bash
make docker-build DOCKER_CHAIN=polygon DOCKER_NETWORK=amoy   # → shinzo-generator:polygon-amoy
CHAIN=polygon NETWORK=amoy docker compose -f docker-compose-local.yml up --build
```

## Runtime configuration

The baked config names the chain, chain ID and cadence. The deployment supplies only the endpoint and secrets, through `.env`:

| Variable | Required | Description |
|---|---|---|
| `GENERATOR_IMAGE_TAG` | No | Compose image tag; default `ethereum-mainnet-latest` |
| `GETH_RPC_URL` | Yes | JSON-RPC endpoint **for the image's chain** |
| `GETH_WS_URL` | No | WebSocket endpoint; leave empty for HTTP polling |
| `GETH_API_KEY` / `GETH_API_KEY_TYPE` | Provider-dependent | API key and its header name |
| `GETH_DIAL_TIMEOUT_SECONDS` | No | Bounds the WS dial at startup; on timeout the generator starts HTTP-only |
| `DEFRADB_KEYRING_SECRET` | Yes | Keyring secret for the embedded DefraDB |

- The generator refuses to start when `eth_chainId` doesn't match the baked `chain_id`, so a Polygon image pointed at an Ethereum RPC fails fast.
- Don't set `CHAIN_NAME`, `CHAIN_NETWORK` or `CHAIN_ID` unless you mean to override the baked chain. If you do, change all three together.
- Per-chain configs name a local store (`./.defra-<chain>`). The image overrides this with `DEFRADB_STORE_PATH=/app/.defra` and `SNAPSHOT_DIR=/app/.defra/snapshots`, so every chain uses the mounted data volume.
- **Changing an existing VM to another chain means a fresh data volume.** Collections are chain-prefixed, but one store should hold one chain.

## VM Setup

### Prerequisites
- Docker installed
- GHCR authentication configured (`docker login ghcr.io`)
- A `.env` file with the runtime configuration above

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

### Generator Container

With compose (recommended): set `GENERATOR_IMAGE_TAG` in `.env`, then `docker compose up -d`.

Or directly:
```bash
docker run -d \
  --label com.centurylinklabs.watchtower.enable=true \
  --name shinzo-generator \
  --restart unless-stopped \
  -p 8080:8080 \
  -p 9171:9171 \
  -v ~/shinzo-data/defradb:/app/.defra \
  -v ~/shinzo-data/keys:/app/keys \
  --env-file .env \
  --health-cmd="curl -f http://localhost:8080/health || exit 1" \
  --health-interval=30s \
  --health-timeout=10s \
  --health-retries=3 \
  --health-start-period=60s \
  ghcr.io/shinzonetwork/shinzo-generator-client:polygon-mainnet-latest
```

## Troubleshooting

### Which chain is this image?
```bash
docker inspect --format '{{index .Config.Labels "network.shinzo.chain"}}-{{index .Config.Labels "network.shinzo.network"}}' \
  ghcr.io/shinzonetwork/shinzo-generator-client:polygon-mainnet-latest
```

### Check Watchtower logs
```bash
docker logs watchtower --tail 50
```

### Force immediate update
```bash
docker exec watchtower /watchtower --run-once
```

### Manual rollback
```bash
docker stop shinzo-generator && docker rm shinzo-generator
GENERATOR_IMAGE_TAG=v0.6.5.4-polygon-mainnet docker compose up -d
```

### Container not updating
- Verify the container has the Watchtower label: `docker inspect shinzo-generator | grep watchtower`
- Check GHCR credentials: `cat ~/.docker/config.json`
- Verify Watchtower is running: `docker ps | grep watchtower`

### Release failed at "Verify version injection"
The release workflow checks `bin/block_poster_evm`. Before this change it checked `bin/block_poster`, which `make evm-build` stopped producing in #521, so every tag failed at this step.

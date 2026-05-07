# Sets up SSL certificates, installs Docker, and starts the indexer via
# docker compose. Run this after indexer-prod-setup.sh has been executed.

set -e &&
sudo mkdir -p ~/ssl &&
sudo openssl genrsa -out ~/ssl/nginx.key 2048 &&
sudo openssl req -new -key ~/ssl/nginx.key -out /tmp/nginx.csr -subj "/C=US/ST=State/L=City/O=Shinzo/OU=Host Client/CN=shinzo.network" &&
sudo openssl x509 -req -days 365 -in /tmp/nginx.csr -signkey ~/ssl/nginx.key -out ~/ssl/nginx.crt &&
sudo rm /tmp/nginx.csr

if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  sudo apt-get update &&
  sudo apt-get install -y docker.io docker-compose-v2
fi &&
sudo mkdir -p ~/data/defradb ~/data/lens &&
sudo chown -R 0:0 ~/data/defradb ~/data/lens &&
docker compose up -d

# Generate private key, certificate signing request, and self-signed certificate
set -e &&
sudo mkdir -p ~/ssl &&
sudo openssl genrsa -out ~/ssl/nginx.key 2048 &&
sudo openssl req -new -key ~/ssl/nginx.key -out /tmp/nginx.csr -subj "/C=US/ST=State/L=City/O=Shinzo/OU=Generator Client/CN=shinzo.network" &&
sudo openssl x509 -req -days 365 -in /tmp/nginx.csr -signkey ~/ssl/nginx.key -out ~/ssl/nginx.crt &&
sudo rm /tmp/nginx.csr

sudo apt-get update &&
sudo apt-get install -y docker.io docker-compose &&
sudo mkdir -p ~/shinzo-data/defradb ~/shinzo-data/lens &&
sudo chown -R 1001:1001 ~/shinzo-data/defradb ~/shinzo-data/lens &&
docker-compose up -d

set -e &&
sudo apt-get update &&
sudo apt-get install -y docker.io docker-compose &&
sudo mkdir -p ~/data/defradb ~/data/lens &&
sudo chown -R 1003:1006 ~/data/defradb ~/data/lens &&
docker-compose up -d

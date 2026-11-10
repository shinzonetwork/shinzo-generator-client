sudo apt-get update &&
sudo apt-get install -y docker.io docker-compose &&
sudo mkdir -p ~/shinzo-data/defradb ~/shinzo-data/lens ~/shinzo-data/keys &&
sudo chown -R 1001:1001 ~/shinzo-data/defradb ~/shinzo-data/lens ~/shinzo-data/keys &&
sudo docker-compose up -d

#!/bin/sh
set -x # show commands in execution

CONTAINER_NAME="ships-npc"
IMAGE_NAME="ships-npc-image"
ROOT_PATH="/home/jonbul/servers/ships"
PROJECT_PATH="$ROOT_PATH/ships-npc"
ENV_PATH="$ROOT_PATH/files/.env"


echo "Usuario actual: $(whoami)"

echo "=== PARADA ==="
docker stop $CONTAINER_NAME 2>/dev/null || true
echo "=== BORRAR CONTENEDOR ==="
docker rm $CONTAINER_NAME 2>/dev/null || true
echo "=== BORRAR IMAGEN ==="
docker rmi $IMAGE_NAME:latest 2>/dev/null || true

set -e # exit on error

# Crear directorio de trabajo si no existe
mkdir -p "$PROJECT_PATH"
cd "$PROJECT_PATH"

# Descargar el binario compilado por GitHub Actions
echo "¿Descargar la última release o el último snapshot?"
echo "1) release"
echo "2) snapshot"
read -r answer

if [ "$answer" -eq 1 ]; then
    curl -L --fail https://github.com/jonbul/ships-npc/releases/latest/download/ships-npc -o ships-npc
elif [ "$answer" -eq 2 ]; then
    curl -L --fail https://github.com/jonbul/ships-npc/releases/download/latest-snapshot/ships-npc -o ships-npc
else
    echo "Opción inválida"
    exit 1
fi
chmod +x ships-npc

echo "=== CONSTRUIR IMAGEN DOCKER ==="
# Se genera el Dockerfile en tiempo de ejecución, sin depender de ningún archivo externo
cat > Dockerfile.tmp << 'DOCKERFILE'
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY ships-npc .
RUN chmod +x ships-npc
CMD ["./ships-npc"]
DOCKERFILE

docker build -f Dockerfile.tmp -t $IMAGE_NAME:latest .
rm Dockerfile.tmp

echo "=== CREAR RED DOCKER (si no existe) ==="
docker network inspect ships-network >/dev/null 2>&1 || docker network create ships-network

echo "=== ARRANCAR CONTENEDOR ==="
# Monta el fichero .env desde el directorio de trabajo (debe existir previamente).
# ships-npc no expone ningún puerto: solo abre una conexión saliente wss hacia
# ships-go, así que no hace falta publicar puertos ni montar certificados SSL
# (el cliente websocket omite la verificación del certificado, ver client.go).
# NPC_WS_URL en .env debe apuntar a un host resoluble desde este contenedor,
# p.ej. el nombre del contenedor de ships-go en la red "ships-network"
# (wss://localhost:3000/ws NO funcionará entre contenedores distintos).
docker run -d \
    --name $CONTAINER_NAME \
    --network container:ships-go \
    -v "$ENV_PATH:/app/.env:ro" \
    $IMAGE_NAME:latest

docker ps -a

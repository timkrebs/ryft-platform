#!/bin/sh
# Dev only (make up): a throwaway CA, the broker's certificate and one client
# certificate per service, so compose speaks AMQPS with client certificates as the
# cluster does (D98). Runs once per volume, in the RabbitMQ image (it has openssl).
# Nothing here is worth keeping secret: the CA exists only on this machine.
set -eu
d=/certs
[ -f "$d/ca.crt" ] && exit 0
cd "$d"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 \
  -subj "/CN=RYFT dev CA" -keyout ca.key -out ca.crt 2>/dev/null
cert() { # name subjectAltName extendedKeyUsage
  mkdir -p "$1"
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$1" \
    -keyout "$1/tls.key" -out "$1/tls.csr" 2>/dev/null
  printf 'subjectAltName=%s\nextendedKeyUsage=%s\n' "$2" "$3" > "$1/ext"
  openssl x509 -req -in "$1/tls.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 3650 \
    -out "$1/tls.crt" -extfile "$1/ext" 2>/dev/null
  cp ca.crt "$1/ca.crt"
  rm "$1/tls.csr" "$1/ext"
}
cert rabbitmq "DNS:rabbitmq,DNS:localhost" serverAuth,clientAuth
for s in ingest api scoring notify; do cert "$s" "DNS:$s" clientAuth; done
# The broker runs as uid 999, the services as 65532: everyone reads (dev only).
chmod -R a+rX "$d"
chmod a+r "$d"/*/tls.key
echo "dev AMQP certificates written"

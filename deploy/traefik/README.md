# Traefik self-signed cert

Generate a local self-signed cert (used when running with the `tls` profile):

```sh
mkdir -p certs
openssl req -x509 -nodes -newkey rsa:2048 \
  -keyout certs/mist.key -out certs/mist.crt \
  -days 365 \
  -subj "/CN=mist.localhost" \
  -addext "subjectAltName=DNS:mist.localhost,DNS:s3.mist.localhost,DNS:localhost"
```

Add to `/etc/hosts`:

```
127.0.0.1 mist.localhost s3.mist.localhost
```

Then `make run` from the repo root.

FROM registry.access.redhat.com/hi/go:1.27 AS builder
WORKDIR /opt/app-root/src
# Build context must be the repo root (gecko/). The module has no local replace
# directives, so only its own sources are needed.
COPY marketplace-signup/go.mod marketplace-signup/go.sum marketplace-signup/
RUN cd marketplace-signup && go mod download
COPY marketplace-signup/ marketplace-signup/
RUN cd marketplace-signup && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /opt/app-root/src/marketplace-signup-backend ./cmd/server

FROM registry.access.redhat.com/ubi9/ubi-micro:9.9-1794478307

LABEL name="gecko-marketplace-signup-backend" \
      com.redhat.component="gecko-marketplace-signup-backend" \
      version="0.0.1" \
      release="1" \
      description="Interim marketplace sign-up backend from the gecko project: verifies a Red Hat Google identity, activates the project through the activate-project workflow, and sends a best-effort notification." \
      io.k8s.description="Interim marketplace sign-up backend from the gecko project." \
      summary="Gecko Marketplace Sign-up Backend" \
      distribution-scope="private" \
      url="https://github.com/openshift-online/gecko" \
      vendor="Red Hat, Inc."

RUN mkdir /app && chown 65532:65532 /app
COPY --from=builder /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /opt/app-root/src/marketplace-signup-backend /app/marketplace-signup-backend
USER 65532:65532
WORKDIR /app
ENTRYPOINT ["/app/marketplace-signup-backend"]

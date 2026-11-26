# Static SPA served by a UBI nginx. Build context must be the repo root (gecko/).
FROM registry.access.redhat.com/ubi9/ubi-minimal:9.9-1795198179

LABEL name="gecko-marketplace-signup-frontend" \
      com.redhat.component="gecko-marketplace-signup-frontend" \
      version="0.0.1" \
      release="1" \
      description="Interim marketplace sign-up single-page app from the gecko project, served by nginx on an unprivileged port." \
      io.k8s.description="Interim marketplace sign-up SPA from the gecko project." \
      summary="Gecko Marketplace Sign-up Frontend" \
      distribution-scope="private" \
      url="https://github.com/openshift-online/gecko" \
      vendor="Red Hat, Inc."

RUN microdnf install -y --nodocs nginx && microdnf clean all && rm -rf /var/cache/dnf /var/cache/yum

# nginx listens on 8080 and writes only under /tmp; the SPA falls back to index.html.
COPY marketplace-signup/frontend/nginx.conf /etc/nginx/nginx.conf
# The assets are staged under /etc/signup. The entrypoint renders them, with the
# public configuration injected, into /usr/share/nginx/html, which the chart
# mounts as a writable volume because the root filesystem is read-only.
COPY marketplace-signup/frontend/index.html marketplace-signup/frontend/app.js marketplace-signup/frontend/styles.css /etc/signup/
COPY --chmod=0755 marketplace-signup/frontend/entrypoint.sh /usr/local/bin/signup-entrypoint

# nginx opens its compiled-in default log and state paths before it reads the
# configuration, so point them at /tmp (a writable volume at runtime). The
# package's default pages are removed so the image serves only the SPA.
RUN rm -rf /var/log/nginx && ln -s /tmp /var/log/nginx && \
    rm -rf /var/lib/nginx && ln -s /tmp/nginx-lib /var/lib/nginx && \
    rm -rf /usr/share/nginx/html && mkdir -p /usr/share/nginx/html /var/cache/nginx /run && \
    chown -R 65532:0 /usr/share/nginx /var/cache/nginx /run /etc/nginx && \
    chmod -R g+rwX /usr/share/nginx /var/cache/nginx /run

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/signup-entrypoint"]

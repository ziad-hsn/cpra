# Production packaging only. Named context release contains the tested native directories.
# Set VERSION, COMMIT and SOURCE_DATE_EPOCH from the approved RELEASE.json:
# docker buildx build --build-context release=dist/release -f docker/Dockerfile \
#   --build-arg VERSION --build-arg COMMIT --build-arg SOURCE_DATE_EPOCH .
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS runtime-base

FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS payload
ARG TARGETARCH
ARG SOURCE_DATE_EPOCH
COPY --from=runtime-base /etc/passwd /etc/group /payload/etc/
COPY --from=release /linux_${TARGETARCH}/cpra /linux_${TARGETARCH}/cpractl /payload/usr/local/bin/
COPY --from=release /linux_${TARGETARCH}/LICENSE /payload/usr/share/licenses/cpra/LICENSE
COPY --from=release /linux_${TARGETARCH}/LICENSES/ /payload/usr/share/licenses/cpra/LICENSES/
COPY --from=release /linux_${TARGETARCH}/THIRD_PARTY_NOTICES/ /payload/usr/share/licenses/cpra/THIRD_PARTY_NOTICES/
COPY --from=release /linux_${TARGETARCH}/RELEASE.json /linux_${TARGETARCH}/DEPENDENCIES.json /payload/usr/share/cpra/
COPY docker/runtime.yaml /payload/etc/cpra/runtime.yaml
# BuildKit rewrite-timestamp only clamps newer timestamps. Normalize every
# packaged inode explicitly so an older checkout/extraction produces the same
# final layer. Intermediate COPY layers, with original metadata, do not ship.
RUN case "$SOURCE_DATE_EPOCH" in ''|*[!0-9]*) exit 1 ;; esac && \
    test -s /etc/ssl/certs/ca-certificates.crt && \
    printf 'cpra:x:1001:1001:CPRa:/nonexistent:/sbin/nologin\n' >> /payload/etc/passwd && \
    printf 'cpra:x:1001:\n' >> /payload/etc/group && \
    mkdir -p /payload/var/lib/cpra && \
    find /payload -type d -exec chmod 0755 {} + && \
    find /payload -type f -exec chmod 0644 {} + && \
    chmod 0755 /payload/usr/local/bin/cpra /payload/usr/local/bin/cpractl && \
    chown 1001:1001 /payload/var/lib/cpra && chmod 0700 /payload/var/lib/cpra && \
    find /payload -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +

FROM runtime-base
ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE_DATE_EPOCH
LABEL org.opencontainers.image.title="CPRa" \
      org.opencontainers.image.source="https://github.com/ziad-hsn/cpra" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
# The pinned base supplies CA certificates. The final image contains only its
# layers and the normalized payload; no packages are fetched while packaging.
COPY --from=payload /payload/ /
WORKDIR /var/lib/cpra
USER 1001:1001
EXPOSE 8060
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/cpra"]
CMD ["-yaml", "/etc/cpra/monitors.yaml", "-runtime-config", "/etc/cpra/runtime.yaml"]

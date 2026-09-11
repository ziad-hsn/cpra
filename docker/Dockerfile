# Production packaging only. Named context release contains the tested native directories.
# docker buildx build --build-context release=dist/release -f docker/Dockerfile .
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG SOURCE_DATE_EPOCH
LABEL org.opencontainers.image.title="CPRa" \
      org.opencontainers.image.source="https://github.com/ziad-hsn/cpra" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="MIT"
# The pinned Alpine base already supplies ca-certificates-bundle. Do not resolve
# packages from a changing repository while wrapping verified executables.
RUN test -s /etc/ssl/certs/ca-certificates.crt && \
    printf 'cpra:x:1001:1001:CPRa:/nonexistent:/sbin/nologin\n' >> /etc/passwd && \
    printf 'cpra:x:1001:\n' >> /etc/group && \
    mkdir -p /var/lib/cpra && chown 1001:1001 /var/lib/cpra && chmod 0700 /var/lib/cpra
COPY --from=release /linux_${TARGETARCH}/cpra /linux_${TARGETARCH}/cpractl /usr/local/bin/
COPY --from=release /linux_${TARGETARCH}/LICENSE /usr/share/licenses/cpra/LICENSE
COPY --from=release /linux_${TARGETARCH}/LICENSES/ /usr/share/licenses/cpra/LICENSES/
COPY --from=release /linux_${TARGETARCH}/THIRD_PARTY_NOTICES/ /usr/share/licenses/cpra/THIRD_PARTY_NOTICES/
COPY --from=release /linux_${TARGETARCH}/RELEASE.json /linux_${TARGETARCH}/DEPENDENCIES.json /usr/share/cpra/
COPY docker/runtime.yaml /etc/cpra/runtime.yaml
WORKDIR /var/lib/cpra
USER 1001:1001
EXPOSE 8060
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/cpra"]
CMD ["-yaml", "/etc/cpra/monitors.yaml", "-runtime-config", "/etc/cpra/runtime.yaml"]

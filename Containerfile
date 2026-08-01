FROM docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS build
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X github.com/jahwag/zestmem/internal/buildinfo.Version=${VERSION} -X github.com/jahwag/zestmem/internal/buildinfo.Revision=${REVISION} -X github.com/jahwag/zestmem/internal/buildinfo.Date=${CREATED}" -o /out/memoryd ./cmd/memoryd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="ZestMem" \
      org.opencontainers.image.description="Distributed, context-bounded memory for autonomous agent teams." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.source="https://github.com/jahwag/zestmem" \
      org.opencontainers.image.licenses="LicenseRef-Proprietary"
COPY --from=build /out/memoryd /usr/local/bin/memoryd
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=30 CMD ["/usr/local/bin/memoryd", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/memoryd"]

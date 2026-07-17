FROM docker.io/library/golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58 AS build
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid= -X github.com/bytelope/zestmem/internal/buildinfo.Version=${VERSION} -X github.com/bytelope/zestmem/internal/buildinfo.Revision=${REVISION} -X github.com/bytelope/zestmem/internal/buildinfo.Date=${CREATED}" -o /out/memoryd ./cmd/memoryd

FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="ZestMem" \
      org.opencontainers.image.description="Distributed, context-bounded memory for autonomous agent teams." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.source="https://github.com/bytelope/zestmem" \
      org.opencontainers.image.licenses="LicenseRef-Proprietary"
COPY --from=build /out/memoryd /usr/local/bin/memoryd
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/memoryd"]
